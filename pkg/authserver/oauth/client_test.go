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

package oauth

import (
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
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

func TestLoopbackClient_ImplementsFositeClient(t *testing.T) {
	t.Parallel()

	// Verify LoopbackClient implements fosite.Client interface
	var _ fosite.Client = (*LoopbackClient)(nil)

	defaultClient := &fosite.DefaultClient{
		ID:            "test-client",
		RedirectURIs:  []string{"http://127.0.0.1/callback"},
		GrantTypes:    []string{"authorization_code"},
		ResponseTypes: []string{"code"},
		Scopes:        []string{"openid"},
		Public:        true,
	}

	client := NewLoopbackClient(defaultClient)

	// Test all interface methods
	assert.Equal(t, "test-client", client.GetID())
	assert.Equal(t, []string{"http://127.0.0.1/callback"}, client.GetRedirectURIs())
	assert.Equal(t, fosite.Arguments{"authorization_code"}, client.GetGrantTypes())
	assert.Equal(t, fosite.Arguments{"code"}, client.GetResponseTypes())
	assert.Equal(t, fosite.Arguments{"openid"}, client.GetScopes())
	assert.True(t, client.IsPublic())
	assert.Empty(t, client.GetHashedSecret())
	assert.Empty(t, client.GetAudience())
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

		// RFC 8252 Section 7.3 - IPv6 loopback ([::1])
		{
			name:           "loopback IPv6 - dynamic port matches",
			registeredURIs: []string{"http://[::1]/callback"},
			requestedURI:   "http://[::1]:57403/callback",
			shouldMatch:    true,
		},
		{
			name:           "loopback IPv6 - path must match",
			registeredURIs: []string{"http://[::1]/callback"},
			requestedURI:   "http://[::1]:57403/other",
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
			name:           "loopback localhost - case insensitive",
			registeredURIs: []string{"http://localhost/callback"},
			requestedURI:   "http://LOCALHOST:57403/callback",
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

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		hostname   string
		isLoopback bool
	}{
		// Valid loopback addresses
		{"127.0.0.1", true},
		{"::1", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{"LocalHost", true},

		// Other addresses in 127.0.0.0/8 are also loopback
		{"127.0.0.2", true},
		{"127.255.255.255", true},

		// Not loopback addresses
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"example.com", false},
		{"", false},
		{"0.0.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			t.Parallel()

			result := IsLoopbackHost(tt.hostname)
			assert.Equal(t, tt.isLoopback, result, "IsLoopbackHost(%q)", tt.hostname)
		})
	}
}

func TestHostnamesMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		requested  string
		registered string
		shouldMach bool
	}{
		{"same IP", "127.0.0.1", "127.0.0.1", true},
		{"same localhost lowercase", "localhost", "localhost", true},
		{"localhost case insensitive", "LOCALHOST", "localhost", true},
		{"localhost case insensitive reverse", "localhost", "LOCALHOST", true},
		{"different IPs", "127.0.0.1", "192.168.1.1", false},
		{"IP vs localhost", "127.0.0.1", "localhost", false},
		{"localhost vs IP", "localhost", "127.0.0.1", false},
		{"IPv6 same", "::1", "::1", true},
		{"IPv6 vs IPv4", "::1", "127.0.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := hostnamesMatch(tt.requested, tt.registered)
			assert.Equal(t, tt.shouldMach, result)
		})
	}
}

func TestMatchesAsLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		requestedURI  string
		registeredURI string
		shouldMatch   bool
	}{
		{
			name:          "valid loopback match",
			requestedURI:  "http://127.0.0.1:8080/callback",
			registeredURI: "http://127.0.0.1/callback",
			shouldMatch:   true,
		},
		{
			name:          "https not allowed for loopback",
			requestedURI:  "https://127.0.0.1:8080/callback",
			registeredURI: "https://127.0.0.1/callback",
			shouldMatch:   false,
		},
		{
			name:          "mixed schemes not allowed",
			requestedURI:  "http://127.0.0.1:8080/callback",
			registeredURI: "https://127.0.0.1/callback",
			shouldMatch:   false,
		},
		{
			name:          "non-loopback requested",
			requestedURI:  "http://example.com:8080/callback",
			registeredURI: "http://127.0.0.1/callback",
			shouldMatch:   false,
		},
		{
			name:          "non-loopback registered",
			requestedURI:  "http://127.0.0.1:8080/callback",
			registeredURI: "http://example.com/callback",
			shouldMatch:   false,
		},
		{
			name:          "path mismatch",
			requestedURI:  "http://127.0.0.1:8080/other",
			registeredURI: "http://127.0.0.1/callback",
			shouldMatch:   false,
		},
		{
			name:          "query match",
			requestedURI:  "http://127.0.0.1:8080/callback?foo=bar",
			registeredURI: "http://127.0.0.1/callback?foo=bar",
			shouldMatch:   true,
		},
		{
			name:          "query mismatch",
			requestedURI:  "http://127.0.0.1:8080/callback?foo=bar",
			registeredURI: "http://127.0.0.1/callback",
			shouldMatch:   false,
		},
		{
			name:          "invalid requested URI",
			requestedURI:  "://invalid",
			registeredURI: "http://127.0.0.1/callback",
			shouldMatch:   false,
		},
		{
			name:          "invalid registered URI",
			requestedURI:  "http://127.0.0.1:8080/callback",
			registeredURI: "://invalid",
			shouldMatch:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := matchesAsLoopback(tt.requestedURI, tt.registeredURI)
			assert.Equal(t, tt.shouldMatch, result)
		})
	}
}
