// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spiffeauth

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSPIFFEIDFromCertificate(t *testing.T) {
	t.Parallel()

	validURI := mustParseURI(t, "spiffe://example.org/workload/service")
	tests := []struct {
		name    string
		uris    []*url.URL
		wantID  spiffeid.ID
		wantErr string
	}{
		{
			name:   "ignores non SPIFFE URI SAN",
			uris:   []*url.URL{mustParseURI(t, "https://example.org/workload"), validURI},
			wantID: spiffeid.RequireFromString("spiffe://example.org/workload/service"),
		},
		{
			name:    "rejects no SPIFFE URI SAN",
			uris:    []*url.URL{mustParseURI(t, "https://example.org/workload")},
			wantErr: "required",
		},
		{
			name:    "rejects multiple SPIFFE URI SANs",
			uris:    []*url.URL{validURI, mustParseURI(t, "spiffe://example.org/workload/other")},
			wantErr: "multiple",
		},
		{
			name:    "rejects traversal in SPIFFE URI SAN",
			uris:    []*url.URL{mustParseURI(t, "spiffe://example.org/workload/../other")},
			wantErr: "dot segments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id, err := SPIFFEIDFromCertificate(&x509.Certificate{URIs: tt.uris})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func TestMiddleware(t *testing.T) {
	t.Parallel()

	validCert := &x509.Certificate{URIs: []*url.URL{mustParseURI(t, "spiffe://example.org/workload/service")}}
	invalidCert := &x509.Certificate{URIs: []*url.URL{mustParseURI(t, "https://example.org/workload")}}
	tests := []struct {
		name             string
		path             string
		peerCertificates []*x509.Certificate
		wantStatus       int
		wantID           spiffeid.ID
		wantNextCalls    int
	}{
		{
			name:             "only examines token endpoint",
			path:             "/other",
			peerCertificates: []*x509.Certificate{invalidCert},
			wantStatus:       http.StatusNoContent,
			wantNextCalls:    1,
		},
		{
			name:          "passes request without peer certificate",
			path:          "/oauth/token",
			wantStatus:    http.StatusNoContent,
			wantNextCalls: 1,
		},
		{
			name:             "attaches valid identity",
			path:             "/oauth/token",
			peerCertificates: []*x509.Certificate{validCert},
			wantStatus:       http.StatusNoContent,
			wantID:           spiffeid.RequireFromString("spiffe://example.org/workload/service"),
			wantNextCalls:    1,
		},
		{
			name:             "rejects invalid SAN without calling next",
			path:             "/oauth/token",
			peerCertificates: []*x509.Certificate{invalidCert},
			wantStatus:       http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nextCalls := 0
			var gotID spiffeid.ID
			handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				gotID, _ = SPIFFEIDFromContext(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			if tt.peerCertificates != nil {
				req.TLS = &tls.ConnectionState{PeerCertificates: tt.peerCertificates}
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, req)

			assert.Equal(t, tt.wantStatus, response.Code)
			assert.Equal(t, tt.wantNextCalls, nextCalls)
			assert.Equal(t, tt.wantID, gotID)
		})
	}
}

func mustParseURI(t *testing.T, rawURI string) *url.URL {
	t.Helper()

	uri, err := url.Parse(rawURI)
	require.NoError(t, err)
	return uri
}
