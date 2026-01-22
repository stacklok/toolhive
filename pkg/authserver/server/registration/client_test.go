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
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestNewLoopbackClient(t *testing.T) {
	t.Parallel()

	defaultClient := &fosite.DefaultClient{
		ID:           "test-client",
		RedirectURIs: []string{"http://127.0.0.1/callback"},
		Public:       true,
	}

	client := NewLoopbackClient(defaultClient)

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

			client := NewLoopbackClient(&fosite.DefaultClient{
				ID:           "test-client",
				RedirectURIs: tt.registeredURIs,
				Public:       true,
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

			client := NewLoopbackClient(&fosite.DefaultClient{
				ID:           "test-client",
				RedirectURIs: tt.registeredURIs,
				Public:       true,
			})

			result := client.GetMatchingRedirectURI(tt.requestedURI)
			assert.Equal(t, tt.expectedURI, result)
		})
	}
}

func TestNewClient_PublicClient(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ID:           "test-public-client",
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
		Public:       true,
	}

	client, err := New(cfg)
	require.NoError(t, err)

	// Public clients should be wrapped in LoopbackClient
	_, isLoopback := client.(*LoopbackClient)
	assert.True(t, isLoopback, "public client should be wrapped in LoopbackClient")

	// Check basic properties
	assert.Equal(t, "test-public-client", client.GetID())
	assert.True(t, client.IsPublic())
	assert.Equal(t, []string{"http://127.0.0.1:8080/callback"}, client.GetRedirectURIs())

	// Check defaults are applied (use ElementsMatch since fosite returns fosite.Arguments type)
	assert.ElementsMatch(t, DefaultGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, DefaultResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
}

func TestNewClient_ConfidentialClient(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ID:           "test-confidential-client",
		Secret:       "my-secret",
		RedirectURIs: []string{"https://example.com/callback"},
		Public:       false,
	}

	client, err := New(cfg)
	require.NoError(t, err)

	// Confidential clients should be DefaultClient, not wrapped
	defaultClient, isDefault := client.(*fosite.DefaultClient)
	require.True(t, isDefault, "confidential client should be *fosite.DefaultClient")

	// Check basic properties
	assert.Equal(t, "test-confidential-client", client.GetID())
	assert.False(t, client.IsPublic())
	assert.Equal(t, []string{"https://example.com/callback"}, client.GetRedirectURIs())

	// Verify the secret is bcrypt-hashed, not stored as plaintext
	err = bcrypt.CompareHashAndPassword(defaultClient.Secret, []byte("my-secret"))
	assert.NoError(t, err, "stored secret should be bcrypt hash of plaintext")

	// Check defaults are applied (use ElementsMatch since fosite returns fosite.Arguments type)
	assert.ElementsMatch(t, DefaultGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, DefaultResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
}

func TestNewClient_ConfidentialClientWithoutSecret(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ID:           "test-client",
		Secret:       "", // Empty secret
		RedirectURIs: []string{"https://example.com/callback"},
		Public:       false,
	}

	client, err := New(cfg)
	assert.Nil(t, client, "client should be nil on error")
	assert.Error(t, err, "confidential client without secret should fail")
	assert.Contains(t, err.Error(), "confidential client requires a secret")
}

func TestNewClient_CustomOverrides(t *testing.T) {
	t.Parallel()

	customGrantTypes := []string{"authorization_code", "client_credentials"}
	customResponseTypes := []string{"code", "token"}
	customScopes := []string{"openid", "custom-scope"}

	cfg := Config{
		ID:            "test-custom-client",
		RedirectURIs:  []string{"http://localhost:3000/callback"},
		Public:        true,
		GrantTypes:    customGrantTypes,
		ResponseTypes: customResponseTypes,
		Scopes:        customScopes,
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
		ID:            "test-client",
		RedirectURIs:  []string{"http://localhost:8080/callback"},
		Public:        true,
		GrantTypes:    nil,        // nil should use defaults
		ResponseTypes: []string{}, // empty should use defaults
		Scopes:        nil,
	}

	client, err := New(cfg)
	require.NoError(t, err)

	// Use ElementsMatch since fosite returns fosite.Arguments type
	assert.ElementsMatch(t, DefaultGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, DefaultResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
}
