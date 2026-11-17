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

func TestNewSPIFFEClient(t *testing.T) {
	t.Parallel()

	scopes := []string{"openid"}
	resources := []string{"https://resource.example.com"}
	grants := []string{oauthproto.GrantTypeTokenExchange}
	client, err := NewSPIFFEClient("spiffe-client", grants, scopes, resources)
	require.NoError(t, err)

	grants[0] = "changed"
	scopes[0] = "changed"
	resources[0] = "changed"

	assert.Equal(t, "spiffe-client", client.GetID())
	assert.Nil(t, client.GetHashedSecret())
	assert.Nil(t, client.GetRedirectURIs())
	assert.Nil(t, client.GetResponseTypes())
	assert.False(t, client.IsPublic())
	assert.Equal(t, []string{oauthproto.GrantTypeTokenExchange}, []string(client.GetGrantTypes()))
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Nil(t, client.GetAudience())
	assert.Equal(t, []string{"https://resource.example.com"}, client.Resources())

	client.GetGrantTypes()[0] = "mutated"
	client.GetScopes()[0] = "mutated"
	client.Resources()[0] = "mutated"
	assert.Equal(t, []string{oauthproto.GrantTypeTokenExchange}, []string(client.GetGrantTypes()))
	assert.Equal(t, "openid", client.GetScopes()[0])
	assert.Equal(t, []string{"https://resource.example.com"}, client.Resources())

	_, err = NewSPIFFEClient("disabled-exchange", []string{oauthproto.GrantTypeTokenExchange}, nil, nil)
	require.Error(t, err)
}

func TestNewSPIFFEClient_Validation(t *testing.T) {
	t.Parallel()

	const tokenExchange = oauthproto.GrantTypeTokenExchange
	tests := []struct {
		name    string
		id      string
		grants  []string
		scopes  []string
		wantErr string
	}{
		{name: "missing ID", wantErr: "SPIFFE client ID is required"},
		{
			name: "missing grant types", id: "spiffe-client", scopes: []string{"openid"},
			wantErr: "SPIFFE client grant types are required",
		},
		{
			name: "missing scopes", id: "spiffe-client", grants: []string{tokenExchange},
			wantErr: "SPIFFE client scopes are required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := NewSPIFFEClient(tt.id, tt.grants, tt.scopes, nil)
			require.EqualError(t, err, tt.wantErr)
			assert.Nil(t, client)
		})
	}
}

func TestNewAuthenticatedSPIFFEClient(t *testing.T) {
	t.Parallel()

	static, err := NewSPIFFEClient("client", []string{oauthproto.GrantTypeTokenExchange},
		[]string{"openid"}, []string{"https://resource.example.com"})
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
			assert.Equal(t, []string{"https://resource.example.com"}, got.Resources())
			assert.Nil(t, got.GetAudience())
			assert.False(t, got.IsPublic())
			assert.True(t, BackChannelOnly(got))
		})
	}
}
