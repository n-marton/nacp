package mutator

import (
	"context"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/nomad/api"
	"github.com/mxab/nacp/admissionctrl/opa"
)

type OpaJsonPatchMutator struct {
	query  *opa.OpaQuery
	logger hclog.Logger
	name   string
}

func (j *OpaJsonPatchMutator) Mutate(job *api.Job) (*api.Job, []error, error) {
	allWarnings := make([]error, 0)
	ctx := context.TODO()

	results, err := j.query.Query(ctx, job)
	if err != nil {
		return nil, nil, err
	}

	errors := results.GetErrors()

	if len(errors) > 0 {
		j.logger.Debug("Got errors from rule", "rule", j.Name(), "errors", errors, "job", job.ID)
		allErrors := multierror.Append(nil)
		for _, warn := range errors {
			allErrors = multierror.Append(allErrors, fmt.Errorf("%s (%s)", warn, j.Name()))
		}
		return nil, nil, allErrors
	}

	warnings := results.GetWarnings()

	if len(warnings) > 0 {
		j.logger.Debug("Got warnings from rule", "rule", j.Name(), "warnings", warnings, "job", job.ID)
		for _, warn := range warnings {
			allWarnings = append(allWarnings, fmt.Errorf("%s (%s)", warn, j.Name()))
		}
	}
	patchData := results.GetPatch()
	patchJSON, err := json.Marshal(patchData)
	if err != nil {
		return nil, nil, err
	}

	patch, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return nil, nil, err
	}
	j.logger.Debug("Got patch fom rule", "rule", j.Name(), "patch", string(patchJSON), "job", job.ID)
	jobJson, err := json.Marshal(job)
	if err != nil {
		return nil, nil, err
	}

	patched, err := patch.Apply(jobJson)
	if err != nil {
		return nil, nil, err
	}
	var patchedJob api.Job
	err = json.Unmarshal(patched, &patchedJob)
	if err != nil {
		return nil, nil, err
	}
	job = &patchedJob

	return job, allWarnings, nil
}
func (j *OpaJsonPatchMutator) Name() string {
	return j.name
}

func NewOpaJsonPatchMutator(name, filename, query string, logger hclog.Logger) (*OpaJsonPatchMutator, error) {

	ctx := context.TODO()
	// read the policy file
	preparedQuery, err := opa.CreateQuery(filename, query, ctx)
	if err != nil {
		return nil, err
	}
	return &OpaJsonPatchMutator{
		query:  preparedQuery,
		logger: logger,
		name:   name,
	}, nil

}
