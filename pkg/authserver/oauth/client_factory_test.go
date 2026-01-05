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
	"github.com/stretchr/testify/require"
)

func TestNewClient_PublicClient(t *testing.T) {
	t.Parallel()

	cfg := ClientConfig{
		ID:           "test-public-client",
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
		Public:       true,
	}

	client := NewClient(cfg)

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

	cfg := ClientConfig{
		ID:           "test-confidential-client",
		Secret:       "my-secret",
		RedirectURIs: []string{"https://example.com/callback"},
		Public:       false,
	}

	client := NewClient(cfg)

	// Confidential clients should be DefaultClient, not wrapped
	defaultClient, isDefault := client.(*fosite.DefaultClient)
	require.True(t, isDefault, "confidential client should be *fosite.DefaultClient")

	// Check basic properties
	assert.Equal(t, "test-confidential-client", client.GetID())
	assert.False(t, client.IsPublic())
	assert.Equal(t, []string{"https://example.com/callback"}, client.GetRedirectURIs())
	assert.Equal(t, []byte("my-secret"), defaultClient.Secret)

	// Check defaults are applied (use ElementsMatch since fosite returns fosite.Arguments type)
	assert.ElementsMatch(t, DefaultGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, DefaultResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
}

func TestNewClient_ConfidentialClientWithoutSecret(t *testing.T) {
	t.Parallel()

	cfg := ClientConfig{
		ID:           "test-client",
		Secret:       "", // Empty secret
		RedirectURIs: []string{"https://example.com/callback"},
		Public:       false,
	}

	client := NewClient(cfg)

	defaultClient, isDefault := client.(*fosite.DefaultClient)
	require.True(t, isDefault)
	assert.Nil(t, defaultClient.Secret, "empty secret should result in nil Secret field")
}

func TestNewClient_CustomOverrides(t *testing.T) {
	t.Parallel()

	customGrantTypes := []string{"authorization_code", "client_credentials"}
	customResponseTypes := []string{"code", "token"}
	customScopes := []string{"openid", "custom-scope"}

	cfg := ClientConfig{
		ID:            "test-custom-client",
		RedirectURIs:  []string{"http://localhost:3000/callback"},
		Public:        true,
		GrantTypes:    customGrantTypes,
		ResponseTypes: customResponseTypes,
		Scopes:        customScopes,
	}

	client := NewClient(cfg)

	// Custom values should be used instead of defaults (use ElementsMatch since fosite returns fosite.Arguments type)
	assert.ElementsMatch(t, customGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, customResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, customScopes, client.GetScopes())
}

func TestNewClient_EmptySlicesUseDefaults(t *testing.T) {
	t.Parallel()

	cfg := ClientConfig{
		ID:            "test-client",
		RedirectURIs:  []string{"http://localhost:8080/callback"},
		Public:        true,
		GrantTypes:    nil,        // nil should use defaults
		ResponseTypes: []string{}, // empty should use defaults
		Scopes:        nil,
	}

	client := NewClient(cfg)

	// Use ElementsMatch since fosite returns fosite.Arguments type
	assert.ElementsMatch(t, DefaultGrantTypes, client.GetGrantTypes())
	assert.ElementsMatch(t, DefaultResponseTypes, client.GetResponseTypes())
	assert.ElementsMatch(t, DefaultScopes, client.GetScopes())
}

func TestDefaultConstants(t *testing.T) {
	t.Parallel()

	// Verify the default constants have expected values
	assert.Contains(t, DefaultGrantTypes, "authorization_code")
	assert.Contains(t, DefaultGrantTypes, "refresh_token")

	assert.Contains(t, DefaultResponseTypes, "code")

	assert.Contains(t, DefaultScopes, "openid")
	assert.Contains(t, DefaultScopes, "profile")
	assert.Contains(t, DefaultScopes, "email")
}

func TestNewClient_LoopbackClientSupportsRFC8252(t *testing.T) {
	t.Parallel()

	cfg := ClientConfig{
		ID:           "loopback-test",
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
		Public:       true,
	}

	client := NewClient(cfg)

	loopbackClient, ok := client.(*LoopbackClient)
	require.True(t, ok, "public client should be LoopbackClient")

	// RFC 8252 Section 7.3: The port should be ignored for loopback matching
	// Same host with different port should match
	assert.True(t, loopbackClient.MatchRedirectURI("http://127.0.0.1:9999/callback"),
		"loopback client should allow any port per RFC 8252")

	// Different path should not match
	assert.False(t, loopbackClient.MatchRedirectURI("http://127.0.0.1:8080/other"),
		"path must match exactly")
}
