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
// HeaderForwardConfig from the JSON-encoded TOOLHIVE_HEADER_FORWARD_<entry>
// env vars the operator emits on the vMCP pod. Original header casing /
// punctuation is preserved by the JSON value, so the runtime reconstructs
// "X-MCP-Toolsets" rather than "X_MCP_TOOLSETS".
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
			env:                []string{`TOOLHIVE_HEADER_FORWARD_DEMO={"addPlaintextHeaders":{"X-Trace":"t"}}`},
			staticBackendNames: nil,
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.Nil(t, m)
			},
		},
		{
			name: "plaintext header decodes preserving original casing and hyphens",
			env: []string{
				`TOOLHIVE_HEADER_FORWARD_GITHUB_COPILOT={"addPlaintextHeaders":{"X-MCP-Toolsets":"projects,issues"}}`,
				"PATH=/bin",
			},
			staticBackendNames: []string{"github-copilot"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["github-copilot"]
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
				// secretKeyRef; its presence here just shows that
				// readHeaderForwardFromEnv ignores it (the existing
				// resolveHeaderForward path consumes it at request time).
				"TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_STRIPE=resolved-secret-value",
			},
			staticBackendNames: []string{"stripe"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["stripe"]
				require.NotNil(t, cfg)
				assert.Empty(t, cfg.AddPlaintextHeaders)
				assert.Equal(t,
					map[string]string{"X-Api-Key": "HEADER_FORWARD_X_API_KEY_STRIPE"},
					cfg.AddHeadersFromSecret,
				)
			},
		},
		{
			name: "secret env var alone (no manifest) yields no entry",
			env: []string{
				"TOOLHIVE_SECRET_HEADER_FORWARD_X_TOKEN_DEMO=resolved",
			},
			staticBackendNames: []string{"demo"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				// A secret env var without a matching manifest is dropped.
				// In production the operator emits both, so this can't happen
				// in practice — but the runtime stays defensive.
				assert.Empty(t, m)
			},
		},
		{
			name: "stray env var with unknown backend is ignored",
			env: []string{
				`TOOLHIVE_HEADER_FORWARD_GHOST={"addPlaintextHeaders":{"X-Trace":"x"}}`,
			},
			staticBackendNames: []string{"alpha"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.Empty(t, m)
			},
		},
		{
			name: "malformed manifest is skipped without failing other backends",
			env: []string{
				`TOOLHIVE_HEADER_FORWARD_BROKEN=not-json`,
				`TOOLHIVE_HEADER_FORWARD_DEMO={"addPlaintextHeaders":{"X-Trace":"ok"}}`,
			},
			staticBackendNames: []string{"broken", "demo"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				assert.NotContains(t, m, "broken")
				cfg := m["demo"]
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
			staticBackendNames: []string{"alpha", "beta"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()

				alpha := m["alpha"]
				require.NotNil(t, alpha)
				assert.Equal(t, map[string]string{"X-Trace": "alpha-trace"}, alpha.AddPlaintextHeaders)
				assert.Equal(t,
					map[string]string{"X-Token": "HEADER_FORWARD_X_TOKEN_ALPHA"},
					alpha.AddHeadersFromSecret,
				)

				beta := m["beta"]
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
			staticBackendNames: []string{"demo"},
			validate: func(t *testing.T, m map[string]*vmcp.HeaderForwardConfig) {
				t.Helper()
				cfg := m["demo"]
				require.NotNil(t, cfg)
				assert.Equal(t, map[string]string{"X-Trace": "ok"}, cfg.AddPlaintextHeaders)
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
