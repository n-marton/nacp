package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/helper"
	"github.com/mxab/nacp/admissionctrl"
	"github.com/mxab/nacp/admissionctrl/mutator"
	"github.com/mxab/nacp/admissionctrl/opa"
	"github.com/mxab/nacp/admissionctrl/validator"
	"github.com/mxab/nacp/config"
)

type contextKeyWarnings struct{}
type contextKeyValidationErrors struct{}
type contextKeyValidationError struct{}

var (
	ctxWarnings        = contextKeyWarnings{}
	ctxValidationError = contextKeyValidationError{}
	jobPathRegex       = regexp.MustCompile(`^/v1/job/[a-zA-Z]+[a-z-Z0-9\-]*$`)
	jobPlanPathRegex   = regexp.MustCompile(`^/v1/job/[a-zA-Z]+[a-z-Z0-9\-]*/plan$`)
)

func NewProxyHandler(nomadAddress *url.URL, jobHandler *admissionctrl.JobHandler, appLogger hclog.Logger, transport *http.Transport) func(http.ResponseWriter, *http.Request) {

	// create a reverse proxy that catches "/v1/jobs" post calls
	// and forwards them to the jobs service
	// create a new reverse proxy

	proxy := httputil.NewSingleHostReverseProxy(nomadAddress)
	if transport != nil {
		proxy.Transport = transport
	}

	originalDirector := proxy.Director

	proxy.Director = func(r *http.Request) {
		originalDirector(r)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {

		var response interface{}
		var err error

		if isRegister(resp.Request) {
			response, err = handRegisterResponse(resp, appLogger)
		} else if isPlan(resp.Request) {
			response, err = handleJobPlanResponse(resp, appLogger)
		} else if isValidate(resp.Request) {
			response, err = handleJobValdidateResponse(resp, appLogger)
		}
		if err != nil {
			appLogger.Error("Preparing response failed", "error", err)
			return err
		}
		if response == nil {
			return nil
		}

		responeData, err := json.Marshal(response)

		if err != nil {
			appLogger.Error("Error marshalling job", "error", err)
			return err
		}

		rewriteResponse(resp, responeData)

		return nil
	}

	return func(w http.ResponseWriter, r *http.Request) {

		appLogger.Info("Request received", "path", r.URL.Path, "method", r.Method)

		var err error
		//var err error
		if isRegister(r) {
			r, err = handleRegister(r, appLogger, jobHandler)

		} else if isPlan(r) {

			r, err = handlePlan(r, appLogger, jobHandler)

		} else if isValidate(r) {
			r, err = handleValidate(r, appLogger, jobHandler)

		}
		if err != nil {
			appLogger.Warn("Error applying admission controllers", "error", err)
			writeError(w, err)

		} else {
			proxy.ServeHTTP(w, r)
		}

	}

}

func handRegisterResponse(resp *http.Response, appLogger hclog.Logger) (interface{}, error) {

	warnings, ok := resp.Request.Context().Value(ctxWarnings).([]error)
	if !ok && len(warnings) == 0 {
		return nil, nil
	}
	response := &api.JobRegisterResponse{}
	err := json.NewDecoder(resp.Body).Decode(response)
	if err != nil {
		appLogger.Error("Error decoding job", "error", err)
		return nil, err
	}
	appLogger.Info("Job after admission controllers", "job", response.JobModifyIndex)

	response.Warnings = buildFullWarningMsg(response.Warnings, warnings)

	return response, nil
}
func handleJobPlanResponse(resp *http.Response, appLogger hclog.Logger) (interface{}, error) {
	warnings, ok := resp.Request.Context().Value(ctxWarnings).([]error)
	if !ok && len(warnings) == 0 {
		return nil, nil
	}

	response := &api.JobPlanResponse{}
	err := json.NewDecoder(resp.Body).Decode(response)
	if err != nil {
		appLogger.Error("Error decoding job", "error", err)
		return nil, err
	}
	appLogger.Info("Job after admission controllers", "job", response.JobModifyIndex)

	response.Warnings = buildFullWarningMsg(response.Warnings, warnings)

	return response, nil
}
func handleJobValdidateResponse(resp *http.Response, appLogger hclog.Logger) (interface{}, error) {

	ctx := resp.Request.Context()
	validationErr, okErr := ctx.Value(ctxValidationError).(error)
	warnings, okWarnings := resp.Request.Context().Value(ctxWarnings).([]error)
	if !okErr && !okWarnings {
		return nil, nil
	}

	response := &api.JobValidateResponse{}
	err := json.NewDecoder(resp.Body).Decode(response)
	if err != nil {
		appLogger.Error("Error decoding job", "error", err)
		return nil, err
	}

	if validationErr != nil {
		validationErrors := []string{}
		var validationError string
		if merr, ok := validationErr.(*multierror.Error); ok {
			for _, err := range merr.Errors {
				validationErrors = append(validationErrors, err.Error())
			}
			validationError = merr.Error()
		} else {
			validationErrors = append(validationErrors, validationErr.Error())
			validationError = err.Error()
		}

		response.ValidationErrors = validationErrors
		response.Error = validationError
	}

	if len(warnings) > 0 {
		response.Warnings = buildFullWarningMsg(response.Warnings, warnings)
	}

	return response, nil
}

func buildFullWarningMsg(upstreamResponseWarnings string, warnings []error) string {
	allWarnings := &multierror.Error{}

	if upstreamResponseWarnings != "" {
		multierror.Append(allWarnings, fmt.Errorf("%s", upstreamResponseWarnings))
	}
	allWarnings = multierror.Append(allWarnings, warnings...)
	warningMsg := helper.MergeMultierrorWarnings(allWarnings)
	return warningMsg
}

func rewriteResponse(resp *http.Response, newResponeData []byte) {
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(newResponeData)))
	resp.ContentLength = int64(len(newResponeData))
	resp.Body = io.NopCloser(bytes.NewBuffer(newResponeData))
}

func rewriteRequest(r *http.Request, data []byte) {

	r.ContentLength = int64(len(data))
	r.Body = io.NopCloser(bytes.NewBuffer(data))
}

func handleRegister(r *http.Request, appLogger hclog.Logger, jobHandler *admissionctrl.JobHandler) (*http.Request, error) {
	body := r.Body
	jobRegisterRequest := &api.JobRegisterRequest{}

	if err := json.NewDecoder(body).Decode(jobRegisterRequest); err != nil {

		return r, fmt.Errorf("failed decoding job, skipping admission controller: %w", err)
	}
	orginalJob := jobRegisterRequest.Job

	job, warnings, err := jobHandler.ApplyAdmissionControllers(orginalJob)
	if err != nil {
		return r, fmt.Errorf("admission controllers send an error, returning error: %w", err)
	}
	jobRegisterRequest.Job = job

	data, err := json.Marshal(jobRegisterRequest)

	if err != nil {
		return r, fmt.Errorf("error marshalling job: %w", err)
	}

	ctx := r.Context()
	if len(warnings) > 0 {
		ctx = context.WithValue(ctx, ctxWarnings, warnings)
	}

	appLogger.Info("Job after admission controllers", "job", string(data))
	r = r.WithContext(ctx)
	rewriteRequest(r, data)
	return r, nil
}
func handlePlan(r *http.Request, appLogger hclog.Logger, jobHandler *admissionctrl.JobHandler) (*http.Request, error) {
	body := r.Body
	jobPlanRequest := &api.JobPlanRequest{}

	if err := json.NewDecoder(body).Decode(jobPlanRequest); err != nil {
		return r, fmt.Errorf("failed decoding job, skipping admission controller: %w", err)
	}
	orginalJob := jobPlanRequest.Job

	job, warnings, err := jobHandler.ApplyAdmissionControllers(orginalJob)
	if err != nil {
		return r, fmt.Errorf("admission controllers send an error, returning error: %w", err)
	}

	jobPlanRequest.Job = job

	data, err := json.Marshal(jobPlanRequest)

	if err != nil {
		return r, fmt.Errorf("error marshalling job: %w", err)
	}
	ctx := r.Context()
	if len(warnings) > 0 {
		ctx = context.WithValue(ctx, ctxWarnings, warnings)

	}
	r = r.WithContext(ctx)
	appLogger.Info("Job after admission controllers", "job", string(data))
	rewriteRequest(r, data)
	return r, nil
}

func handleValidate(r *http.Request, appLogger hclog.Logger, jobHandler *admissionctrl.JobHandler) (*http.Request, error) {

	body := r.Body
	jobValidateRequest := &api.JobValidateRequest{}
	err := json.NewDecoder(body).Decode(jobValidateRequest)
	if err != nil {
		appLogger.Error("Error decoding job", "error", err)
		return r, err
	}
	job := jobValidateRequest.Job

	job, mutateWarnings, err := jobHandler.AdmissionMutators(job)

	if err != nil {
		return r, err
	}
	jobValidateRequest.Job = job

	// args.Job = job

	// // Validate the job and capture any warnings
	// TODO: handle err
	validateWarnings, err := jobHandler.AdmissionValidators(job)
	//copied from https: //github.com/hashicorp/nomad/blob/v1.5.0/nomad/job_endpoint.go#L574

	ctx := r.Context()
	// if err != nil {
	ctx = context.WithValue(ctx, ctxValidationError, err)
	// 	if merr, ok := err.(*multierror.Error); ok {
	// 		for _, err := range merr.Errors {
	// 			validationErrors = append(validationErrors, err.Error())
	// 		}
	// 		errs = merr.Error()
	// 	} else {
	// 		validationErrors = append(validationErrors, err.Error())
	// 		errs = err.Error()
	// 	}

	// }

	validateWarnings = append(validateWarnings, mutateWarnings...)

	// // Set the warning message

	// reply.DriverConfigValidated = true
	data, err := json.Marshal(jobValidateRequest)
	if err != nil {
		return r, err
	}

	if len(validateWarnings) > 0 {
		ctx = context.WithValue(ctx, ctxWarnings, validateWarnings)

	}
	r = r.WithContext(ctx)
	// appLogger.Info("Job after admission controllers", "job", string(data))
	rewriteRequest(r, data)
	return r, nil

}

func writeError(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(err.Error()))
}
func isRegister(r *http.Request) bool {
	isRegister := isCreate(r) || isUpdate(r)
	return isRegister
}

func isCreate(r *http.Request) bool {
	return r.Method == "PUT" && r.URL.Path == "/v1/jobs"
}
func isUpdate(r *http.Request) bool {

	return r.Method == "PUT" && jobPathRegex.MatchString(r.URL.Path)
}
func isPlan(r *http.Request) bool {

	return r.Method == "PUT" && jobPlanPathRegex.MatchString(r.URL.Path)
}
func isValidate(r *http.Request) bool {

	return r.Method == "PUT" && r.URL.Path == "/v1/validate/job"
}

// https://www.codedodle.com/go-reverse-proxy-example.html
// https://joshsoftware.wordpress.com/2021/05/25/simple-and-powerful-reverseproxy-in-go/
func main() {

	appLogger := hclog.New(&hclog.LoggerOptions{
		Name:   "nacp",
		Level:  hclog.LevelFromString("DEBUG"),
		Output: os.Stdout,
	})

	appLogger.Info("Starting Nomad Admission Control Proxy")

	// and forwards them to the jobs service
	// create a new reverse proxy
	configPtr := flag.String("config", "", "point to a nacp config file")
	flag.Parse()
	var c *config.Config

	if _, err := os.Stat(*configPtr); err == nil && *configPtr != "" {
		c, err = config.LoadConfig(*configPtr)
		if err != nil {
			appLogger.Error("Failed to load config", "error", err)
			os.Exit(1)
		}
	} else {
		c = config.DefaultConfig()
	}

	backend, err := url.Parse(c.Nomad.Address)
	if err != nil {
		appLogger.Error("Failed to parse nomad address", "error", err)
		os.Exit(1)
	}
	var transport *http.Transport
	if c.Nomad.TLS != nil {
		transport, err = buildCustomTransport(*c.Nomad.TLS)
		if err != nil {
			appLogger.Error("Failed to create custom transport", "error", err)
			os.Exit(1)
		}
	}
	jobMutators, err := createMutatators(c, appLogger)
	if err != nil {
		appLogger.Error("Failed to create mutators", "error", err)
		os.Exit(1)
	}
	jobValidators, err := createValidators(c, appLogger)
	if err != nil {
		appLogger.Error("Failed to create validators", "error", err)
		os.Exit(1)
	}

	handler := admissionctrl.NewJobHandler(

		jobMutators,
		jobValidators,
		appLogger.Named("handler"),
	)

	proxy := NewProxyHandler(backend, handler, appLogger, transport)

	http.HandleFunc("/", proxy)

	appLogger.Info("Started Nomad Admission Control Proxy", "bind", c.Bind, "port", c.Port)
	appLogger.Error("NACP stopped", "error", http.ListenAndServe(fmt.Sprintf("%s:%d", c.Bind, c.Port), nil))
}

func createMutatators(c *config.Config, appLogger hclog.Logger) ([]admissionctrl.JobMutator, error) {
	var jobMutators []admissionctrl.JobMutator
	for _, m := range c.Mutators {
		switch m.Type {

		case "opa_jsonpatch":

			opaRules := []opa.OpaQueryAndModule{}
			for _, r := range m.OpaRules {
				opaRules = append(opaRules, opa.OpaQueryAndModule{
					Filename: r.Filename,
					Query:    r.Query,
				})
			}
			mutator, err := mutator.NewOpaJsonPatchMutator(opaRules, appLogger.Named("opa_mutator"))
			if err != nil {
				return nil, err
			}
			jobMutators = append(jobMutators, mutator)

		}

	}
	return jobMutators, nil
}
func createValidators(c *config.Config, appLogger hclog.Logger) ([]admissionctrl.JobValidator, error) {
	var jobValidators []admissionctrl.JobValidator
	for _, v := range c.Validators {
		switch v.Type {
		case "opa":

			opaRules := []opa.OpaQueryAndModule{}
			for _, r := range v.OpaRules {
				opaRules = append(opaRules, opa.OpaQueryAndModule{
					Filename: r.Filename,
					Query:    r.Query,
				})
			}
			opaValidator, err := validator.NewOpaValidator(opaRules, appLogger.Named("opa_validator"))
			if err != nil {
				return nil, err
			}
			jobValidators = append(jobValidators, opaValidator)

		}
	}
	return jobValidators, nil
}

func buildCustomTransport(config config.NomadServerTLS) (*http.Transport, error) {
	// Create a custom transport to allow for self-signed certs
	// and to allow for a custom timeout

	//load key pair
	cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, err
	}

	// create CA pool
	caCert, err := ioutil.ReadFile(config.CaFile)
	if err != nil {
		return nil, err
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: config.InsecureSkipVerify,

			Certificates: []tls.Certificate{cert},
			RootCAs:      caCertPool,
		},
	}
	return transport, err
}
