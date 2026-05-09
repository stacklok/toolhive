// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestReadHeaderForwardFromEnv covers reconstructing the per-backend
// HeaderForwardConfig from env vars the operator emitted on the vMCP pod.
// Plaintext and secret-backed entries are both decoded; secret entries
// carry only the identifier (the value is resolved later via
// secrets.EnvironmentProvider at request time).
func TestReadHeaderForwardFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		env                []string
		staticBackendNames []string
		validate           func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig)
	}{
		{
			name:               "no env vars yields empty map",
			env:                []string{"HOME=/root", "PATH=/bin"},
			staticBackendNames: []string{"github-copilot"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.Empty(t, m)
			},
		},
		{
			name:               "no static backends yields nil",
			env:                []string{"TOOLHIVE_HEADER_PLAINTEXT_X_TRACE_DEMO=t"},
			staticBackendNames: nil,
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.Nil(t, m)
			},
		},
		{
			name: "plaintext header decodes",
			env: []string{
				"TOOLHIVE_HEADER_PLAINTEXT_X_MCP_TOOLSETS_GITHUB_COPILOT=projects,issues",
				"PATH=/bin",
			},
			staticBackendNames: []string{"github-copilot"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["github-copilot"]
				require.NotNil(t, cfg)
				assert.Equal(t, map[string]string{"X_MCP_TOOLSETS": "projects,issues"}, cfg.AddPlaintextHeaders)
				assert.Empty(t, cfg.AddHeadersFromSecret)
			},
		},
		{
			name: "secret header decodes to identifier (value not captured)",
			env: []string{
				"TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_STRIPE=resolved-secret-value",
			},
			staticBackendNames: []string{"stripe"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["stripe"]
				require.NotNil(t, cfg)
				assert.Empty(t, cfg.AddPlaintextHeaders)
				assert.Equal(t,
					map[string]string{"X_API_KEY": "HEADER_FORWARD_X_API_KEY_STRIPE"},
					cfg.AddHeadersFromSecret,
				)
			},
		},
		{
			name: "stray env var with unknown backend is ignored",
			env: []string{
				"TOOLHIVE_HEADER_PLAINTEXT_X_TRACE_GHOST=should-be-dropped",
			},
			staticBackendNames: []string{"alpha"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.Empty(t, m)
			},
		},
		{
			name: "header name with multiple underscores splits at the right boundary",
			env: []string{
				"TOOLHIVE_HEADER_PLAINTEXT_X_GITHUB_COPILOT_TOKEN_GITHUB_COPILOT=t",
			},
			staticBackendNames: []string{"github-copilot"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["github-copilot"]
				require.NotNil(t, cfg)
				// Header reconstructs as everything before the trailing
				// "_GITHUB_COPILOT" (the normalized backend name).
				assert.Equal(t,
					map[string]string{"X_GITHUB_COPILOT_TOKEN": "t"},
					cfg.AddPlaintextHeaders,
				)
			},
		},
		{
			name: "mixed plaintext + secret across multiple backends scoped per backend",
			env: []string{
				"TOOLHIVE_HEADER_PLAINTEXT_X_TRACE_ALPHA=alpha-trace",
				"TOOLHIVE_SECRET_HEADER_FORWARD_X_TOKEN_ALPHA=alpha-secret-value",
				"TOOLHIVE_HEADER_PLAINTEXT_X_TRACE_BETA=beta-trace",
			},
			staticBackendNames: []string{"alpha", "beta"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()

				alpha := m["alpha"]
				require.NotNil(t, alpha)
				assert.Equal(t, map[string]string{"X_TRACE": "alpha-trace"}, alpha.AddPlaintextHeaders)
				assert.Equal(t,
					map[string]string{"X_TOKEN": "HEADER_FORWARD_X_TOKEN_ALPHA"},
					alpha.AddHeadersFromSecret,
				)

				beta := m["beta"]
				require.NotNil(t, beta)
				assert.Equal(t, map[string]string{"X_TRACE": "beta-trace"}, beta.AddPlaintextHeaders)
				assert.Empty(t, beta.AddHeadersFromSecret)
			},
		},
		{
			name: "malformed env entry without '=' is skipped",
			env: []string{
				"NO_EQUALS_HERE",
				"TOOLHIVE_HEADER_PLAINTEXT_X_TRACE_DEMO=ok",
			},
			staticBackendNames: []string{"demo"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["demo"]
				require.NotNil(t, cfg)
				assert.Equal(t, map[string]string{"X_TRACE": "ok"}, cfg.AddPlaintextHeaders)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := readHeaderForwardFromEnv(tt.env, tt.staticBackendNames)
			tt.validate(t, result)
		})
	}
}
