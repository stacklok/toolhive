// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	registry "github.com/stacklok/toolhive-core/registry/types"
)

func TestDetachedEnvVarValidator_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		envVars         []*registry.EnvVar
		suppliedEnvVars map[string]string
		wantErr         bool
		wantEnvVars     map[string]string
	}{
		{
			name: "optional secret not provided is silently skipped",
			envVars: []*registry.EnvVar{
				{Name: "OCI_TOKEN", Required: false, Secret: true},
			},
			suppliedEnvVars: map[string]string{},
			wantErr:         false,
			wantEnvVars:     map[string]string{},
		},
		{
			name: "required non-secret not provided returns error",
			envVars: []*registry.EnvVar{
				{Name: "REQUIRED_VAR", Required: true, Secret: false},
			},
			suppliedEnvVars: map[string]string{},
			wantErr:         true,
		},
		{
			name: "required secret not provided returns error",
			envVars: []*registry.EnvVar{
				{Name: "REQUIRED_SECRET", Required: true, Secret: true},
			},
			suppliedEnvVars: map[string]string{},
			wantErr:         true,
		},
		{
			name: "optional secret with default applies default",
			envVars: []*registry.EnvVar{
				{Name: "OPT_TOKEN", Required: false, Secret: true, Default: "default-val"},
			},
			suppliedEnvVars: map[string]string{},
			wantErr:         false,
			wantEnvVars:     map[string]string{"OPT_TOKEN": "default-val"},
		},
		{
			name: "provided secret passes through unchanged",
			envVars: []*registry.EnvVar{
				{Name: "OCI_TOKEN", Required: false, Secret: true},
			},
			suppliedEnvVars: map[string]string{"OCI_TOKEN": "my-token"},
			wantErr:         false,
			wantEnvVars:     map[string]string{"OCI_TOKEN": "my-token"},
		},
		{
			name:            "nil metadata skips all checks",
			envVars:         nil,
			suppliedEnvVars: map[string]string{"EXTRA": "val"},
			wantErr:         false,
			wantEnvVars:     map[string]string{"EXTRA": "val"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var metadata *registry.ImageMetadata
			if tc.envVars != nil {
				metadata = &registry.ImageMetadata{EnvVars: tc.envVars}
			}

			runConfig := &RunConfig{Secrets: []string{}}
			validator := &DetachedEnvVarValidator{}

			got, err := validator.Validate(context.Background(), metadata, runConfig, tc.suppliedEnvVars)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.wantEnvVars != nil {
				assert.Equal(t, tc.wantEnvVars, got)
			}
		})
	}
}
