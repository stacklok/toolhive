// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

func TestSPIFFEAssociationRegistryResolve(t *testing.T) {
	t.Parallel()

	exact := testSPIFFEAssociation("exact-client", "openid")
	wildcard := testSPIFFEAssociation("wildcard-client", "profile")
	wildcard.PrincipalPattern = "spiffe://example.org/ns/workloads/*"
	other := testSPIFFEAssociation("other-client", "profile")
	other.PrincipalPattern = "spiffe://example.org/ns/other/agent"
	registry := newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{exact, wildcard, other})

	tests := []struct {
		name         string
		registry     *SPIFFEAssociationRegistry
		spiffeID     string
		clientID     string
		method       SPIFFEAuthenticationMethod
		wantClientID string
		wantScope    string
		wantErr      string
	}{
		{
			name: "exact identity", registry: registry,
			spiffeID: "spiffe://example.org/ns/default/agent", clientID: "exact-client",
			method: SPIFFEAuthenticationMethodX509, wantScope: "openid",
		},
		{
			name: "empty client ID derives from association", registry: registry,
			spiffeID: "spiffe://example.org/ns/default/agent", clientID: "",
			method: SPIFFEAuthenticationMethodX509, wantScope: "openid", wantClientID: "exact-client",
		},
		{
			name: "wildcard identity", registry: registry,
			spiffeID: "spiffe://example.org/ns/workloads/agent", clientID: "wildcard-client",
			method: SPIFFEAuthenticationMethodX509, wantScope: "profile",
		},
		{
			name: "nil registry", registry: nil,
			spiffeID: "spiffe://example.org/ns/default/agent", clientID: "exact-client",
			method: SPIFFEAuthenticationMethodX509, wantErr: "no SPIFFE associations",
		},
		{
			name: "malformed identity", registry: registry,
			spiffeID: "not-a-spiffe-id", clientID: "exact-client",
			method: SPIFFEAuthenticationMethodX509, wantErr: "invalid SPIFFE ID",
		},
		{
			name: "unknown identity", registry: registry,
			spiffeID: "spiffe://example.org/ns/missing/agent", clientID: "exact-client",
			method: SPIFFEAuthenticationMethodX509, wantErr: "no SPIFFE association for ID",
		},
		{
			name: "identity client mismatch", registry: registry,
			spiffeID: "spiffe://example.org/ns/default/agent", clientID: "other-client",
			method: SPIFFEAuthenticationMethodX509, wantErr: "not associated with client ID",
		},
		{
			name: "unknown client", registry: registry,
			spiffeID: "spiffe://example.org/ns/default/agent", clientID: "missing-client",
			method: SPIFFEAuthenticationMethodX509, wantErr: "not associated with client ID",
		},
		{
			name: "disabled method", registry: registry,
			spiffeID: "spiffe://example.org/ns/default/agent", clientID: "exact-client",
			method: SPIFFEAuthenticationMethodJWT, wantErr: "is not enabled",
		},
		{
			name: "unknown method", registry: registry,
			spiffeID: "spiffe://example.org/ns/default/agent", clientID: "exact-client",
			method: SPIFFEAuthenticationMethod("unknown"), wantErr: "is not enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			principal, err := tt.registry.Resolve(tt.spiffeID, tt.clientID, tt.method)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, NormalizedSPIFFEPrincipal{}, principal)
				return
			}
			require.NoError(t, err)
			wantClientID := tt.wantClientID
			if wantClientID == "" {
				wantClientID = tt.clientID
			}
			assert.Equal(t, wantClientID, principal.ClientID())
			assert.Equal(t, tt.spiffeID, principal.SPIFFEID())
			assert.Equal(t, "example.org", principal.TrustDomain())
			assert.Equal(t, tt.method, principal.AuthenticationMethod())
			assert.Equal(t, []string{tt.wantScope}, principal.AuthorizationPolicy().Scopes())
		})
	}
}

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

// TestNewSPIFFEAssociationRegistryRejectsOverlappingPatterns proves
// NewSPIFFEAssociationRegistry itself fails closed when two associations'
// patterns could both match the same concrete SPIFFE ID, since resolution at
// runtime would otherwise depend on random map iteration order. It builds the
// SPIFFETrustConfig directly (bypassing NewSPIFFETrustConfig's own upstream
// overlap validation) so the registry constructor's defense is exercised on
// its own, as it must be for any other in-package caller that builds a
// SPIFFETrustConfig without going through that validation.
func TestNewSPIFFEAssociationRegistryRejectsOverlappingPatterns(t *testing.T) {
	t.Parallel()

	wildcard := testNormalizedSPIFFEAssociation("wildcard-client", "spiffe://example.org/ns/workloads/*")
	narrowerWildcard := testNormalizedSPIFFEAssociation("narrower-client", "spiffe://example.org/ns/workloads/agent/*")
	exactUnderWildcard := testNormalizedSPIFFEAssociation("exact-client", "spiffe://example.org/ns/workloads/agent")
	other := testNormalizedSPIFFEAssociation("other-client", "spiffe://example.org/ns/other/*")

	tests := []struct {
		name         string
		associations []SPIFFEClientAuthConfig
		wantErr      string
	}{
		{
			name:         "overlapping wildcards",
			associations: []SPIFFEClientAuthConfig{wildcard, narrowerWildcard},
			wantErr:      "overlaps with existing pattern",
		},
		{
			name:         "exact pattern added after covering wildcard",
			associations: []SPIFFEClientAuthConfig{wildcard, exactUnderWildcard},
			wantErr:      "overlaps with existing pattern",
		},
		{
			name:         "exact pattern added before covering wildcard",
			associations: []SPIFFEClientAuthConfig{exactUnderWildcard, wildcard},
			wantErr:      "overlaps with existing pattern",
		},
		{
			name:         "non-overlapping wildcards succeed",
			associations: []SPIFFEClientAuthConfig{wildcard, other},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			trust := &SPIFFETrustConfig{associations: tt.associations}
			registry, err := NewSPIFFEAssociationRegistry(trust)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, registry)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, registry)
		})
	}
}

func testNormalizedSPIFFEAssociation(clientID, principal string) SPIFFEClientAuthConfig {
	return SPIFFEClientAuthConfig{
		trustDomainRef: "production",
		principal:      principal,
		clientID:       clientID,
		methods:        []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
		authorization:  SPIFFEAuthorizationPolicy{scopes: []string{"openid"}},
	}
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
