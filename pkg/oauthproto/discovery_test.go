// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOIDCDiscoveryDocument_Validate(t *testing.T) {
	t.Parallel()

	validDoc := func() OIDCDiscoveryDocument {
		return OIDCDiscoveryDocument{
			AuthorizationServerMetadata: AuthorizationServerMetadata{
				Issuer:                 "https://example.com",
				AuthorizationEndpoint:  "https://example.com/authorize",
				TokenEndpoint:          "https://example.com/token",
				JWKSURI:                "https://example.com/jwks",
				ResponseTypesSupported: []string{"code"},
			},
		}
	}

	tests := []struct {
		name    string
		modify  func(*OIDCDiscoveryDocument)
		isOIDC  bool
		wantErr error
	}{
		{"valid OAuth document", nil, false, nil},
		{"valid OIDC document", nil, true, nil},
		{"missing issuer", func(d *OIDCDiscoveryDocument) { d.Issuer = "" }, false, ErrMissingIssuer},
		{"missing authorization_endpoint", func(d *OIDCDiscoveryDocument) { d.AuthorizationEndpoint = "" }, false, ErrMissingAuthorizationEndpoint},
		{"missing token_endpoint", func(d *OIDCDiscoveryDocument) { d.TokenEndpoint = "" }, false, ErrMissingTokenEndpoint},
		{"missing jwks_uri for OIDC", func(d *OIDCDiscoveryDocument) { d.JWKSURI = "" }, true, ErrMissingJWKSURI},
		{"missing jwks_uri for OAuth is OK", func(d *OIDCDiscoveryDocument) { d.JWKSURI = "" }, false, nil},
		{"missing response_types_supported for OIDC", func(d *OIDCDiscoveryDocument) { d.ResponseTypesSupported = nil }, true, ErrMissingResponseTypesSupported},
		{"missing response_types_supported for OAuth is OK", func(d *OIDCDiscoveryDocument) { d.ResponseTypesSupported = nil }, false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := validDoc()
			if tt.modify != nil {
				tt.modify(&doc)
			}
			err := doc.Validate(tt.isOIDC)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestOIDCDiscoveryDocument_SupportsPKCE(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		methods []string
		want    bool
	}{
		{"nil slice", nil, false},
		{"empty slice", []string{}, false},
		{"only plain", []string{"plain"}, false},
		{"S256 present", []string{"S256"}, true},
		{"both plain and S256", []string{"plain", "S256"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := OIDCDiscoveryDocument{
				AuthorizationServerMetadata: AuthorizationServerMetadata{
					CodeChallengeMethodsSupported: tt.methods,
				},
			}
			if got := doc.SupportsPKCE(); got != tt.want {
				t.Errorf("SupportsPKCE() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOIDCDiscoveryDocument_SupportsGrantType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		grants    []string
		grantType string
		want      bool
	}{
		{"nil slice", nil, GrantTypeAuthorizationCode, false},
		{"empty slice", []string{}, GrantTypeAuthorizationCode, false},
		{"grant type present", []string{GrantTypeAuthorizationCode}, GrantTypeAuthorizationCode, true},
		{"grant type absent", []string{GrantTypeRefreshToken}, GrantTypeAuthorizationCode, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := OIDCDiscoveryDocument{
				AuthorizationServerMetadata: AuthorizationServerMetadata{
					GrantTypesSupported: tt.grants,
				},
			}
			if got := doc.SupportsGrantType(tt.grantType); got != tt.want {
				t.Errorf("SupportsGrantType(%q) = %v, want %v", tt.grantType, got, tt.want)
			}
		})
	}
}

// writeJSONMetadata serves an AuthorizationServerMetadata document as JSON.
// Silently swallows encoding errors: the caller is an httptest server handler,
// which has no way to surface an error back to the test.
func writeJSONMetadata(w http.ResponseWriter, md AuthorizationServerMetadata) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(md)
}

func TestFetchAuthorizationServerMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// handler builds the test HTTP handler given the issuer URL, since the
		// issuer is only known after httptest.NewServer is started.
		handler func(issuer string) http.HandlerFunc
		// tenantPath is appended to the server URL to form the issuer under test.
		// Empty string means the issuer is the server's root URL.
		tenantPath string
	}{
		{
			name: "serves metadata from RFC 8414 path-insertion",
			// Issuer has a tenant path; only the path-insertion URL responds.
			tenantPath: "/tenants/acme",
			handler: func(issuer string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/.well-known/oauth-authorization-server/tenants/acme" {
						http.NotFound(w, r)
						return
					}
					writeJSONMetadata(w, AuthorizationServerMetadata{
						Issuer:               issuer,
						TokenEndpoint:        issuer + "/token",
						RegistrationEndpoint: issuer + "/register",
					})
				}
			},
		},
		{
			name: "serves metadata from OIDC discovery path",
			// Issuer has a tenant path. Path-insertion 404s; OIDC wins.
			tenantPath: "/tenants/acme",
			handler: func(issuer string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/tenants/acme/.well-known/openid-configuration" {
						http.NotFound(w, r)
						return
					}
					writeJSONMetadata(w, AuthorizationServerMetadata{
						Issuer:               issuer,
						TokenEndpoint:        issuer + "/token",
						RegistrationEndpoint: issuer + "/register",
					})
				}
			},
		},
		{
			name: "serves metadata from bare RFC 8414 path",
			// Issuer has a tenant path so attempts 1 and 3 are distinct URLs.
			// Only the bare path responds, proving the fallback reaches
			// iteration 3 after 1 and 2 404.
			tenantPath: "/tenants/acme",
			handler: func(issuer string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/.well-known/oauth-authorization-server" {
						http.NotFound(w, r)
						return
					}
					writeJSONMetadata(w, AuthorizationServerMetadata{
						Issuer:               issuer,
						TokenEndpoint:        issuer + "/token",
						RegistrationEndpoint: issuer + "/register",
					})
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a placeholder server so we can derive an issuer URL; swap
			// the real handler in after the URL is known.
			server := httptest.NewServer(http.NotFoundHandler())
			t.Cleanup(server.Close)

			issuer := server.URL + tt.tenantPath
			server.Config.Handler = tt.handler(issuer)

			metadata, err := FetchAuthorizationServerMetadata(context.Background(), issuer, server.Client())
			require.NoError(t, err)
			require.NotNil(t, metadata)
			assert.Equal(t, issuer, metadata.Issuer)
			assert.Equal(t, issuer+"/token", metadata.TokenEndpoint)
			assert.Equal(t, issuer+"/register", metadata.RegistrationEndpoint)
		})
	}
}

func TestFetchAuthorizationServerMetadata_InvalidIssuer(t *testing.T) {
	t.Parallel()

	// Exercise the input-validation branches of buildDiscoveryURLs via the
	// public entrypoint: a nil client means no HTTP server is needed, since
	// these inputs must be rejected before any request is made.
	tests := []struct {
		name   string
		issuer string
		errSub string
	}{
		{name: "empty issuer", issuer: "", errSub: "issuer is required"},
		{name: "malformed URL", issuer: "://not a url", errSub: "invalid issuer URL"},
		{name: "missing scheme and host", issuer: "example.com", errSub: "scheme and host are required"},
		{name: "http non-loopback host", issuer: "http://example.com", errSub: "issuer must use https"},
		{name: "ftp scheme", issuer: "ftp://example.com", errSub: "issuer must use https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metadata, err := FetchAuthorizationServerMetadata(context.Background(), tt.issuer, nil)
			require.Error(t, err)
			assert.Nil(t, metadata)
			assert.Contains(t, err.Error(), tt.errSub)
		})
	}
}

func TestFetchAuthorizationServerMetadata_AllowsLoopbackHTTP(t *testing.T) {
	t.Parallel()

	// httptest.NewServer binds to 127.0.0.1 over http, which must be accepted
	// so local development and tests can run without a TLS certificate.
	// Start with a placeholder handler so we can capture the server URL before
	// the real handler closes over it.
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	issuer := server.URL
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONMetadata(w, AuthorizationServerMetadata{
			Issuer:               issuer,
			TokenEndpoint:        issuer + "/token",
			RegistrationEndpoint: issuer + "/register",
		})
	})

	metadata, err := FetchAuthorizationServerMetadata(context.Background(), issuer, server.Client())
	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Equal(t, issuer, metadata.Issuer)
}

// TestFetchAuthorizationServerMetadata_TimeoutOverridesCallerContext cannot
// run with t.Parallel() because it mutates the package-level discoveryTimeout
// var; concurrent parallel tests would race when reading it.
//
//nolint:paralleltest // see comment above
func TestFetchAuthorizationServerMetadata_TimeoutOverridesCallerContext(t *testing.T) {
	// Verifies the documented contract that the function applies a bounded
	// per-call timeout via context.WithTimeout on top of the caller's context,
	// so a caller passing context.Background does not hang forever on an
	// unresponsive server. We shorten discoveryTimeout so the test finishes
	// in well under a second rather than waiting the production 10 s.
	originalTimeout := discoveryTimeout
	discoveryTimeout = 100 * time.Millisecond
	t.Cleanup(func() { discoveryTimeout = originalTimeout })

	// Handler blocks until the request context is cancelled, so every URL
	// the function tries will exceed the internal timeout.
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	// Caller passes context.Background (no deadline). If the internal
	// timeout were not applied, this call would hang indefinitely and the
	// test runner would eventually time out with an unclear message.
	done := make(chan struct{})
	var fetchErr error
	go func() {
		_, fetchErr = FetchAuthorizationServerMetadata(context.Background(), server.URL, server.Client())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FetchAuthorizationServerMetadata did not honor bounded internal timeout")
	}

	require.Error(t, fetchErr)
	assert.Contains(t, fetchErr.Error(), "failed to discover authorization server metadata")
}

func TestFetchAuthorizationServerMetadata_IssuerMismatch(t *testing.T) {
	t.Parallel()

	// Server returns metadata whose issuer disagrees with the caller's expected
	// issuer. Every well-known URL the function tries returns the same bad
	// document, so all three attempts fail and the aggregated error surfaces
	// the RFC 8414 §3.3 mismatch.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AuthorizationServerMetadata{
			Issuer:               "https://evil.example.com",
			TokenEndpoint:        "https://evil.example.com/token",
			RegistrationEndpoint: "https://evil.example.com/register",
		})
	}))
	t.Cleanup(server.Close)

	metadata, err := FetchAuthorizationServerMetadata(context.Background(), server.URL, server.Client())

	require.Error(t, err)
	require.Nil(t, metadata)
	assert.Contains(t, err.Error(), "issuer mismatch")
	assert.Contains(t, err.Error(), "RFC 8414")
}

func TestFetchAuthorizationServerMetadata_MissingRegistrationEndpoint(t *testing.T) {
	t.Parallel()

	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the first attempted URL (path-insertion) serves a document;
		// the others 404, so the first one is the winner.
		if !strings.HasPrefix(r.URL.Path, "/.well-known/oauth-authorization-server") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AuthorizationServerMetadata{
			Issuer:        issuer,
			TokenEndpoint: issuer + "/token",
			// RegistrationEndpoint intentionally omitted.
		})
	}))
	t.Cleanup(server.Close)

	issuer = server.URL

	metadata, err := FetchAuthorizationServerMetadata(context.Background(), issuer, server.Client())

	require.ErrorIs(t, err, ErrRegistrationEndpointMissing)
	// Documented return contract: when the winning document lacks
	// registration_endpoint, the function returns (non-nil metadata,
	// ErrRegistrationEndpointMissing) so callers can still reuse the other
	// fields via errors.Is. Assert the full partial document, not just
	// non-nil, so future regressions that drop the metadata (or that stop
	// populating a specific field) are caught.
	require.NotNil(t, metadata)
	assert.Equal(t, issuer, metadata.Issuer)
	assert.Equal(t, issuer+"/token", metadata.TokenEndpoint)
	assert.Empty(t, metadata.RegistrationEndpoint)
}
