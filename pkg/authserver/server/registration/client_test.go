// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package registration

import (
	"context"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLoopbackClient(t *testing.T) {
	t.Parallel()

	defaultClient := &fosite.DefaultClient{
		ID:           "test-client",
		RedirectURIs: []string{"http://127.0.0.1/callback"},
		Public:       true,
	}

	client := NewLoopbackClient(&fosite.DefaultOpenIDConnectClient{DefaultClient: defaultClient})

	assert.NotNil(t, client)
	assert.Equal(t, "test-client", client.GetID())
	assert.Equal(t, []string{"http://127.0.0.1/callback"}, client.GetRedirectURIs())
	assert.True(t, client.IsPublic())
}

func TestLoopbackClient_MatchRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		registeredURIs []string
		requestedURI   string
		shouldMatch    bool
	}{
		// Exact matches
		{
			name:           "exact match - https",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://example.com/callback",
			shouldMatch:    true,
		},
		{
			name:           "exact match - http loopback with port",
			registeredURIs: []string{"http://127.0.0.1:8080/callback"},
			requestedURI:   "http://127.0.0.1:8080/callback",
			shouldMatch:    true,
		},

		// RFC 8252 Section 7.3 - IPv4 loopback (127.0.0.1)
		{
			name:           "loopback IPv4 - dynamic port matches",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:57403/callback",
			shouldMatch:    true,
		},
		{
			name:           "loopback IPv4 - different dynamic port matches",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:8080/callback",
			shouldMatch:    true,
		},
		{
			name:           "loopback IPv4 - no port in request matches registered without port",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1/callback",
			shouldMatch:    true,
		},
		{
			name:           "loopback IPv4 - path must match",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:57403/other",
			shouldMatch:    false,
		},
		{
			name:           "loopback IPv4 - query must match",
			registeredURIs: []string{"http://127.0.0.1/callback?foo=bar"},
			requestedURI:   "http://127.0.0.1:57403/callback?foo=bar",
			shouldMatch:    true,
		},
		{
			name:           "loopback IPv4 - query mismatch fails",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:57403/callback?extra=param",
			shouldMatch:    false,
		},

		// RFC 8252 Section 7.3 - localhost
		{
			name:           "loopback localhost - dynamic port matches",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://localhost:57403/callback",
			shouldMatch:    true,
		},
		{
			name:           "loopback localhost - path must match",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://localhost:57403/other",
			shouldMatch:    false,
		},

		// Cross-hostname matching should NOT work (security requirement)
		{
			name:           "localhost and 127.0.0.1 are different",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://localhost:57403/callback",
			shouldMatch:    false,
		},
		{
			name:           "127.0.0.1 and localhost are different",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://127.0.0.1:57403/callback",
			shouldMatch:    false,
		},

		// Non-loopback should use exact matching only
		{
			name:           "non-loopback - exact match required",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://example.com:8080/callback",
			shouldMatch:    false,
		},
		{
			name:           "non-loopback - different host fails",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://other.com/callback",
			shouldMatch:    false,
		},

		// HTTPS loopback should NOT get dynamic port matching (RFC 8252 says http)
		{
			name:           "https loopback - no dynamic port matching",
			registeredURIs: []string{"https://127.0.0.1/callback"},
			requestedURI:   "https://127.0.0.1:57403/callback",
			shouldMatch:    false,
		},

		// Multiple registered URIs
		{
			name:           "multiple URIs - matches first",
			registeredURIs: []string{"http://127.0.0.1/callback", "https://example.com/callback"},
			requestedURI:   "http://127.0.0.1:8080/callback",
			shouldMatch:    true,
		},
		{
			name:           "multiple URIs - matches second",
			registeredURIs: []string{"http://127.0.0.1/callback", "https://example.com/callback"},
			requestedURI:   "https://example.com/callback",
			shouldMatch:    true,
		},

		// Edge cases
		{
			name:           "empty registered URIs",
			registeredURIs: []string{},
			requestedURI:   "http://127.0.0.1:8080/callback",
			shouldMatch:    false,
		},
		{
			name:           "invalid requested URI",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "://invalid",
			shouldMatch:    false,
		},
		{
			name:           "empty path matches empty path",
			registeredURIs: []string{"http://127.0.0.1"},
			requestedURI:   "http://127.0.0.1:8080",
			shouldMatch:    true,
		},
		{
			name:           "root path matches root path",
			registeredURIs: []string{"http://127.0.0.1/"},
			requestedURI:   "http://127.0.0.1:8080/",
			shouldMatch:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := NewLoopbackClient(&fosite.DefaultOpenIDConnectClient{
				DefaultClient: &fosite.DefaultClient{
					ID:           "test-client",
					RedirectURIs: tt.registeredURIs,
					Public:       true,
				},
			})

			result := client.MatchRedirectURI(tt.requestedURI)
			assert.Equal(t, tt.shouldMatch, result)
		})
	}
}

func TestLoopbackClient_GetMatchingRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		registeredURIs []string
		requestedURI   string
		expectedURI    string
	}{
		{
			name:           "loopback - returns requested URI with port",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:57403/callback",
			expectedURI:    "http://127.0.0.1:57403/callback",
		},
		{
			name:           "non-loopback exact match - returns registered URI",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://example.com/callback",
			expectedURI:    "https://example.com/callback",
		},
		{
			name:           "no match - returns empty string",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://other.com/callback",
			expectedURI:    "",
		},
		{
			name:           "localhost loopback - returns requested URI",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://localhost:8080/callback",
			expectedURI:    "http://localhost:8080/callback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := NewLoopbackClient(&fosite.DefaultOpenIDConnectClient{
				DefaultClient: &fosite.DefaultClient{
					ID:           "test-client",
					RedirectURIs: tt.registeredURIs,
					Public:       true,
				},
			})

			result := client.GetMatchingRedirectURI(tt.requestedURI)
			assert.Equal(t, tt.expectedURI, result)
		})
	}
}

func TestNewClient_PublicClient(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ID:                      "test-public-client",
		RedirectURIs:            []string{"http://127.0.0.1:8080/callback"},
		TokenEndpointAuthMethod: "none",
	}

	client, err := New(cfg)
	require.NoError(t, err)

	// Public clients should be wrapped in LoopbackClient
	_, isLoopback := client.(*publicClient)
	assert.True(t, isLoopback, "public client should be the DCR-issued loopback-wrapped shape")

	// Check basic properties
	assert.Equal(t, "test-public-client", client.GetID())
	assert.True(t, client.IsPublic())
	assert.Equal(t, []string{"http://127.0.0.1:8080/callback"}, client.GetRedirectURIs())

	// The OIDC shape is what activates fosite's method enforcement.
	oidc, ok := client.(fosite.OpenIDConnectClient)
	require.True(t, ok, "public client must satisfy fosite.OpenIDConnectClient")
	assert.Equal(t, "none", oidc.GetTokenEndpointAuthMethod())

	// Check defaults are applied (use ElementsMatch since fosite returns fosite.Arguments type)
	assert.ElementsMatch(t, defaultGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, defaultResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
}

func TestNewClient_ConfidentialClient(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ID:                      "test-confidential-client",
		Secret:                  "my-secret",
		RedirectURIs:            []string{"https://example.com/callback"},
		TokenEndpointAuthMethod: "client_secret_basic",
	}

	client, err := New(cfg)
	require.NoError(t, err)

	// Confidential clients are OIDC clients (method pinning) and are NOT
	// loopback-wrapped — no dynamic-port matching for a secret holder.
	conf, isConfidential := client.(*confidentialClient)
	require.True(t, isConfidential, "confidential client should be the DCR-issued OIDC shape")
	defaultClient := conf.DefaultClient

	// Check basic properties
	assert.Equal(t, "test-confidential-client", client.GetID())
	assert.False(t, client.IsPublic())
	assert.Equal(t, []string{"https://example.com/callback"}, client.GetRedirectURIs())

	oidc, ok := client.(fosite.OpenIDConnectClient)
	require.True(t, ok, "confidential client must satisfy fosite.OpenIDConnectClient")
	assert.Equal(t, "client_secret_basic", oidc.GetTokenEndpointAuthMethod())

	// Verify the secret is hashed with SHA256Hasher, not stored as plaintext
	err = SHA256Hasher.Compare(context.Background(), defaultClient.Secret, []byte("my-secret"))
	assert.NoError(t, err, "stored secret should be a SHA-256 hash of the plaintext")
	assert.NotContains(t, string(defaultClient.Secret), "my-secret")

	// Check defaults are applied (use ElementsMatch since fosite returns fosite.Arguments type)
	assert.ElementsMatch(t, defaultGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, defaultResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
}

func TestNewClient_ConfidentialClientWithoutSecret(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ID:                      "test-client",
		Secret:                  "", // Empty secret
		RedirectURIs:            []string{"https://example.com/callback"},
		TokenEndpointAuthMethod: "client_secret_post",
	}

	client, err := New(cfg)
	assert.Nil(t, client, "client should be nil on error")
	assert.Error(t, err, "confidential client without secret should fail")
	assert.Contains(t, err.Error(), "confidential client requires a secret")
}

// TestNewConfidentialPlain pins the shape NewConfidentialPlain produces: a
// DCR-issued, non-public *fosite.DefaultClient with a hashed secret and no
// fosite.OpenIDConnectClient implementation — the shape whose auth method
// fosite does not enforce, so a client can present credentials via either
// HTTP Basic or the form body.
func TestNewConfidentialPlain(t *testing.T) {
	t.Parallel()

	t.Run("builds a plain non-OIDC confidential DCR-issued client", func(t *testing.T) {
		t.Parallel()
		cfg := Config{
			ID:           "forced-client",
			Secret:       "my-secret",
			RedirectURIs: []string{"https://example.com/callback"},
		}

		client, err := NewConfidentialPlain(cfg)
		require.NoError(t, err)

		assert.Equal(t, "forced-client", client.GetID())
		assert.False(t, client.IsPublic())
		assert.Equal(t, []string{"https://example.com/callback"}, client.GetRedirectURIs())
		assert.True(t, DCRIssued(client), "must carry the DCRIssued marker so storage retention applies")

		_, isOIDC := client.(fosite.OpenIDConnectClient)
		assert.False(t, isOIDC,
			"must NOT implement fosite.OpenIDConnectClient: fosite only enforces "+
				"token_endpoint_auth_method on that interface, and this shape must accept "+
				"either Basic or form-body credential presentation")

		err = SHA256Hasher.Compare(context.Background(), client.GetHashedSecret(), []byte("my-secret"))
		assert.NoError(t, err, "stored secret must be a SHA-256 hash of the plaintext")

		assert.ElementsMatch(t, defaultGrantTypes, client.GetGrantTypes())
		assert.ElementsMatch(t, defaultResponseTypes, client.GetResponseTypes())
		assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
	})

	t.Run("requires a secret", func(t *testing.T) {
		t.Parallel()
		client, err := NewConfidentialPlain(Config{ID: "forced-client"})
		assert.Nil(t, client)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "confidential client requires a secret")
	})
}

// TestNewClient_AuthMethodValidation pins the fail-closed constructor: an
// empty or unrecognized token_endpoint_auth_method is rejected outright —
// silently defaulting would reclassify the client one layer up.
func TestNewClient_AuthMethodValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		wantErr bool
	}{
		{"empty method rejected", "", true},
		{"unknown method rejected", "client_secret_jwt", true},
		{"garbage rejected", "garbage", true},
		{"none accepted", "none", false},
		{"client_secret_basic accepted", "client_secret_basic", false},
		{"client_secret_post accepted", "client_secret_post", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{
				ID:                      "test-client",
				Secret:                  "my-secret",
				RedirectURIs:            []string{"https://example.com/callback"},
				TokenEndpointAuthMethod: tt.method,
			}
			client, err := New(cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported token_endpoint_auth_method")
				assert.Nil(t, client)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

func TestNewClient_CustomOverrides(t *testing.T) {
	t.Parallel()

	customGrantTypes := []string{"authorization_code", "client_credentials"}
	customResponseTypes := []string{"code", "token"}
	customScopes := []string{"openid", "custom-scope"}

	cfg := Config{
		ID:                      "test-custom-client",
		RedirectURIs:            []string{"http://localhost:3000/callback"},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              customGrantTypes,
		ResponseTypes:           customResponseTypes,
		Scopes:                  customScopes,
	}

	client, err := New(cfg)
	require.NoError(t, err)

	// Custom values should be used instead of defaults (use ElementsMatch since fosite returns fosite.Arguments type)
	assert.ElementsMatch(t, customGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, customResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, customScopes, client.GetScopes())
}

func TestNewClient_EmptySlicesUseDefaults(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ID:                      "test-client",
		RedirectURIs:            []string{"http://localhost:8080/callback"},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              nil,        // nil should use defaults
		ResponseTypes:           []string{}, // empty should use defaults
		Scopes:                  nil,
	}

	client, err := New(cfg)
	require.NoError(t, err)

	// Use ElementsMatch since fosite returns fosite.Arguments type
	assert.ElementsMatch(t, defaultGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, defaultResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
}
