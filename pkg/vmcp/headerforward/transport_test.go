// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package headerforward

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
	got, err := BuildHeaderForwardTripper(t.Context(), base, nil, nil, "x")
	require.NoError(t, err)
	assert.Same(t, base, got, "nil cfg must pass base through untouched")
}

// TestMergeForwardedHeaders exercises the key rules of MergeForwardedHeaders:
// empty forwarded, static-only, forwarded-only, merged output, restricted-name
// drop, and collision detection.
func TestMergeForwardedHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		base      *vmcp.HeaderForwardConfig
		forwarded map[string]string
		wantCfg   *vmcp.HeaderForwardConfig
		wantErr   bool
	}{
		{
			name:      "empty forwarded returns base unchanged",
			base:      &vmcp.HeaderForwardConfig{AddPlaintextHeaders: map[string]string{"X-Static": "v"}},
			forwarded: nil,
			wantCfg:   &vmcp.HeaderForwardConfig{AddPlaintextHeaders: map[string]string{"X-Static": "v"}},
		},
		{
			name:      "nil base and empty forwarded returns nil",
			base:      nil,
			forwarded: nil,
			wantCfg:   nil,
		},
		{
			name:      "nil base with forwarded yields forwarded-only config",
			base:      nil,
			forwarded: map[string]string{"X-Api-Key": "key123"},
			wantCfg: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Api-Key": "key123"},
			},
		},
		{
			name:      "forwarded headers are canonicalized",
			base:      nil,
			forwarded: map[string]string{"x-api-key": "key123"},
			wantCfg: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Api-Key": "key123"},
			},
		},
		{
			name: "static and forwarded are merged",
			base: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Static": "static-value"},
			},
			forwarded: map[string]string{"X-Dynamic": "dynamic-value"},
			wantCfg: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"X-Static":  "static-value",
					"X-Dynamic": "dynamic-value",
				},
			},
		},
		{
			name: "secret headers preserved from base",
			base: &vmcp.HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{"X-Auth": "MY_SECRET"},
			},
			forwarded: map[string]string{"X-Dynamic": "value"},
			wantCfg: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders:  map[string]string{"X-Dynamic": "value"},
				AddHeadersFromSecret: map[string]string{"X-Auth": "MY_SECRET"},
			},
		},
		{
			name:      "restricted header is silently dropped",
			base:      nil,
			forwarded: map[string]string{"X-Forwarded-For": "1.2.3.4", "X-Api-Key": "k"},
			wantCfg: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Api-Key": "k"},
			},
		},
		{
			name: "collision with plaintext static returns error",
			base: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Api-Key": "static"},
			},
			forwarded: map[string]string{"X-Api-Key": "forwarded"},
			wantErr:   true,
		},
		{
			name: "collision with secret static returns error",
			base: &vmcp.HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{"X-Auth": "SECRET_ID"},
			},
			forwarded: map[string]string{"X-Auth": "value"},
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Snapshot base before the call to catch any mutation.
			var origBasePlaintext map[string]string
			if tc.base != nil && tc.base.AddPlaintextHeaders != nil {
				origBasePlaintext = make(map[string]string, len(tc.base.AddPlaintextHeaders))
				for k, v := range tc.base.AddPlaintextHeaders {
					origBasePlaintext[k] = v
				}
			}

			got, err := MergeForwardedHeaders(tc.base, tc.forwarded)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Verify base was not mutated regardless of outcome.
			if tc.base != nil {
				assert.Equal(t, origBasePlaintext, tc.base.AddPlaintextHeaders,
					"base.AddPlaintextHeaders must not be mutated by MergeForwardedHeaders")
			}

			if tc.wantCfg == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.wantCfg.AddPlaintextHeaders, got.AddPlaintextHeaders)
			assert.Equal(t, tc.wantCfg.AddHeadersFromSecret, got.AddHeadersFromSecret)
		})
	}
}

// TestMergeForwardedHeaders_EmptyForwardedReturnsSamePointer verifies that when
// forwarded is nil/empty, the exact same base pointer is returned (no allocation).
func TestMergeForwardedHeaders_EmptyForwardedReturnsSamePointer(t *testing.T) {
	t.Parallel()
	base := &vmcp.HeaderForwardConfig{AddPlaintextHeaders: map[string]string{"X-A": "v"}}
	got, err := MergeForwardedHeaders(base, nil)
	require.NoError(t, err)
	assert.Same(t, base, got, "empty forwarded must return the same base pointer")

	got2, err := MergeForwardedHeaders(base, map[string]string{})
	require.NoError(t, err)
	assert.Same(t, base, got2, "empty forwarded map must return the same base pointer")
}

// TestMergeForwardedHeaders_RestrictedHeadersList verifies that every header in
// middleware.RestrictedHeaders is dropped when present in forwarded.
func TestMergeForwardedHeaders_RestrictedHeadersList(t *testing.T) {
	t.Parallel()
	for name := range middleware.RestrictedHeaders {
		forwarded := map[string]string{name: "inject"}
		got, err := MergeForwardedHeaders(nil, forwarded)
		require.NoError(t, err, "restricted header %q must not return an error", name)
		if got != nil {
			assert.NotContains(t, got.AddPlaintextHeaders, name,
				"restricted header %q must be dropped", name)
		}
	}
}

// closeIdleSpy is a RoundTripper that records CloseIdleConnections calls.
type closeIdleSpy struct{ closed int }

func (*closeIdleSpy) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}
func (s *closeIdleSpy) CloseIdleConnections() { s.closed++ }

// TestHeaderForwardRoundTripper_CloseIdleConnections verifies the wrapper forwards
// CloseIdleConnections to its base rather than swallowing it.
func TestHeaderForwardRoundTripper_CloseIdleConnections(t *testing.T) {
	t.Parallel()

	spy := &closeIdleSpy{}
	rt := &headerForwardRoundTripper{base: spy}
	rt.CloseIdleConnections()
	assert.Equal(t, 1, spy.closed)
}
