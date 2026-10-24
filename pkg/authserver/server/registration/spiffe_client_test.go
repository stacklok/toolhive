// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSPIFFEClient(t *testing.T) {
	t.Parallel()

	scopes := []string{"openid"}
	audiences := []string{"https://api.example.com"}
	resources := []string{"https://resource.example.com"}
	client, err := NewSPIFFEClient("spiffe-client", scopes, audiences, resources)
	require.NoError(t, err)

	scopes[0] = "changed"
	audiences[0] = "changed"
	resources[0] = "changed"

	assert.Equal(t, "spiffe-client", client.GetID())
	assert.Nil(t, client.GetHashedSecret())
	assert.Nil(t, client.GetRedirectURIs())
	assert.Nil(t, client.GetResponseTypes())
	assert.False(t, client.IsPublic())
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", client.GetGrantTypes()[0])
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, []string{"https://api.example.com"}, client.Audiences())
	assert.Equal(t, []string{"https://api.example.com"}, []string(client.GetAudience()))
	assert.Equal(t, []string{"https://resource.example.com"}, client.Resources())

	client.GetScopes()[0] = "mutated"
	client.Audiences()[0] = "mutated"
	client.GetAudience()[0] = "mutated"
	client.Resources()[0] = "mutated"
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, []string{"https://api.example.com"}, client.Audiences())
	assert.Equal(t, []string{"https://api.example.com"}, []string(client.GetAudience()))
	assert.Equal(t, []string{"https://resource.example.com"}, client.Resources())

	_, err = NewSPIFFEClient("disabled-exchange", nil, nil, nil)
	require.Error(t, err)
}

// TestNewSPIFFEClient_DisjointAudiencesAndResources proves audiences and
// resources are independently tracked, not aliases of the same underlying
// field: a client configured with disjoint allowlists must return each list
// unmodified by the other, and GetAudience (the RFC 8693 audience allowlist)
// must never leak the RFC 8707 resource allowlist or vice versa.
func TestNewSPIFFEClient_DisjointAudiencesAndResources(t *testing.T) {
	t.Parallel()

	client, err := NewSPIFFEClient(
		"spiffe-client",
		[]string{"openid"},
		[]string{"https://audience.example.com"},
		[]string{"https://resource.example.com"},
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"https://audience.example.com"}, []string(client.GetAudience()))
	assert.Equal(t, []string{"https://resource.example.com"}, client.Resources())
	assert.NotContains(t, client.GetAudience(), "https://resource.example.com")
	assert.NotContains(t, client.Resources(), "https://audience.example.com")
}

func TestNewSPIFFEClient_RequiresID(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEClient("", nil, nil, nil)
	require.Error(t, err)
}

func TestNewSPIFFEClient_RequiresAudiences(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEClient(
		"spiffe-client",
		[]string{"openid"},
		nil,
		nil,
	)
	require.EqualError(t, err, "SPIFFE client scopes and audiences are required")
}
