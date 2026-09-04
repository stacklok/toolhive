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
	first.Audiences = []string{"https://first-audience.example.com"}
	second := testSPIFFEAssociation("second-client", "profile")
	second.PrincipalPattern = "spiffe://example.org/ns/default/other-agent"
	second.Audiences = []string{"https://second-audience.example.com"}

	registry := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{first, second})
	clients, err := registry.staticClients()
	require.NoError(t, err)

	assert.Equal(t, []string{"https://first-audience.example.com"}, []string(clients["first-client"].GetAudience()))
	assert.Equal(t, []string{"https://second-audience.example.com"}, []string(clients["second-client"].GetAudience()))
}

// TestSPIFFEAssociationRegistryKeepsResourcesIndependentOfAudiences proves the
// association registry wires an association's RFC 8707 resources through to
// the runtime client as a dimension independent of RFC 8693 audiences: a
// client configured with disjoint audiences and resources must expose each
// list separately, not have one silently discarded or aliased to the other.
func TestSPIFFEAssociationRegistryKeepsResourcesIndependentOfAudiences(t *testing.T) {
	t.Parallel()

	association := testSPIFFEAssociation("client", "openid")
	association.Audiences = []string{"https://audience.example.com"}
	association.Resources = []string{"https://resource.example.com"}

	registry := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{association})
	clients, err := registry.staticClients()
	require.NoError(t, err)

	client, found := clients["client"]
	require.True(t, found)

	resourceScoped, ok := client.(interface{ Resources() []string })
	require.True(t, ok, "SPIFFE client must expose a Resources() accessor")

	assert.Equal(t, []string{"https://audience.example.com"}, []string(client.GetAudience()))
	assert.Equal(t, []string{"https://resource.example.com"}, resourceScoped.Resources())
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
