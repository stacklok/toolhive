// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	registry "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
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

// TestCLIEnvVarValidator_PromptsRequiredEnvVars covers the interactive prompt
// path for required, non-secret environment variables declared by the registry
// (for example OKTA_SCOPES or CLICKHOUSE_HOST in the built-in catalog).
//
// The prompt reads process-global os.Stdin, so this test swaps it and cannot
// run in parallel.
//
//nolint:paralleltest // Swaps process-global os.Stdin; cannot run in parallel.
func TestCLIEnvVarValidator_PromptsRequiredEnvVars(t *testing.T) {
	tests := []struct {
		name        string
		envVars     []*registry.EnvVar
		input       string
		wantErr     bool
		wantEnvVars map[string]string
	}{
		{
			name: "value containing spaces is captured in full",
			envVars: []*registry.EnvVar{
				{Name: "OKTA_SCOPES", Required: true, Description: "Space-separated OAuth 2.0 scopes"},
			},
			input:       "okta.users.read okta.groups.read\n",
			wantEnvVars: map[string]string{"OKTA_SCOPES": "okta.users.read okta.groups.read"},
		},
		{
			name: "a value that ends without a newline is still captured",
			envVars: []*registry.EnvVar{
				{Name: "OKTA_SCOPES", Required: true, Description: "Space-separated OAuth 2.0 scopes"},
			},
			input:       "okta.users.read okta.groups.read",
			wantEnvVars: map[string]string{"OKTA_SCOPES": "okta.users.read okta.groups.read"},
		},
		{
			name: "each required variable is prompted on its own line",
			envVars: []*registry.EnvVar{
				{Name: "CLICKHOUSE_HOST", Required: true, Description: "The hostname of your ClickHouse server"},
				{Name: "CLICKHOUSE_USER", Required: true, Description: "The username for authentication"},
			},
			input:       "prod.example.com\nsvc reader\n",
			wantEnvVars: map[string]string{"CLICKHOUSE_HOST": "prod.example.com", "CLICKHOUSE_USER": "svc reader"},
		},
		{
			name: "empty answer for a required variable is an error",
			envVars: []*registry.EnvVar{
				{Name: "GRAFANA_URL", Required: true, Description: "URL of the Grafana instance"},
			},
			input:   "\n",
			wantErr: true,
		},
		{
			name: "unreadable input for a required variable is an error",
			envVars: []*registry.EnvVar{
				{Name: "GRAFANA_URL", Required: true, Description: "URL of the Grafana instance"},
			},
			input:   "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Feed the prompt through a pipe so the test never touches the real stdin,
			// and restore os.Stdin on cleanup.
			origIn := os.Stdin
			pipeR, pipeW, err := os.Pipe()
			require.NoError(t, err)
			_, err = pipeW.WriteString(tc.input)
			require.NoError(t, err)
			require.NoError(t, pipeW.Close())
			t.Cleanup(func() {
				os.Stdin = origIn
				_ = pipeR.Close()
			})
			os.Stdin = pipeR

			validator := NewCLIEnvVarValidator(config.NewPathProvider(filepath.Join(t.TempDir(), "config.yaml")))
			metadata := &registry.ImageMetadata{EnvVars: tc.envVars}
			runConfig := &RunConfig{Secrets: []string{}}

			got, err := validator.Validate(context.Background(), metadata, runConfig, map[string]string{})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantEnvVars, got)
		})
	}
}
