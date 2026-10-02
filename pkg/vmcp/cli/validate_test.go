// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestValidateJSONSummaryIsStableAndSecretFree(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vmcp.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
name: sensitive-vmcp
groupRef: sensitive-group

backends:
  - name: first-backend
    url: http://first.example.test/mcp
    transport: streamable-http
  - name: second-backend
    url: http://second.example.test/sse
    transport: sse

incomingAuth:
  type: anonymous

outgoingAuth:
  source: inline
  default:
    type: unauthenticated
  backends:
    protected-backend:
      type: header_injection
      headerInjection:
        headerName: X-Private-API-Key
        headerValue: vmcp-json-secret-value

aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"

telemetry:
  headers:
    X-Telemetry-Token: vmcp-telemetry-secret-value

passthroughHeaders:
  - X-Tenant-Token
`), 0o600))

	var output bytes.Buffer
	err := Validate(context.Background(), ValidateConfig{
		ConfigPath: path,
		Format:     "json",
		Writer:     &output,
	})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &got))
	assert.Equal(t, map[string]any{
		"valid":                       true,
		"name":                        "sensitive-vmcp",
		"group":                       "sensitive-group",
		"incoming_auth":               "anonymous",
		"outgoing_auth_source":        "inline",
		"backend_auth_override_count": float64(1),
		"backend_count":               float64(2),
		"conflict_resolution":         "prefix",
		"composite_tool_count":        float64(0),
	}, got)

	for _, sensitiveValue := range []string{
		"vmcp-json-secret-value",
		"vmcp-telemetry-secret-value",
		"X-Private-API-Key",
		"X-Telemetry-Token",
		"X-Tenant-Token",
		"first.example.test",
		"second.example.test",
	} {
		assert.NotContains(t, output.String(), sensitiveValue)
	}
}

//nolint:paralleltest // slog.SetDefault is process-wide and must be restored before parallel tests run.
func TestValidateTextOutputRemainsCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vmcp.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validConfigYAML), 0o600))

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	for _, format := range []string{"", "text"} {
		logs.Reset()
		var output bytes.Buffer
		require.NoError(t, Validate(context.Background(), ValidateConfig{
			ConfigPath: path,
			Format:     format,
			Writer:     &output,
		}))

		assert.Empty(t, output.String(), "text output must not be written to the JSON writer")
		for _, summaryLine := range []string{
			"Configuration is valid",
			"Name: test-vmcp",
			"Group: test-group",
			"Incoming Auth: anonymous",
			"Outgoing Auth: default only (source: inline)",
			"Conflict Resolution: prefix",
		} {
			assert.Contains(t, logs.String(), summaryLine)
		}
	}
}
