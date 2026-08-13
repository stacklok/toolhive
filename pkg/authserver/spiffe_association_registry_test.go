// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSPIFFEAssociationRegistryResolve(t *testing.T) {
	t.Parallel()

	registry := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{
		{
			TrustDomainRef: "production",
			Principal:      "spiffe://example.org/ns/default/exact",
			ClientID:       "exact-client",
			Methods:        []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
			GrantTypes:     []string{SPIFFEGrantTypeTokenExchange},
			Scopes:         []string{"openid"},
			Resources:      []string{"https://resource.example.com"},
			Audiences:      []string{"https://audience.example.com"},
			TokenExchange:  &SPIFFETokenExchangeRunConfig{Enabled: true},
		},
		{
			TrustDomainRef: "production",
			Principal:      "spiffe://example.org/ns/default/agents/*",
			ClientID:       "wildcard-client",
			Methods:        []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
			GrantTypes:     []string{SPIFFEGrantTypeTokenExchange},
			Scopes:         []string{"openid"},
			Resources:      []string{"https://resource.example.com"},
			Audiences:      []string{"https://audience.example.com"},
			TokenExchange:  &SPIFFETokenExchangeRunConfig{Enabled: true},
		},
	})

	tests := []struct {
		name     string
		spiffeID string
		clientID string
		method   SPIFFEAuthenticationMethod
		wantErr  string
	}{
		{name: "exact x509", spiffeID: "spiffe://example.org/ns/default/exact", clientID: "exact-client", method: SPIFFEAuthenticationMethodX509},
		{name: "exact jwt", spiffeID: "spiffe://example.org/ns/default/exact", clientID: "exact-client", method: SPIFFEAuthenticationMethodJWT},
		{name: "wildcard x509", spiffeID: "spiffe://example.org/ns/default/agents/one", clientID: "wildcard-client", method: SPIFFEAuthenticationMethodX509},
		{name: "wildcard jwt", spiffeID: "spiffe://example.org/ns/default/agents/one", clientID: "wildcard-client", method: SPIFFEAuthenticationMethodJWT},
		{name: "client belongs to another principal", spiffeID: "spiffe://example.org/ns/default/exact", clientID: "wildcard-client", method: SPIFFEAuthenticationMethodX509, wantErr: "not associated"},
		{name: "wrong trust domain", spiffeID: "spiffe://other.example/ns/default/exact", clientID: "exact-client", method: SPIFFEAuthenticationMethodX509, wantErr: "no SPIFFE association"},
		{name: "missing association", spiffeID: "spiffe://example.org/ns/default/exact", clientID: "missing-client", method: SPIFFEAuthenticationMethodX509, wantErr: "no SPIFFE association"},
		{name: "disabled method", spiffeID: "spiffe://example.org/ns/default/exact", clientID: "exact-client", method: SPIFFEAuthenticationMethod("unknown"), wantErr: "not enabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			principal, err := registry.Resolve(tt.spiffeID, tt.clientID, tt.method)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.clientID, principal.ClientID())
			assert.Equal(t, tt.spiffeID, principal.SPIFFEID())
			assert.Equal(t, "example.org", principal.TrustDomain())
			assert.Equal(t, tt.method, principal.AuthenticationMethod())
			assert.Equal(t, []string{SPIFFEGrantTypeTokenExchange}, principal.AuthorizationPolicy().GrantTypes())
			assert.Equal(t, []string{"https://resource.example.com"}, principal.AuthorizationPolicy().Resources())
			assert.Equal(t, []string{"https://audience.example.com"}, principal.AuthorizationPolicy().Audiences())
		})
	}
}

func TestSPIFFEAssociationRegistryRebuildsChangedAndRemovedAuthority(t *testing.T) {
	t.Parallel()

	initial := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{testSPIFFEAssociation("client", "openid")})
	changed := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{testSPIFFEAssociation("client", "profile")})
	removedTrust, err := NewSPIFFETrustConfig(nil, nil, []string{"openid", "profile"}, []string{"https://resource.example.com"})
	require.NoError(t, err)
	require.Nil(t, removedTrust)
	removed, err := NewSPIFFEAssociationRegistry(removedTrust)
	require.NoError(t, err)

	initialPrincipal, err := initial.Resolve("spiffe://example.org/ns/default/agent", "client", SPIFFEAuthenticationMethodX509)
	require.NoError(t, err)
	assert.Equal(t, []string{"openid"}, initialPrincipal.AuthorizationPolicy().Scopes())

	changedPrincipal, err := changed.Resolve("spiffe://example.org/ns/default/agent", "client", SPIFFEAuthenticationMethodX509)
	require.NoError(t, err)
	assert.Equal(t, []string{"profile"}, changedPrincipal.AuthorizationPolicy().Scopes())

	_, err = removed.Resolve("spiffe://example.org/ns/default/agent", "client", SPIFFEAuthenticationMethodX509)
	require.Error(t, err)
	assert.Empty(t, removed.clientIDs())
	client, found, err := removed.staticClient("client")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, client)
}

func newTestSPIFFEAssociationRegistry(t *testing.T, associations []SPIFFEClientAuthRunConfig) *SPIFFEAssociationRegistry {
	t.Helper()

	trust, err := NewSPIFFETrustConfig([]SPIFFETrustDomainRunConfig{{
		Name:        "production",
		TrustDomain: "example.org",
		Methods:     []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
		BundleSource: SPIFFEBundleSourceRunConfig{
			Type:        SPIFFEBundleSourceTypeWorkloadAPI,
			WorkloadAPI: &SPIFFEWorkloadAPIBundleSourceRunConfig{},
		},
	}}, &InboundGrantsRunConfig{SPIFFEClientAuth: associations}, []string{"openid", "profile"},
		[]string{"https://resource.example.com", "https://audience.example.com"})
	require.NoError(t, err)
	registry, err := NewSPIFFEAssociationRegistry(trust)
	require.NoError(t, err)
	return registry
}

func testSPIFFEAssociation(clientID, scope string) SPIFFEClientAuthRunConfig {
	return SPIFFEClientAuthRunConfig{
		TrustDomainRef: "production",
		Principal:      "spiffe://example.org/ns/default/agent",
		ClientID:       clientID,
		Methods:        []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
		GrantTypes:     []string{SPIFFEGrantTypeTokenExchange},
		Scopes:         []string{scope},
		Resources:      []string{"https://resource.example.com"},
		Audiences:      []string{"https://audience.example.com"},
		TokenExchange:  &SPIFFETokenExchangeRunConfig{Enabled: true},
	}
}
