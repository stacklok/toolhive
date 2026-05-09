// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// mockProvider is an in-memory stand-in for secrets.Provider so tests don't rely
// on process env. It only implements GetSecret; other methods return errors.
type mockProvider struct {
	values map[string]string
}

func (m *mockProvider) GetSecret(_ context.Context, name string) (string, error) {
	if v, ok := m.values[name]; ok {
		return v, nil
	}
	return "", secrets.ErrSecretNotFound
}

// mockProvider needs to satisfy secrets.Provider; the other methods are unused.
func (*mockProvider) SetSecret(_ context.Context, _, _ string) error { return nil }
func (*mockProvider) DeleteSecret(_ context.Context, _ string) error { return nil }
func (*mockProvider) ListSecrets(_ context.Context) ([]secrets.SecretDescription, error) {
	return nil, nil
}
func (*mockProvider) DeleteSecrets(_ context.Context, _ []string) error { return nil }
func (*mockProvider) Cleanup() error                                    { return nil }
func (*mockProvider) Capabilities() secrets.ProviderCapabilities {
	return secrets.ProviderCapabilities{CanRead: true}
}

// captureTripper records the headers seen on the last request that reached it.
type captureTripper struct {
	lastHeaders http.Header
}

func (c *captureTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.lastHeaders = req.Header.Clone()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}, nil
}

func TestHeaderForwardRoundTripper_InjectsPlaintext(t *testing.T) {
	t.Parallel()

	base := &captureTripper{}
	rt := &headerForwardRoundTripper{
		base: base,
		headers: http.Header{
			"X-Mcp-Toolsets": []string{"projects,issues"},
			"X-Tenant":       []string{"acme"},
		},
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.com", http.NoBody)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, "projects,issues", base.lastHeaders.Get("X-Mcp-Toolsets"))
	assert.Equal(t, "acme", base.lastHeaders.Get("X-Tenant"))
	// Original request must remain unmutated.
	assert.Empty(t, req.Header.Get("X-Mcp-Toolsets"))
}

func TestHeaderForwardRoundTripper_NoHeadersIsNoOp(t *testing.T) {
	t.Parallel()

	base := &captureTripper{}
	rt := &headerForwardRoundTripper{base: base, headers: nil}

	req, err := http.NewRequest(http.MethodGet, "https://example.com", http.NoBody)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)
	assert.Empty(t, base.lastHeaders.Get("X-Mcp-Toolsets"))
}

func TestHeaderForwardRoundTripper_DoesNotClobberExistingHeader(t *testing.T) {
	t.Parallel()

	base := &captureTripper{}
	rt := &headerForwardRoundTripper{
		base: base,
		headers: http.Header{
			"Authorization": []string{"Bearer user-provided"},
		},
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.com", http.NoBody)
	require.NoError(t, err)
	// Pre-set the header as an outer (identity/trace/auth) tripper would.
	req.Header.Set("Authorization", "Bearer real-auth")

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, "Bearer real-auth", base.lastHeaders.Get("Authorization"),
		"user-supplied header-forward value must not clobber an already-set header")
}

func TestResolveHeaderForward_PlaintextAndSecretMerged(t *testing.T) {
	t.Parallel()

	cfg := &vmcp.HeaderForwardConfig{
		AddPlaintextHeaders: map[string]string{
			"X-Tenant": "acme",
		},
		AddHeadersFromSecret: map[string]string{
			"X-API-Key": "HEADER_FORWARD_X_API_KEY_BACKEND_A",
		},
	}
	provider := &mockProvider{values: map[string]string{
		"HEADER_FORWARD_X_API_KEY_BACKEND_A": "resolved-from-env",
	}}

	hdr, err := resolveHeaderForward(cfg, provider, "backend-a")
	require.NoError(t, err)
	assert.Equal(t, "acme", hdr.Get("X-Tenant"))
	assert.Equal(t, "resolved-from-env", hdr.Get("X-Api-Key"))
}

func TestResolveHeaderForward_SecretNotFoundReturnsError(t *testing.T) {
	t.Parallel()

	cfg := &vmcp.HeaderForwardConfig{
		AddHeadersFromSecret: map[string]string{
			"X-API-Key": "HEADER_FORWARD_X_API_KEY_BACKEND_A",
		},
	}
	provider := &mockProvider{values: map[string]string{}}

	hdr, err := resolveHeaderForward(cfg, provider, "backend-a")
	require.Error(t, err)
	assert.Nil(t, hdr)
	// Error must not leak the identifier or the backend's private values.
	assert.NotContains(t, err.Error(), "HEADER_FORWARD_X_API_KEY_BACKEND_A")
}

func TestResolveHeaderForward_RestrictedHeaderRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *vmcp.HeaderForwardConfig
	}{
		{
			name: "plaintext Host rejected",
			cfg: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"Host": "attacker.example"},
			},
		},
		{
			name: "plaintext Authorization rejected via secret not tested here",
			cfg: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"Transfer-Encoding": "chunked"},
			},
		},
		{
			name: "secret-backed X-Forwarded-For rejected",
			cfg: &vmcp.HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{
					"X-Forwarded-For": "HEADER_FORWARD_X_FORWARDED_FOR_X",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := resolveHeaderForward(tt.cfg, &mockProvider{}, "backend-x")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "restricted")
		})
	}
}

func TestResolveHeaderForward_NilCfgReturnsNil(t *testing.T) {
	t.Parallel()
	hdr, err := resolveHeaderForward(nil, nil, "x")
	require.NoError(t, err)
	assert.Nil(t, hdr)
}

func TestBuildHeaderForwardTripper_NilCfgReturnsBase(t *testing.T) {
	t.Parallel()
	base := &captureTripper{}
	got, err := buildHeaderForwardTripper(base, nil, nil, "x")
	require.NoError(t, err)
	assert.Same(t, base, got, "nil cfg must pass base through untouched")
}

// TestHeaderForwardRoundTripper_EndToEndHTTPTestServer exercises the tripper
// against a real httptest.Server to verify the header reaches a live receiver,
// catching any Clone/Set regressions.
func TestHeaderForwardRoundTripper_EndToEndHTTPTestServer(t *testing.T) {
	t.Parallel()

	var receivedToolsets string
	var receivedAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		receivedToolsets = r.Header.Get("X-MCP-Toolsets")
		receivedAPIKey = r.Header.Get("X-API-Key")
	}))
	t.Cleanup(server.Close)

	cfg := &vmcp.HeaderForwardConfig{
		AddPlaintextHeaders: map[string]string{
			"X-MCP-Toolsets": "projects,issues,pull_requests",
		},
		AddHeadersFromSecret: map[string]string{
			"X-API-Key": "HEADER_FORWARD_X_API_KEY_BACKEND_E",
		},
	}
	provider := &mockProvider{values: map[string]string{
		"HEADER_FORWARD_X_API_KEY_BACKEND_E": "secret-token-value",
	}}

	rt, err := buildHeaderForwardTripper(http.DefaultTransport, cfg, provider, "backend-e")
	require.NoError(t, err)
	client := &http.Client{Transport: rt}

	req, err := http.NewRequest(http.MethodGet, server.URL, http.NoBody)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Equal(t, "projects,issues,pull_requests", receivedToolsets)
	assert.Equal(t, "secret-token-value", receivedAPIKey)
}
