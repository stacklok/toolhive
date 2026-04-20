// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const validConfigYAML = `
name: test-vmcp
groupRef: test-group

incomingAuth:
  type: anonymous

outgoingAuth:
  source: inline
  default:
    type: unauthenticated

aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"
`

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(t *testing.T) ValidateConfig
		wantErr     bool
		errContains string
	}{
		{
			name: "missing config path",
			setup: func(_ *testing.T) ValidateConfig {
				return ValidateConfig{}
			},
			wantErr:     true,
			errContains: "no configuration file specified",
		},
		{
			name: "valid config file",
			setup: func(t *testing.T) ValidateConfig {
				t.Helper()
				path := filepath.Join(t.TempDir(), "vmcp.yaml")
				require.NoError(t, os.WriteFile(path, []byte(validConfigYAML), 0o600))
				return ValidateConfig{ConfigPath: path}
			},
			wantErr: false,
		},
		{
			name: "non-existent file",
			setup: func(t *testing.T) ValidateConfig {
				t.Helper()
				return ValidateConfig{ConfigPath: filepath.Join(t.TempDir(), "nonexistent.yaml")}
			},
			wantErr:     true,
			errContains: "configuration loading failed",
		},
		{
			name: "malformed YAML",
			setup: func(t *testing.T) ValidateConfig {
				t.Helper()
				path := filepath.Join(t.TempDir(), "bad.yaml")
				require.NoError(t, os.WriteFile(path, []byte(":::not valid yaml:::"), 0o600))
				return ValidateConfig{ConfigPath: path}
			},
			wantErr:     true,
			errContains: "configuration loading failed",
		},
		{
			name: "config missing required groupRef",
			setup: func(t *testing.T) ValidateConfig {
				t.Helper()
				path := filepath.Join(t.TempDir(), "invalid.yaml")
				// groupRef is required; omitting it must fail validation.
				require.NoError(t, os.WriteFile(path, []byte(`
name: test-vmcp
incomingAuth:
  type: anonymous
outgoingAuth:
  source: inline
aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"
`), 0o600))
				return ValidateConfig{ConfigPath: path}
			},
			wantErr:     true,
			errContains: "group reference is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.setup(t)
			err := Validate(context.Background(), cfg)
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
