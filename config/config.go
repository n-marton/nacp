package config

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsimple"
)

type Webhook struct {
	Endpoint string `hcl:"endpoint"`
	Method   string `hcl:"method"`
}
type OpaRule struct {
	Query    string `hcl:"query"`
	Filename string `hcl:"filename"`
}

type Validator struct {
	Type    string   `hcl:"type,label"`
	Name    string   `hcl:"name,label"`
	OpaRule *OpaRule `hcl:"opa_rule,block"`
	Webhook *Webhook `hcl:"webhook,block"`
}
type Mutator struct {
	Type    string   `hcl:"type,label"`
	Name    string   `hcl:"name,label"`
	OpaRule *OpaRule `hcl:"opa_rule,block"`
	Webhook *Webhook `hcl:"webhook,block"`
}

type NomadServerTLS struct {
	CaFile             string `hcl:"ca_file"`
	CertFile           string `hcl:"cert_file"`
	KeyFile            string `hcl:"key_file"`
	InsecureSkipVerify bool   `hcl:"insecure_skip_verify,optional"`
}
type NomadServer struct {
	Address string          `hcl:"address"`
	TLS     *NomadServerTLS `hcl:"tls,block"`
}
type ProxyTLS struct {
	CertFile string `hcl:"cert_file"`
	KeyFile  string `hcl:"key_file"`
	CaFile   string `hcl:"ca_file"`
}
type Config struct {
	Port int    `hcl:"port,optional"`
	Bind string `hcl:"bind,optional"`

	LogLevel string    `hcl:"log_level,optional"`
	Tls      *ProxyTLS `hcl:"tls,block"`

	Nomad      *NomadServer `hcl:"nomad,block"`
	Validators []Validator  `hcl:"validator,block"`
	Mutators   []Mutator    `hcl:"mutator,block"`
}

func DefaultConfig() *Config {
	c := &Config{
		Port: 6464,
		Bind: "0.0.0.0",
		Nomad: &NomadServer{
			Address: "http://localhost:4646",
		},
		LogLevel:   "info",
		Validators: []Validator{},
		Mutators:   []Mutator{},
	}
	return c
}
func LoadConfig(name string) (*Config, error) {

	c := DefaultConfig()

	evalContext := &hcl.EvalContext{}
	err := hclsimple.DecodeFile(name, evalContext, c)
	if err != nil {
		return nil, err
	}
	return c, nil
}
