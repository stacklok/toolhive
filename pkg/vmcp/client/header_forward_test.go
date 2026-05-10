// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport/middleware"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// mockProvider is an in-memory stand-in for secrets.Provider so tests don't rely
// on process env. It only implements GetSecret; other methods return errors.
//
// errors maps an identifier to a per-key error to inject — used to verify that
// resolveHeaderForward propagates non-NotFound errors (transient backend
// failures, permission errors, etc.) without translating them to NotFound.
type mockProvider struct {
	values map[string]string
	errors map[string]error
}

func (m *mockProvider) GetSecret(_ context.Context, name string) (string, error) {
	if m.errors != nil {
		if err, ok := m.errors[name]; ok {
			return "", err
		}
	}
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

	hdr, err := resolveHeaderForward(t.Context(), cfg, provider, "backend-a")
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

	hdr, err := resolveHeaderForward(t.Context(), cfg, provider, "backend-a")
	require.Error(t, err)
	assert.Nil(t, hdr)
	// Error must not leak the identifier or the backend's private values.
	assert.NotContains(t, err.Error(), "HEADER_FORWARD_X_API_KEY_BACKEND_A")
}

// TestResolveHeaderForward_RestrictedHeaderRejected iterates the full
// middleware.RestrictedHeaders set and verifies every entry is rejected via
// both the plaintext and secret-backed paths. Hardcoding a subset would let a
// future addition to RestrictedHeaders silently fall out of coverage.
func TestResolveHeaderForward_RestrictedHeaderRejected(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, middleware.RestrictedHeaders, "guard against empty map")

	for header := range middleware.RestrictedHeaders {
		t.Run("plaintext_"+header, func(t *testing.T) {
			t.Parallel()
			cfg := &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{header: "x"},
			}
			_, err := resolveHeaderForward(t.Context(), cfg, &mockProvider{}, "backend-x")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "restricted")
		})

		t.Run("secret_"+header, func(t *testing.T) {
			t.Parallel()
			cfg := &vmcp.HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{header: "HEADER_FORWARD_RESTRICTED"},
			}
			_, err := resolveHeaderForward(t.Context(), cfg, &mockProvider{}, "backend-x")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "restricted")
		})
	}
}

// TestResolveHeaderForward_NonNotFoundProviderError verifies that a transient
// secret-provider failure (e.g. permission denied, timeout) is propagated with
// its original chain intact — distinct from the user-facing "not found"
// message produced for ErrSecretNotFound.
func TestResolveHeaderForward_NonNotFoundProviderError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("permission denied calling secrets backend")
	cfg := &vmcp.HeaderForwardConfig{
		AddHeadersFromSecret: map[string]string{
			"X-API-Key": "HEADER_FORWARD_X_API_KEY_BACKEND_A",
		},
	}
	provider := &mockProvider{
		errors: map[string]error{
			"HEADER_FORWARD_X_API_KEY_BACKEND_A": wantErr,
		},
	}

	hdr, err := resolveHeaderForward(t.Context(), cfg, provider, "backend-a")
	require.Error(t, err)
	assert.Nil(t, hdr)
	require.ErrorIs(t, err, wantErr,
		"non-NotFound provider errors must be wrapped with %%w so callers can errors.Is them")
	assert.NotContains(t, err.Error(), "not found",
		"only ErrSecretNotFound should produce the user-facing 'not found' message")
}

func TestResolveHeaderForward_NilCfgReturnsNil(t *testing.T) {
	t.Parallel()
	hdr, err := resolveHeaderForward(t.Context(), nil, nil, "x")
	require.NoError(t, err)
	assert.Nil(t, hdr)
}

func TestBuildHeaderForwardTripper_NilCfgReturnsBase(t *testing.T) {
	t.Parallel()
	base := &captureTripper{}
	got, err := buildHeaderForwardTripper(t.Context(), base, nil, nil, "x")
	require.NoError(t, err)
	assert.Same(t, base, got, "nil cfg must pass base through untouched")
}
