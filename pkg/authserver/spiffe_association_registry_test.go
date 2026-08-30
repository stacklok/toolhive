// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSPIFFEAssociationRegistryRebuildsChangedAndRemovedAuthority(t *testing.T) {
	t.Parallel()

	initial := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{testSPIFFEAssociation("client", "openid")})
	changed := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{testSPIFFEAssociation("client", "profile")})
	removedTrust, err := NewSPIFFETrustConfig(nil, nil, []string{"openid", "profile"}, []string{"https://resource.example.com"})
	require.NoError(t, err)
	assert.Empty(t, removedTrust.Associations())
	removed, err := NewSPIFFEAssociationRegistry(removedTrust)
	require.NoError(t, err)

	initialClients, err := initial.staticClients()
	require.NoError(t, err)
	initialClient, found := initialClients["client"]
	require.True(t, found)
	assert.Equal(t, []string{"openid"}, []string(initialClient.GetScopes()))

	changedClients, err := changed.staticClients()
	require.NoError(t, err)
	changedClient, found := changedClients["client"]
	require.True(t, found)
	assert.Equal(t, []string{"profile"}, []string(changedClient.GetScopes()))

	removedClients, err := removed.staticClients()
	require.NoError(t, err)
	assert.Empty(t, removedClients)
}

func TestSPIFFEAssociationRegistryKeepsClientAudiencesIsolated(t *testing.T) {
	t.Parallel()

	first := testSPIFFEAssociation("first-client", "openid")
	first.Audiences = []string{"https://resource.example.com"}
	second := testSPIFFEAssociation("second-client", "profile")
	second.PrincipalPattern = "spiffe://example.org/ns/default/other-agent"
	second.Audiences = []string{"https://audience.example.com"}

	registry := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{first, second})
	clients, err := registry.staticClients()
	require.NoError(t, err)

	assert.Equal(t, []string{"https://resource.example.com"}, []string(clients["first-client"].GetAudience()))
	assert.Equal(t, []string{"https://audience.example.com"}, []string(clients["second-client"].GetAudience()))
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
		TrustDomainRef:   "production",
		PrincipalPattern: "spiffe://example.org/ns/default/agent",
		ClientID:         clientID,
		Methods:          []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
		Scopes:           []string{scope},
		Audiences:        []string{"https://audience.example.com"},
		GrantTypes:       []string{SPIFFEGrantTypeTokenExchange},
	}
}
