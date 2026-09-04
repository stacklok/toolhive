// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
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

// TestSPIFFEAssociationRegistryStaticClientPreservesConcreteCapabilities is the
// regression test for the review finding on PR #6473: wrapping the registry's
// runtime client to carry an identity fingerprint must not lose capabilities
// the concrete *registration.SPIFFEClient exposes beyond fosite.Client's
// method set -- Resources() (independent RFC 8707 resource enforcement) and
// the BackChannelOnlyMarker (relied on by the authorize handler). Embedding
// the fosite.Client interface instead of the concrete type would silently
// drop both while still compiling.
func TestSPIFFEAssociationRegistryStaticClientPreservesConcreteCapabilities(t *testing.T) {
	t.Parallel()

	association := testSPIFFEAssociation("client", "openid")
	association.Resources = []string{"https://resource.example.com"}

	registry := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{association})
	clients, err := registry.staticClients()
	require.NoError(t, err)

	client, found := clients["client"]
	require.True(t, found)

	resourceScoped, ok := client.(interface{ Resources() []string })
	require.True(t, ok, "wrapped SPIFFE client must still expose a Resources() accessor")
	assert.Equal(t, []string{"https://resource.example.com"}, resourceScoped.Resources())

	assert.True(t, registration.BackChannelOnly(client),
		"wrapped SPIFFE client must still carry the explicit back-channel-only marker")
}

// TestSPIFFEAssociationFingerprintOrderStable proves two associations that
// are identical except for the order their configured methods are listed in
// produce the SAME identity fingerprint. fingerprintSPIFFEAssociation sorts
// methods before hashing specifically so config-file reordering (which
// carries no semantic meaning) can never manifest as a false collision on
// reconciliation.
func TestSPIFFEAssociationFingerprintOrderStable(t *testing.T) {
	t.Parallel()

	forward := testSPIFFEAssociation("client", "openid")
	forward.Methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT}
	reversed := testSPIFFEAssociation("client", "openid")
	reversed.Methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT, SPIFFEAuthenticationMethodX509}

	forwardClients, err := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{forward}).staticClients()
	require.NoError(t, err)
	reversedClients, err := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{reversed}).staticClients()
	require.NoError(t, err)

	forwardIdentity, ok := forwardClients["client"].(interface{ IdentityFingerprint() string })
	require.True(t, ok)
	reversedIdentity, ok := reversedClients["client"].(interface{ IdentityFingerprint() string })
	require.True(t, ok)

	assert.Equal(t, forwardIdentity.IdentityFingerprint(), reversedIdentity.IdentityFingerprint(),
		"method order must not affect the identity fingerprint")
}

// TestSPIFFEAssociationFingerprintDiffersOnMethodsOnly proves two associations
// identical in every other respect but differing in their accepted methods
// set produce DIFFERENT identity fingerprints -- the review finding on PR
// #6473 was precisely that the fingerprint ignored methods (and the rest of
// the association identity) entirely.
func TestSPIFFEAssociationFingerprintDiffersOnMethodsOnly(t *testing.T) {
	t.Parallel()

	x509Only := testSPIFFEAssociation("client", "openid")
	x509Only.Methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509}
	jwtOnly := testSPIFFEAssociation("client", "openid")
	jwtOnly.Methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}

	x509Clients, err := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{x509Only}).staticClients()
	require.NoError(t, err)
	jwtClients, err := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{jwtOnly}).staticClients()
	require.NoError(t, err)

	x509Identity, ok := x509Clients["client"].(interface{ IdentityFingerprint() string })
	require.True(t, ok)
	jwtIdentity, ok := jwtClients["client"].(interface{ IdentityFingerprint() string })
	require.True(t, ok)

	assert.NotEqual(t, x509Identity.IdentityFingerprint(), jwtIdentity.IdentityFingerprint(),
		"a genuinely different methods set must produce a different identity fingerprint")
}

// TestSPIFFEAssociationFingerprintFieldsDoNotConcatenateAmbiguously pins the
// length-prefixing invariant fingerprintSPIFFEAssociation's doc comment
// asserts but nothing otherwise enforces: without it, two different
// (trustDomainRef, principal) pairs whose concatenation is byte-identical
// -- e.g. ("12", "3") and ("1", "23") -- would hash to the same value,
// silently reconciling two different associations as one. Calls
// fingerprintSPIFFEAssociation directly (unexported, same-package test) since
// the collision is a property of that function's own encoding, not of
// anything reachable through the public registry API.
func TestSPIFFEAssociationFingerprintFieldsDoNotConcatenateAmbiguously(t *testing.T) {
	t.Parallel()

	a := fingerprintSPIFFEAssociation(SPIFFEClientAuthConfig{trustDomainRef: "12", principal: "3"})
	b := fingerprintSPIFFEAssociation(SPIFFEClientAuthConfig{trustDomainRef: "1", principal: "23"})
	assert.NotEqual(t, a, b, "length prefixing must keep field boundaries unambiguous")
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
