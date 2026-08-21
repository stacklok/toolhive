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

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// TestRegisteredLoopbackRedirectURI covers RegisteredLoopbackRedirectURI, the
// sole production-reachable matcher for loopback clients.
func TestRegisteredLoopbackRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		registeredURIs []string
		requestedURI   string
		expectedURI    string
		expectedOK     bool
	}{
		{
			name:           "exact match - https",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://example.com/callback",
			expectedURI:    "https://example.com/callback",
			expectedOK:     true,
		},
		{
			name:           "exact match - http loopback with port",
			registeredURIs: []string{"http://127.0.0.1:8080/callback"},
			requestedURI:   "http://127.0.0.1:8080/callback",
			expectedURI:    "http://127.0.0.1:8080/callback",
			expectedOK:     true,
		},

		// RFC 8252 Section 7.3 - IPv4 loopback (127.0.0.1)
		{
			name:           "loopback IPv4 - dynamic port matches, returns registered portless URI",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:54321/callback",
			expectedURI:    "http://127.0.0.1/callback",
			expectedOK:     true,
		},
		{
			name:           "loopback IPv4 - no port in request matches registered without port",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1/callback",
			expectedURI:    "http://127.0.0.1/callback",
			expectedOK:     true,
		},
		{
			name:           "loopback IPv4 - path must match",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:57403/other",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "loopback IPv4 - query must match",
			registeredURIs: []string{"http://127.0.0.1/callback?foo=bar"},
			requestedURI:   "http://127.0.0.1:57403/callback?foo=bar",
			expectedURI:    "http://127.0.0.1/callback?foo=bar",
			expectedOK:     true,
		},
		{
			name:           "loopback IPv4 - query mismatch fails",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:57403/callback?extra=param",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "loopback IPv4 - fragment on requested URI rejected",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://127.0.0.1:57403/callback#frag",
			expectedURI:    "",
			expectedOK:     false,
		},

		// RFC 8252 Section 7.3 - localhost
		{
			name:           "localhost loopback - returns registered portless URI",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://localhost:54321/callback",
			expectedURI:    "http://localhost/callback",
			expectedOK:     true,
		},
		{
			name:           "loopback localhost - path must match",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://localhost:57403/other",
			expectedURI:    "",
			expectedOK:     false,
		},
		// A percent-encoded separator in the registered path must NOT match an
		// unencoded literal that merely decodes to the same string: Path is
		// decoded, so comparing it (instead of EscapedPath) would treat
		// "/callback%2Fchild" (registered) and "/callback/child" (requested,
		// never actually registered) as equal.
		{
			name:           "encoded path separator does not match unencoded literal",
			registeredURIs: []string{"http://localhost/callback%2Fchild"},
			requestedURI:   "http://localhost:57403/callback/child",
			expectedURI:    "",
			expectedOK:     false,
		},
		// A bare trailing "?" (ForceQuery) must not be treated as equivalent
		// to no query string at all: both parse to an empty RawQuery, so
		// comparing RawQuery alone can't tell "/callback" from "/callback?".
		{
			name:           "bare trailing question mark does not match no query string",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://localhost:57403/callback?",
			expectedURI:    "",
			expectedOK:     false,
		},

		// RFC 6749 §3.1.2: the redirection endpoint URI MUST NOT include a
		// fragment component or userinfo; a dynamic-port match must not let
		// either through unvalidated.
		{
			name:           "loopback localhost - fragment on requested URI rejected",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://localhost:57403/callback#frag",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "loopback localhost - userinfo on requested URI rejected",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://user:pass@localhost:57403/callback",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "loopback localhost - userinfo on registered URI rejected",
			registeredURIs: []string{"http://user:pass@localhost/callback"},
			requestedURI:   "http://localhost:57403/callback",
			expectedURI:    "",
			expectedOK:     false,
		},

		// isLoopbackHostname is self-contained (not networking.IsLocalhost, which
		// has a separate, wider-blast-radius bug: a case-sensitive prefix check
		// requiring the bracketed "[::1]" form that url.Hostname() never
		// produces -- gating ~15 unrelated HTTPS-exemption/DCR/discovery call
		// sites, tracked separately, out of scope for #6189). So both [::1] and
		// mixed-case "localhost" work correctly here.
		{
			name:           "IPv6 loopback [::1] - dynamic port matches",
			registeredURIs: []string{"http://[::1]/callback"},
			requestedURI:   "http://[::1]:54321/callback",
			expectedURI:    "http://[::1]/callback",
			expectedOK:     true,
		},
		{
			name:           "case-insensitive localhost - dynamic port matches",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://LOCALHOST:54321/callback",
			expectedURI:    "http://localhost/callback",
			expectedOK:     true,
		},

		// Cross-hostname matching should NOT work (security requirement)
		{
			name:           "localhost and 127.0.0.1 are different (registered 127.0.0.1)",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "http://localhost:57403/callback",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "wrong loopback host - localhost does not match 127.0.0.1 (registered localhost)",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://127.0.0.1:54321/callback",
			expectedURI:    "",
			expectedOK:     false,
		},

		// Non-loopback should use exact matching only
		{
			name:           "non-loopback - exact match required",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://example.com:8080/callback",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "non-loopback - different host fails",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://other.com/callback",
			expectedURI:    "",
			expectedOK:     false,
		},

		// HTTPS loopback should NOT get dynamic port matching (RFC 8252 says http)
		{
			name:           "https loopback - no dynamic port matching",
			registeredURIs: []string{"https://127.0.0.1/callback"},
			requestedURI:   "https://127.0.0.1:57403/callback",
			expectedURI:    "",
			expectedOK:     false,
		},

		// Multiple registered URIs
		{
			name:           "multiple URIs - matches first",
			registeredURIs: []string{"http://127.0.0.1/callback", "https://example.com/callback"},
			requestedURI:   "http://127.0.0.1:8080/callback",
			expectedURI:    "http://127.0.0.1/callback",
			expectedOK:     true,
		},
		{
			name:           "multiple URIs - matches second",
			registeredURIs: []string{"http://127.0.0.1/callback", "https://example.com/callback"},
			requestedURI:   "https://example.com/callback",
			expectedURI:    "https://example.com/callback",
			expectedOK:     true,
		},

		// A registered port is not pinned: matchesAsLoopback only checks
		// scheme/hostname/path/query, so a registered port is ignored on both
		// sides, not just the requested one. Defensible under RFC 8252 (native
		// apps don't have a fixed listening port to pin to), but this is the
		// security-relevant edge of the loopback carve-out, so it must be
		// explicitly pinned by a test rather than left implicit.
		{
			name:           "registered port is not pinned - any requested port still matches",
			registeredURIs: []string{"http://localhost:8080/callback"},
			requestedURI:   "http://localhost:54321/callback",
			expectedURI:    "http://localhost:8080/callback",
			expectedOK:     true,
		},

		// Edge cases
		{
			name:           "empty registered URIs",
			registeredURIs: []string{},
			requestedURI:   "http://127.0.0.1:8080/callback",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "invalid requested URI",
			registeredURIs: []string{"http://127.0.0.1/callback"},
			requestedURI:   "://invalid",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "no match - returns empty string and false",
			registeredURIs: []string{"https://example.com/callback"},
			requestedURI:   "https://other.com/callback",
			expectedURI:    "",
			expectedOK:     false,
		},
		{
			name:           "empty path matches empty path",
			registeredURIs: []string{"http://127.0.0.1"},
			requestedURI:   "http://127.0.0.1:8080",
			expectedURI:    "http://127.0.0.1",
			expectedOK:     true,
		},
		{
			name:           "root path matches root path",
			registeredURIs: []string{"http://127.0.0.1/"},
			requestedURI:   "http://127.0.0.1:8080/",
			expectedURI:    "http://127.0.0.1/",
			expectedOK:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fosite.DefaultOpenIDConnectClient{
				DefaultClient: &fosite.DefaultClient{
					ID:           "test-client",
					RedirectURIs: tt.registeredURIs,
					Public:       true,
				},
			}

			uri, ok := RegisteredLoopbackRedirectURI(client, tt.requestedURI)
			assert.Equal(t, tt.expectedURI, uri)
			assert.Equal(t, tt.expectedOK, ok)
		})
	}
}

// TestRegisteredLoopbackRedirectURI_ConfidentialClientNeverMatches pins the
// IsPublic() guard: RegisteredLoopbackRedirectURI must never grant loopback
// dynamic-port matching to a confidential client, even if constructed
// directly with loopback-shaped redirect URIs (bypassing New's own
// public-only wrapping).
func TestRegisteredLoopbackRedirectURI_ConfidentialClientNeverMatches(t *testing.T) {
	t.Parallel()

	client := &fosite.DefaultOpenIDConnectClient{
		DefaultClient: &fosite.DefaultClient{
			ID:           "test-client",
			RedirectURIs: []string{"http://localhost/callback"},
			Public:       false,
		},
	}

	uri, ok := RegisteredLoopbackRedirectURI(client, "http://localhost:54321/callback")
	assert.Equal(t, "", uri)
	assert.False(t, ok, "a confidential client must not get loopback dynamic-port matching")
}

// TestRegisteredLoopbackRedirectURI_ExactMatchPrecedesLoopbackMatch pins that
// exact registered matches take precedence over loopback dynamic-port
// matches: when a public client is registered with both a portless loopback
// URI and an exact-port loopback URI, a request for the exact-port URI must
// return that exact entry, not the portless entry that would also
// loopback-match. Order of registration must not affect the outcome.
func TestRegisteredLoopbackRedirectURI_ExactMatchPrecedesLoopbackMatch(t *testing.T) {
	t.Parallel()

	portless := "http://localhost/callback"
	exact := "http://localhost:54321/callback"

	tests := []struct {
		name           string
		registeredURIs []string
	}{
		{name: "portless registered first", registeredURIs: []string{portless, exact}},
		{name: "exact registered first", registeredURIs: []string{exact, portless}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fosite.DefaultOpenIDConnectClient{
				DefaultClient: &fosite.DefaultClient{
					ID:           "test-client",
					RedirectURIs: tt.registeredURIs,
					Public:       true,
				},
			}

			uri, ok := RegisteredLoopbackRedirectURI(client, exact)
			require.True(t, ok)
			assert.Equal(t, exact, uri, "exact match must win over the portless loopback match")
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

	// Public clients are the DCR-issued publicClient shape (an OIDC client so
	// TokenEndpointAuthMethod is set).
	_, isPublic := client.(*publicClient)
	assert.True(t, isPublic, "public client should be the DCR-issued publicClient shape")

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

func TestNewStaticDelegateClient(t *testing.T) {
	t.Parallel()

	client, err := NewStaticDelegateClient(Config{
		ID:         "delegate",
		Secret:     "test-secret",
		GrantTypes: []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
		Scopes:     []string{"openid"},
		Audience:   []string{"https://mcp.example"},
	})
	require.NoError(t, err)

	assert.IsType(t, &fosite.DefaultClient{}, client)
	assert.False(t, client.IsPublic())
	assert.False(t, DCRIssued(client))
	assert.Empty(t, client.GetRedirectURIs())
	// Asserts on the underlying field, not GetResponseTypes(): fosite.DefaultClient's
	// getter falls back to Arguments{"code"} whenever ResponseTypes is unset (per the
	// OIDC dynamic registration default), regardless of client type, so the getter
	// is never empty for this or any other client that doesn't set it explicitly.
	assert.Empty(t, client.ResponseTypes)
	// Use ElementsMatch since fosite returns fosite.Arguments type.
	assert.ElementsMatch(t, []string{"urn:ietf:params:oauth:grant-type:token-exchange"}, client.GetGrantTypes())
	assert.ElementsMatch(t, []string{"openid"}, client.GetScopes())
	assert.ElementsMatch(t, []string{"https://mcp.example"}, client.GetAudience())
	assert.NoError(t, SHA256Hasher.Compare(context.Background(), client.GetHashedSecret(), []byte("test-secret")))
	assert.Error(t, SHA256Hasher.Compare(context.Background(), client.GetHashedSecret(), []byte("wrong-secret")))
}

func TestNewStaticDelegateClient_Validation(t *testing.T) {
	t.Parallel()

	validGrantTypes := []string{oauthproto.GrantTypeTokenExchange}
	validScopes := []string{"openid"}
	validAudience := []string{"https://mcp.example"}
	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{
			name: "missing ID",
			config: Config{
				Secret:     "test-secret",
				GrantTypes: validGrantTypes,
				Scopes:     validScopes,
				Audience:   validAudience,
			},
			wantErr: "delegate client requires an ID",
		},
		{
			name: "missing secret",
			config: Config{
				ID:         "delegate",
				GrantTypes: validGrantTypes,
				Scopes:     validScopes,
				Audience:   validAudience,
			},
			wantErr: "confidential client requires a secret",
		},
		{
			name: "missing grant types",
			config: Config{
				ID:       "delegate",
				Secret:   "test-secret",
				Scopes:   validScopes,
				Audience: validAudience,
			},
			wantErr: "delegate client grant types must be exactly",
		},
		{
			name: "non-token-exchange grant type",
			config: Config{
				ID:         "delegate",
				Secret:     "test-secret",
				GrantTypes: []string{"authorization_code"},
				Scopes:     validScopes,
				Audience:   validAudience,
			},
			wantErr: "delegate client grant types must be exactly",
		},
		{
			name: "multiple grant types",
			config: Config{
				ID:         "delegate",
				Secret:     "test-secret",
				GrantTypes: []string{oauthproto.GrantTypeTokenExchange, "authorization_code"},
				Scopes:     validScopes,
				Audience:   validAudience,
			},
			wantErr: "delegate client grant types must be exactly",
		},
		{
			name: "missing scopes",
			config: Config{
				ID:         "delegate",
				Secret:     "test-secret",
				GrantTypes: validGrantTypes,
				Audience:   validAudience,
			},
			wantErr: "delegate client requires at least one scope",
		},
		{
			name: "missing audience",
			config: Config{
				ID:         "delegate",
				Secret:     "test-secret",
				GrantTypes: validGrantTypes,
				Scopes:     validScopes,
			},
			wantErr: "delegate client requires at least one audience",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewStaticDelegateClient(tt.config)
			assert.Nil(t, client)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotContains(t, err.Error(), "test-secret")
		})
	}
}
