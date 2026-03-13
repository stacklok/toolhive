// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTokenSource is a test double for the TokenSource interface.
type mockTokenSource struct {
	token string
	err   error
}

func (m *mockTokenSource) Token(_ context.Context) (string, error) {
	return m.token, m.err
}

func TestWrapTransport(t *testing.T) {
	t.Parallel()

	base := http.DefaultTransport

	tests := []struct {
		name           string
		source         TokenSource
		wantSameAsBase bool
	}{
		{
			name:           "nil source returns base transport unchanged",
			source:         nil,
			wantSameAsBase: true,
		},
		{
			name:           "non-nil source returns wrapped transport",
			source:         &mockTokenSource{token: "tok"},
			wantSameAsBase: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := WrapTransport(base, tt.source)

			if tt.wantSameAsBase {
				require.Equal(t, base, got, "expected base transport to be returned unchanged")
			} else {
				require.NotEqual(t, base, got, "expected a wrapped transport to be returned")
				wrapped, ok := got.(*Transport)
				require.True(t, ok, "wrapped transport should be *Transport")
				require.Equal(t, base, wrapped.Base)
				require.Equal(t, tt.source, wrapped.Source)
			}
		})
	}
}

func TestTransport_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		source          TokenSource
		wantAuthHeader  string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:           "nil source passes through without auth header",
			source:         nil,
			wantAuthHeader: "",
			wantErr:        false,
		},
		{
			name:           "source returns token adds Bearer header",
			source:         &mockTokenSource{token: "my-access-token"},
			wantAuthHeader: "Bearer my-access-token",
			wantErr:        false,
		},
		{
			name:           "source returns empty string passes through without auth header",
			source:         &mockTokenSource{token: ""},
			wantAuthHeader: "",
			wantErr:        false,
		},
		{
			name:            "source returns error propagates error",
			source:          &mockTokenSource{err: errors.New("token fetch failed")},
			wantErr:         true,
			wantErrContains: "failed to get auth token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Record the Authorization header received by the server.
			var receivedAuthHeader string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedAuthHeader = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			transport := &Transport{
				Base:   srv.Client().Transport,
				Source: tt.source,
			}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					require.ErrorContains(t, err, tt.wantErrContains)
				}
				require.Nil(t, resp)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			defer resp.Body.Close()

			assert.Equal(t, tt.wantAuthHeader, receivedAuthHeader)
		})
	}
}

func TestTransport_RoundTrip_DoesNotMutateOriginalRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &Transport{
		Base:   srv.Client().Transport,
		Source: &mockTokenSource{token: "secret-token"},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	// Capture the original header state before the round-trip.
	originalAuth := req.Header.Get("Authorization")

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// The original request must not have been mutated.
	assert.Equal(t, originalAuth, req.Header.Get("Authorization"),
		"RoundTrip must not modify the original request's headers")
}

func TestTransport_base_DefaultsToHTTPDefaultTransport(t *testing.T) {
	t.Parallel()

	tr := &Transport{}
	require.Equal(t, http.DefaultTransport, tr.base(),
		"base() should return http.DefaultTransport when Base is nil")

	custom := &http.Transport{}
	tr.Base = custom
	require.Equal(t, custom, tr.base(),
		"base() should return the configured Base transport when set")
}
