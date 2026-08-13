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

	grantTypes := []string{"urn:ietf:params:oauth:grant-type:token-exchange"}
	scopes := []string{"openid"}
	resources := []string{"https://resource.example.com"}
	audiences := []string{"https://api.example.com"}
	client, err := NewSPIFFEClient("spiffe-client", grantTypes, scopes, resources, audiences, true)
	require.NoError(t, err)

	grantTypes[0] = "changed"
	scopes[0] = "changed"
	resources[0] = "changed"
	audiences[0] = "changed"

	assert.Equal(t, "spiffe-client", client.GetID())
	assert.Nil(t, client.GetHashedSecret())
	assert.Nil(t, client.GetRedirectURIs())
	assert.Nil(t, client.GetResponseTypes())
	assert.False(t, client.IsPublic())
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", client.GetGrantTypes()[0])
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, "https://resource.example.com", client.Resources()[0])
	assert.Equal(t, "https://api.example.com", client.Audiences()[0])
	assert.True(t, client.TokenExchangeEnabled())
	assert.ElementsMatch(t, []string{"https://resource.example.com", "https://api.example.com"}, client.GetAudience())

	client.GetScopes()[0] = "mutated"
	client.Resources()[0] = "mutated"
	client.Audiences()[0] = "mutated"
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, "https://resource.example.com", client.Resources()[0])
	assert.Equal(t, "https://api.example.com", client.Audiences()[0])

	disabledClient, err := NewSPIFFEClient("disabled-exchange", nil, nil, nil, nil, false)
	require.NoError(t, err)
	assert.False(t, disabledClient.TokenExchangeEnabled())
}

func TestNewSPIFFEClient_RequiresID(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEClient("", nil, nil, nil, nil, false)
	require.Error(t, err)
}

func TestSPIFFEClient_GetAudience(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resources []string
		audiences []string
		want      []string
	}{
		{
			name:      "overlapping resources and audiences dedup",
			resources: []string{"https://shared.example.com", "https://resource-only.example.com"},
			audiences: []string{"https://shared.example.com", "https://audience-only.example.com"},
			want: []string{
				"https://shared.example.com",
				"https://resource-only.example.com",
				"https://audience-only.example.com",
			},
		},
		{
			name:      "resources only",
			resources: []string{"https://resource.example.com"},
			audiences: nil,
			want:      []string{"https://resource.example.com"},
		},
		{
			name:      "audiences only",
			resources: nil,
			audiences: []string{"https://api.example.com"},
			want:      []string{"https://api.example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewSPIFFEClient("spiffe-client", nil, nil, tt.resources, tt.audiences, false)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, client.GetAudience())
		})
	}
}
