// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

func TestNewSPIFFEClient(t *testing.T) {
	t.Parallel()

	grantTypes := []string{"urn:ietf:params:oauth:grant-type:token-exchange"}
	scopes := []string{"openid"}
	audiences := []string{"https://api.example.com"}
	resources := []string{"https://resource.example.com"}
	client, err := NewSPIFFEClient("spiffe-client", grantTypes, scopes, audiences, resources)
	require.NoError(t, err)

	grantTypes[0] = "changed"
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

	_, err = NewSPIFFEClient("disabled-exchange", grantTypes, nil, nil, nil)
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
		[]string{"urn:ietf:params:oauth:grant-type:token-exchange"},
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

	_, err := NewSPIFFEClient("", nil, nil, nil, nil)
	require.Error(t, err)
}

func TestNewSPIFFEClient_RequiresGrantTypes(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEClient("spiffe-client", nil, []string{"openid"}, []string{"https://api.example.com"}, nil)
	require.EqualError(t, err, "SPIFFE client grant types are required")
}

func TestNewSPIFFEClient_RequiresAudiences(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEClient(
		"spiffe-client",
		[]string{"urn:ietf:params:oauth:grant-type:token-exchange"},
		[]string{"openid"},
		nil,
		nil,
	)
	require.EqualError(t, err, "SPIFFE client scopes and audiences are required")
}

func TestAuthenticatedSPIFFEClient(t *testing.T) {
	t.Parallel()

	static, err := NewSPIFFEClient(
		"client",
		[]string{"client_credentials"},
		[]string{"openid"},
		[]string{"https://audience.example.com"},
		[]string{"https://resource.example.com"},
	)
	require.NoError(t, err)
	valid := spiffeauth.NewNormalizedSPIFFEPrincipal(
		"client", "spiffe://example.org/workload/concrete", "example.org", spiffeauth.SPIFFEAuthenticationMethodJWT,
		spiffeauth.NewSPIFFEAuthorizationPolicy(
			[]string{"client_credentials"}, []string{"openid"}, []string{"https://resource.example.com"},
			[]string{"https://audience.example.com"}, false,
		),
	)

	tests := []struct {
		name      string
		client    *SPIFFEClient
		principal spiffeauth.NormalizedSPIFFEPrincipal
		wantErr   string
	}{
		{name: "valid immutable carrier", client: static, principal: valid},
		{name: "nil client", principal: valid, wantErr: "SPIFFE client is required"},
		{
			name:      "missing principal identity",
			client:    static,
			principal: spiffeauth.NewNormalizedSPIFFEPrincipal("", "", "", spiffeauth.SPIFFEAuthenticationMethodJWT, spiffeauth.SPIFFEAuthorizationPolicy{}),
			wantErr:   "principal identity",
		},
		{
			name:      "invalid method",
			client:    static,
			principal: spiffeauth.NewNormalizedSPIFFEPrincipal("client", "spiffe://example.org/workload/concrete", "example.org", "unknown", spiffeauth.SPIFFEAuthorizationPolicy{}),
			wantErr:   "invalid authentication method",
		},
		{
			name:      "client ID mismatch",
			client:    static,
			principal: spiffeauth.NewNormalizedSPIFFEPrincipal("other", "spiffe://example.org/workload/concrete", "example.org", spiffeauth.SPIFFEAuthenticationMethodJWT, spiffeauth.SPIFFEAuthorizationPolicy{}),
			wantErr:   "does not match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewAuthenticatedSPIFFEClient(tt.client, tt.principal)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, valid, got.Principal())
			assert.Equal(t, static.GetID(), got.GetID())
			assert.Equal(t, static.GetGrantTypes(), got.GetGrantTypes())
			assert.Equal(t, static.GetScopes(), got.GetScopes())
			assert.Equal(t, static.GetAudience(), got.GetAudience())
			assert.False(t, got.IsPublic())
		})
	}
}

func TestSPIFFEClient_GetAudience(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEClient(
		"spiffe-client",
		[]string{"urn:ietf:params:oauth:grant-type:token-exchange"},
		[]string{"openid"},
		nil,
		nil,
	)
	require.EqualError(t, err, "SPIFFE client scopes and audiences are required")
}
