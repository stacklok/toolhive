package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverEndpoints(t *testing.T) {
	t.Parallel()

	t.Run("successful discovery", func(t *testing.T) {
		t.Parallel()
		var testServer *httptest.Server
		testServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, ".well-known")
			assert.Equal(t, UserAgent, r.Header.Get("User-Agent"))

			doc := DiscoveryDocument{
				Issuer:                testServer.URL,
				AuthorizationEndpoint: testServer.URL + "/auth",
				TokenEndpoint:         testServer.URL + "/token",
				JWKSURI:               testServer.URL + "/jwks",
				UserinfoEndpoint:      testServer.URL + "/userinfo",
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc)
		}))
		defer testServer.Close()

		ctx := context.Background()
		doc, err := discoverEndpointsWithClient(ctx, testServer.URL, testServer.Client(), true)
		require.NoError(t, err)
		assert.Equal(t, testServer.URL, doc.Issuer)
		assert.Equal(t, testServer.URL+"/auth", doc.AuthorizationEndpoint)
	})

	t.Run("invalid issuer URL", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		_, err := DiscoverEndpoints(ctx, "not-a-url")
		require.Error(t, err)
		// The error message changed but still validates the URL is rejected
		assert.Contains(t, err.Error(), "HTTPS")
	})

	t.Run("non-HTTPS issuer rejected", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		_, err := DiscoverEndpoints(ctx, "http://example.com")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must use HTTPS")
	})

	t.Run("localhost HTTP allowed", func(t *testing.T) {
		t.Parallel()
		var testServer *httptest.Server
		testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			issuerURL := strings.Replace(testServer.URL, "127.0.0.1", "localhost", 1)
			doc := DiscoveryDocument{
				Issuer:                issuerURL,
				AuthorizationEndpoint: issuerURL + "/auth",
				TokenEndpoint:         issuerURL + "/token",
				JWKSURI:               issuerURL + "/jwks",
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc)
		}))
		defer testServer.Close()

		ctx := context.Background()
		localhostURL := strings.Replace(testServer.URL, "127.0.0.1", "localhost", 1)
		doc, err := DiscoverEndpoints(ctx, localhostURL)
		require.NoError(t, err)
		assert.Contains(t, doc.Issuer, "localhost")
	})
}

func TestDiscoverActualIssuer(t *testing.T) {
	t.Parallel()

	t.Run("different issuer than metadata URL", func(t *testing.T) {
		t.Parallel()
		var testServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {

			doc := DiscoveryDocument{
				Issuer:                "https://actual-issuer.example.com",
				AuthorizationEndpoint: "https://actual-issuer.example.com/auth",
				TokenEndpoint:         "https://actual-issuer.example.com/token",
				JWKSURI:               "https://actual-issuer.example.com/jwks",
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc)
		}))
		defer testServer.Close()

		ctx := context.Background()
		doc, err := discoverEndpointsWithClient(ctx, testServer.URL, testServer.Client(), false)
		require.NoError(t, err)
		assert.Equal(t, "https://actual-issuer.example.com", doc.Issuer)
		assert.NotEqual(t, testServer.URL, doc.Issuer)
	})
}

func TestValidateDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		doc         *DiscoveryDocument
		issuer      string
		oidc        bool
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid OIDC document",
			doc: &DiscoveryDocument{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/auth",
				TokenEndpoint:         "https://example.com/token",
				JWKSURI:               "https://example.com/jwks",
			},
			issuer:      "https://example.com",
			oidc:        true,
			expectError: false,
		},
		{
			name: "missing issuer",
			doc: &DiscoveryDocument{
				AuthorizationEndpoint: "https://example.com/auth",
				TokenEndpoint:         "https://example.com/token",
			},
			issuer:      "https://example.com",
			expectError: true,
			errorMsg:    "missing issuer",
		},
		{
			name: "issuer mismatch",
			doc: &DiscoveryDocument{
				Issuer:                "https://wrong.com",
				AuthorizationEndpoint: "https://example.com/auth",
				TokenEndpoint:         "https://example.com/token",
			},
			issuer:      "https://example.com",
			expectError: true,
			errorMsg:    "issuer mismatch",
		},
		{
			name: "missing JWKS URI for OIDC",
			doc: &DiscoveryDocument{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/auth",
				TokenEndpoint:         "https://example.com/token",
			},
			issuer:      "https://example.com",
			oidc:        true,
			expectError: true,
			errorMsg:    "missing jwks_uri",
		},
		{
			name: "JWKS URI optional for OAuth",
			doc: &DiscoveryDocument{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/auth",
				TokenEndpoint:         "https://example.com/token",
			},
			issuer:      "https://example.com",
			oidc:        false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateDocument(tt.doc, tt.issuer, tt.oidc)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
