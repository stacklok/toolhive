// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestReadHeaderForwardFromEnv covers reconstructing the per-backend
// HeaderForwardConfig from the JSON-encoded TOOLHIVE_HEADER_FORWARD_<entry>
// env vars the operator emits on the vMCP pod. Original header casing /
// punctuation is preserved by the JSON value, so the runtime reconstructs
// "X-MCP-Toolsets" rather than "X_MCP_TOOLSETS".
//
// The map is keyed by the normalized entry segment from the env-var
// suffix; the discoverer normalizes Backend.Name through
// ctrlutil.NormalizeHeaderForEnvVar before indexing.
func TestReadHeaderForwardFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      []string
		validate func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig)
	}{
		{
			name: "no env vars yields empty map",
			env:  []string{"HOME=/root", "PATH=/bin"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.Empty(t, m)
			},
		},
		{
			name: "plaintext header decodes preserving original casing and hyphens",
			env: []string{
				`TOOLHIVE_HEADER_FORWARD_GITHUB_COPILOT={"addPlaintextHeaders":{"X-MCP-Toolsets":"projects,issues"}}`,
				"PATH=/bin",
			},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["GITHUB_COPILOT"]
				require.NotNil(t, cfg)
				assert.Equal(t, map[string]string{"X-MCP-Toolsets": "projects,issues"}, cfg.AddPlaintextHeaders)
				assert.Empty(t, cfg.AddHeadersFromSecret)
			},
		},
		{
			name: "secret header decodes to identifier (value not in manifest)",
			env: []string{
				`TOOLHIVE_HEADER_FORWARD_STRIPE={"addHeadersFromSecret":{"X-Api-Key":"HEADER_FORWARD_X_API_KEY_STRIPE"}}`,
				// The secret-value env var carries the resolved value via
				// secretKeyRef; the walker must NOT treat it as a manifest.
				"TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_STRIPE=resolved-secret-value",
			},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.NotContains(t, m, "X_API_KEY_STRIPE", "secret env var must not be parsed as a manifest")
				cfg := m["STRIPE"]
				require.NotNil(t, cfg)
				assert.Empty(t, cfg.AddPlaintextHeaders)
				assert.Equal(t,
					map[string]string{"X-Api-Key": "HEADER_FORWARD_X_API_KEY_STRIPE"},
					cfg.AddHeadersFromSecret,
				)
			},
		},
		{
			name: "malformed manifest is skipped without failing other backends",
			env: []string{
				`TOOLHIVE_HEADER_FORWARD_BROKEN=not-json`,
				`TOOLHIVE_HEADER_FORWARD_DEMO={"addPlaintextHeaders":{"X-Trace":"ok"}}`,
			},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.NotContains(t, m, "BROKEN")
				cfg := m["DEMO"]
				require.NotNil(t, cfg)
				assert.Equal(t, map[string]string{"X-Trace": "ok"}, cfg.AddPlaintextHeaders)
			},
		},
		{
			name: "mixed plaintext + secret in one manifest scoped per backend",
			env: []string{
				`TOOLHIVE_HEADER_FORWARD_ALPHA={"addPlaintextHeaders":{"X-Trace":"alpha-trace"},"addHeadersFromSecret":{"X-Token":"HEADER_FORWARD_X_TOKEN_ALPHA"}}`,
				`TOOLHIVE_HEADER_FORWARD_BETA={"addPlaintextHeaders":{"X-Trace":"beta-trace"}}`,
			},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()

				alpha := m["ALPHA"]
				require.NotNil(t, alpha)
				assert.Equal(t, map[string]string{"X-Trace": "alpha-trace"}, alpha.AddPlaintextHeaders)
				assert.Equal(t,
					map[string]string{"X-Token": "HEADER_FORWARD_X_TOKEN_ALPHA"},
					alpha.AddHeadersFromSecret,
				)

				beta := m["BETA"]
				require.NotNil(t, beta)
				assert.Equal(t, map[string]string{"X-Trace": "beta-trace"}, beta.AddPlaintextHeaders)
				assert.Empty(t, beta.AddHeadersFromSecret)
			},
		},
		{
			name: "malformed env entry without '=' is skipped",
			env: []string{
				"NO_EQUALS_HERE",
				`TOOLHIVE_HEADER_FORWARD_DEMO={"addPlaintextHeaders":{"X-Trace":"ok"}}`,
			},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["DEMO"]
				require.NotNil(t, cfg)
				assert.Equal(t, map[string]string{"X-Trace": "ok"}, cfg.AddPlaintextHeaders)
			},
		},
		{
			// DNS-1123 forbids underscores in MCPServerEntry names today, so
			// real entries with the same NormalizeForEnvVar output cannot coexist.
			// We pass two manifests that share the same suffix directly to verify
			// the runtime keeps "last wins" behavior and surfaces a warning rather
			// than silently dropping the first.
			name: "duplicate manifest suffix retains last value",
			env: []string{
				`TOOLHIVE_HEADER_FORWARD_DUP={"addPlaintextHeaders":{"X-Trace":"first"}}`,
				`TOOLHIVE_HEADER_FORWARD_DUP={"addPlaintextHeaders":{"X-Trace":"second"}}`,
			},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["DUP"]
				require.NotNil(t, cfg)
				assert.Equal(t, map[string]string{"X-Trace": "second"}, cfg.AddPlaintextHeaders,
					"duplicate manifest must keep the later value (last-write-wins)")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.validate(t, readHeaderForwardFromEnv(tt.env))
		})
	}
}
