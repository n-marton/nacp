package mutator

import (
	"fmt"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/api"
	"github.com/mxab/nacp/admissionctrl/opa"
	"github.com/mxab/nacp/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONPatcher_Mutate(t *testing.T) {
	type args struct {
		job *api.Job
	}
	tests := []struct {
		name         string
		j            *OpaJsonPatchMutator
		args         args
		wantOut      *api.Job
		wantWarnings []error
		wantErr      bool
	}{
		{
			name: "nothing",
			j:    newMutator(t, []opa.OpaQueryAndModule{}),

			args: args{
				job: &api.Job{},
			},
			wantOut:      &api.Job{},
			wantWarnings: []error{},
			wantErr:      false,
		},
		{
			name: "hello world",
			j: newMutator(t, []opa.OpaQueryAndModule{
				{
					Filename: testutil.Filepath(t, "opa/mutators/hello_world_meta.rego"),
					Query:    "patch = data.hello_world_meta.patch",
				},
			}),

			args: args{
				job: &api.Job{},
			},
			wantOut: &api.Job{
				Meta: map[string]string{
					"hello": "world",
				},
			},
			wantWarnings: []error{},
			wantErr:      false,
		},
		{
			name: "warning and world",
			j: newMutator(t, []opa.OpaQueryAndModule{
				{
					Filename: testutil.Filepath(t, "opa/mutators/hello_world_meta.rego"),
					Query:    `patch = data.hello_world_meta.patch`,
				},
				{
					Filename: testutil.Filepath(t, "opa/errors.rego"),
					Query: `

					warnings = data.dummy.warnings
					`,
				},
			}),

			args: args{
				job: &api.Job{},
			},
			wantOut: &api.Job{
				Meta: map[string]string{
					"hello": "world",
				},
			},
			wantWarnings: []error{fmt.Errorf("This is a warning message (%s)", testutil.Filepath(t, "opa/errors.rego"))},
			wantErr:      false,
		},
		{
			name: "error and world",
			j: newMutator(t, []opa.OpaQueryAndModule{
				{
					Filename: testutil.Filepath(t, "opa/mutators/hello_world_meta.rego"),
					Query:    `patch = data.hello_world_meta.patch`,
				},
				{
					Filename: testutil.Filepath(t, "opa/errors.rego"),
					Query: `

					errors = data.dummy.errors
					`,
				},
			}),

			args: args{
				job: &api.Job{},
			},
			wantOut:      nil,
			wantWarnings: nil,
			wantErr:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOut, gotWarnings, err := tt.j.Mutate(tt.args.job)
			require.Equal(t, tt.wantErr, err != nil, "JSONPatcher.Mutate() error = %v, wantErr %v", err, tt.wantErr)

			assert.Equal(t, tt.wantWarnings, gotWarnings, "JSONPatcher.Mutate() gotWarnings = %v, want %v", gotWarnings, tt.wantWarnings)
			assert.Equal(t, tt.wantOut, gotOut)

		})
	}
}

func newMutator(t *testing.T, rules []opa.OpaQueryAndModule) *OpaJsonPatchMutator {
	t.Helper()
	m, err := NewOpaJsonPatchMutator(rules, hclog.NewNullLogger())
	require.NoError(t, err)
	return m
}
