// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

// SPIFFEAssociationRegistry is the immutable runtime index of validated SPIFFE
// associations. It selects policy only; it neither accepts nor authenticates a
// SPIFFE credential.
type SPIFFEAssociationRegistry struct {
	byPattern  map[string]SPIFFEClientAuthConfig
	byClientID map[string]SPIFFEClientAuthConfig
	clients    map[string]spiffeStaticClient
}

// NewSPIFFEAssociationRegistry creates an immutable lookup registry from a
// trust configuration. A nil trust configuration represents an absent SPIFFE
// configuration and returns nil without enabling SPIFFE clients.
//
// It rejects any two associations whose patterns could both match the same
// concrete SPIFFE ID (including an exact duplicate, which is the identical
// pattern matching itself) — associationForSPIFFEID resolves wildcard
// patterns by an unordered map scan, so an unrejected overlap would make
// resolution depend on random iteration order.
func NewSPIFFEAssociationRegistry(trust *SPIFFETrustConfig) (*SPIFFEAssociationRegistry, error) {
	if trust == nil {
		return nil, nil
	}

	associations := trust.Associations()
	registry := &SPIFFEAssociationRegistry{
		byPattern:  make(map[string]SPIFFEClientAuthConfig, len(associations)),
		byClientID: make(map[string]SPIFFEClientAuthConfig, len(associations)),
		clients:    make(map[string]spiffeStaticClient, len(associations)),
	}
	for _, association := range associations {
		if _, exists := registry.byClientID[association.ClientID()]; exists {
			return nil, fmt.Errorf("duplicate SPIFFE association client ID %q", association.ClientID())
		}
		for existingPattern := range registry.byPattern {
			if spiffePatternsOverlap(association.Principal(), existingPattern) {
				return nil, fmt.Errorf(
					"SPIFFE association pattern %q overlaps with existing pattern %q: resolution would be ambiguous",
					association.Principal(), existingPattern,
				)
			}
		}
		registry.byPattern[association.Principal()] = association.clone()
		registry.byClientID[association.ClientID()] = association.clone()

		policy := association.AuthorizationPolicy()
		client, err := registration.NewSPIFFEClient(
			association.ClientID(), policy.GrantTypes(), policy.Scopes(), policy.Resources(),
		)
		if err != nil {
			return nil, fmt.Errorf("SPIFFE client %q: %w", association.ClientID(), err)
		}
		registry.clients[association.ClientID()] = spiffeStaticClient{
			SPIFFEClient:        client,
			identityFingerprint: fingerprintSPIFFEAssociation(association),
		}
	}
	return registry, nil
}

// Resolve selects a configured association for a canonical concrete SPIFFE ID,
// requested OAuth client ID, and explicitly selected authentication method.
// When clientID is empty, the client ID is derived entirely from the
// resolved SPIFFE association (the "optional client ID" case); otherwise
// clientID must match the association's client ID. It fails closed for
// unknown IDs, mismatched client ownership, and disabled methods. This
// method does not validate a credential: the caller must have verified the
// SVID, because the returned client asserts that it was.
func (r *SPIFFEAssociationRegistry) Resolve(
	spiffeID, clientID string, method SPIFFEAuthenticationMethod,
) (*registration.AuthenticatedSPIFFEClient, error) {
	if r == nil {
		return nil, fmt.Errorf("no SPIFFE associations are configured")
	}
	parsedID, err := parseConcreteSPIFFEID(spiffeID)
	if err != nil {
		return nil, fmt.Errorf("invalid SPIFFE ID: %w", err)
	}
	canonicalID := parsedID.String()
	association, ok := r.associationForSPIFFEID(canonicalID)
	if !ok {
		return nil, fmt.Errorf("no SPIFFE association for ID %q", canonicalID)
	}
	if clientID != "" && association.ClientID() != clientID {
		return nil, fmt.Errorf("SPIFFE ID is not associated with client ID %q", clientID)
	}
	if !containsSPIFFEAuthenticationMethod(association.Methods(), method) {
		return nil, fmt.Errorf(
			"SPIFFE authentication method %q is not enabled for client ID %q", method, association.ClientID(),
		)
	}

	return registration.NewAuthenticatedSPIFFEClient(r.clients[association.ClientID()].SPIFFEClient, parsedID, method)
}

// usesX509 reports whether clientID names an association whose methods include
// spiffe_x509. It selects the client-authentication arm only; it does not
// authenticate anything.
func (r *SPIFFEAssociationRegistry) usesX509(clientID string) bool {
	if r == nil {
		return false
	}
	association, ok := r.byClientID[clientID]
	return ok && containsSPIFFEAuthenticationMethod(association.Methods(), SPIFFEAuthenticationMethodX509)
}

func (r *SPIFFEAssociationRegistry) associationForSPIFFEID(spiffeID string) (SPIFFEClientAuthConfig, bool) {
	if association, ok := r.byPattern[spiffeID]; ok {
		return association.clone(), true
	}
	for pattern, association := range r.byPattern {
		if strings.HasSuffix(pattern, "/*") && matchSPIFFEPrincipalPattern(pattern, spiffeID) {
			return association.clone(), true
		}
	}
	return SPIFFEClientAuthConfig{}, false
}

func containsSPIFFEAuthenticationMethod(methods []SPIFFEAuthenticationMethod, wanted SPIFFEAuthenticationMethod) bool {
	for _, method := range methods {
		if method == wanted {
			return true
		}
	}
	return false
}

// spiffeStaticClient embeds the concrete *registration.SPIFFEClient (not the
// fosite.Client interface) so that embedding still promotes Resources() and
// the BackChannelOnlyMarker -- an interface embedding would silently drop
// both, since neither is part of the fosite.Client method set.
type spiffeStaticClient struct {
	*registration.SPIFFEClient
	identityFingerprint string
}

func (c spiffeStaticClient) IdentityFingerprint() string { return c.identityFingerprint }

// staticClients returns the registry's clients as the storage overlay needs
// them, each carrying its association identity for durable placeholder
// reconciliation.
func (r *SPIFFEAssociationRegistry) staticClients() map[string]fosite.Client {
	if r == nil {
		return nil
	}
	clients := make(map[string]fosite.Client, len(r.clients))
	for clientID, client := range r.clients {
		clients[clientID] = client
	}
	return clients
}

// fingerprintSPIFFEAssociation hashes the SPIFFE association identity (trust
// domain reference, principal pattern, grant types, and accepted methods).
// Storage compares it only to set identity_changed in the replacement WARN
// when an edited association replaces its placeholder. Each value is
// length-prefixed with an unambiguous decimal-and-colon marker so
// concatenation can't alias two different value sequences to the same hash.
// Grant types and methods are each sorted before hashing so their original
// order never affects the result. The grant-type count is written before the
// grant types, since methods follow them with no count and the boundary would
// otherwise be ambiguous.
//
// Note: TrustDomainRef() hashes the trust-domain *reference name* (e.g.
// "production"), not the trust domain value itself. In practice this doesn't
// weaken the fingerprint because a principal pattern always embeds its trust
// domain, so a genuine trust-domain difference still changes the hash via the
// principal -- but repointing a trust domain ref's bundle source to a
// different CA while keeping the same ref name and trust domain would not be
// detected by this fingerprint alone.
func fingerprintSPIFFEAssociation(association SPIFFEClientAuthConfig) string {
	hash := sha256.New()
	write := func(value string) {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
	}
	write(association.TrustDomainRef())
	write(association.Principal())
	grantTypes := association.AuthorizationPolicy().GrantTypes()
	slices.Sort(grantTypes)
	write(strconv.Itoa(len(grantTypes)))
	for _, grantType := range grantTypes {
		write(grantType)
	}
	methods := association.Methods()
	slices.Sort(methods)
	for _, method := range methods {
		write(string(method))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
