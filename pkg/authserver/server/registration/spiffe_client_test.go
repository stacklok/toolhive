// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

var tokenExchangeGrants = []string{oauthproto.GrantTypeTokenExchange}

func TestNewSPIFFEClient(t *testing.T) {
	t.Parallel()

	scopes := []string{"openid"}
	audiences := []string{"https://api.example.com"}
	resources := []string{"https://resource.example.com"}
	grants := []string{oauthproto.GrantTypeTokenExchange}
	client, err := NewSPIFFEClient("spiffe-client", grants, scopes, audiences, resources)
	require.NoError(t, err)

	grants[0] = "changed"
	scopes[0] = "changed"
	audiences[0] = "changed"
	resources[0] = "changed"

	assert.Equal(t, "spiffe-client", client.GetID())
	assert.Nil(t, client.GetHashedSecret())
	assert.Nil(t, client.GetRedirectURIs())
	assert.Nil(t, client.GetResponseTypes())
	assert.False(t, client.IsPublic())
	assert.Equal(t, []string{oauthproto.GrantTypeTokenExchange}, []string(client.GetGrantTypes()))
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, []string{"https://api.example.com"}, client.Audiences())
	assert.Equal(t, []string{"https://api.example.com"}, []string(client.GetAudience()))
	assert.Equal(t, []string{"https://resource.example.com"}, client.Resources())

	client.GetGrantTypes()[0] = "mutated"
	client.GetScopes()[0] = "mutated"
	client.Audiences()[0] = "mutated"
	client.GetAudience()[0] = "mutated"
	client.Resources()[0] = "mutated"
	assert.Equal(t, []string{oauthproto.GrantTypeTokenExchange}, []string(client.GetGrantTypes()))
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, []string{"https://api.example.com"}, client.Audiences())
	assert.Equal(t, []string{"https://api.example.com"}, []string(client.GetAudience()))
	assert.Equal(t, []string{"https://resource.example.com"}, client.Resources())

	_, err = NewSPIFFEClient("spiffe-client", tokenExchangeGrants, nil, nil, nil)
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
		tokenExchangeGrants,
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

func TestNewSPIFFEClient_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		id        string
		grants    []string
		scopes    []string
		audiences []string
		wantErr   string
	}{
		{
			name: "missing ID", grants: tokenExchangeGrants,
			wantErr: "SPIFFE client ID is required",
		},
		{
			name: "missing grant types", id: "spiffe-client",
			scopes: []string{"openid"}, audiences: []string{"https://api.example.com"},
			wantErr: "SPIFFE client grant types are required",
		},
		{
			name: "missing audiences", id: "spiffe-client", grants: tokenExchangeGrants,
			scopes:  []string{"openid"},
			wantErr: "SPIFFE client scopes and audiences are required",
		},
		{
			name: "missing scopes", id: "spiffe-client", grants: tokenExchangeGrants,
			audiences: []string{"https://api.example.com"},
			wantErr:   "SPIFFE client scopes and audiences are required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := NewSPIFFEClient(tt.id, tt.grants, tt.scopes, tt.audiences, nil)
			require.EqualError(t, err, tt.wantErr)
			assert.Nil(t, client)
		})
	}
}

func TestNewAuthenticatedSPIFFEClient(t *testing.T) {
	t.Parallel()

	static, err := NewSPIFFEClient("client", tokenExchangeGrants,
		[]string{"openid"}, []string{"https://api.example.com"}, []string{"https://resource.example.com"})
	require.NoError(t, err)
	id := spiffeid.RequireFromString("spiffe://example.org/workload/concrete")

	tests := []struct {
		name    string
		client  *SPIFFEClient
		id      spiffeid.ID
		method  spiffeauth.SPIFFEAuthenticationMethod
		wantErr string
	}{
		{name: "JWT", client: static, id: id, method: spiffeauth.SPIFFEAuthenticationMethodJWT},
		{name: "X.509", client: static, id: id, method: spiffeauth.SPIFFEAuthenticationMethodX509},
		{name: "nil client", id: id, method: spiffeauth.SPIFFEAuthenticationMethodJWT, wantErr: "SPIFFE client is required"},
		{name: "zero ID", client: static, method: spiffeauth.SPIFFEAuthenticationMethodJWT, wantErr: "SPIFFE ID is required"},
		{name: "unknown method", client: static, id: id, method: "unknown", wantErr: "unknown SPIFFE authentication method"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewAuthenticatedSPIFFEClient(tt.client, tt.id, tt.method)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, id, got.SPIFFEID())
			assert.Equal(t, tt.method, got.AuthenticationMethod())
			assert.Equal(t, static.GetID(), got.GetID())
			assert.Equal(t, static.GetGrantTypes(), got.GetGrantTypes())
			assert.Equal(t, static.GetScopes(), got.GetScopes())
			assert.Equal(t, static.GetAudience(), got.GetAudience())
			assert.Equal(t, []string{"https://resource.example.com"}, got.Resources())
			assert.False(t, got.IsPublic())
			assert.True(t, BackChannelOnly(got))
		})
	}
}
