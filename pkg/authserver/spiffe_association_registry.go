// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

// SPIFFEAssociationRegistry is the immutable runtime index of validated SPIFFE
// associations. It selects policy only; it neither accepts nor authenticates a
// SPIFFE credential.
type SPIFFEAssociationRegistry struct {
	byPattern  map[string]SPIFFEClientAuthConfig
	byClientID map[string]SPIFFEClientAuthConfig
}

// NewSPIFFEAssociationRegistry creates an immutable lookup registry from a
// trust configuration. A nil trust configuration represents an absent SPIFFE
// configuration and returns nil without enabling SPIFFE clients.
func NewSPIFFEAssociationRegistry(trust *SPIFFETrustConfig) (*SPIFFEAssociationRegistry, error) {
	if trust == nil {
		return nil, nil
	}

	associations := trust.Associations()
	registry := &SPIFFEAssociationRegistry{
		byPattern:  make(map[string]SPIFFEClientAuthConfig, len(associations)),
		byClientID: make(map[string]SPIFFEClientAuthConfig, len(associations)),
	}
	for _, association := range associations {
		if _, exists := registry.byPattern[association.Principal()]; exists {
			return nil, fmt.Errorf("duplicate SPIFFE association pattern %q", association.Principal())
		}
		if _, exists := registry.byClientID[association.ClientID()]; exists {
			return nil, fmt.Errorf("duplicate SPIFFE association client ID %q", association.ClientID())
		}
		registry.byPattern[association.Principal()] = association.clone()
		registry.byClientID[association.ClientID()] = association.clone()
	}
	return registry, nil
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

// staticClients builds immutable OAuth clients and their association identity for
// durable placeholder reconciliation.
func (r *SPIFFEAssociationRegistry) staticClients() (map[string]fosite.Client, error) {
	if r == nil {
		return nil, nil
	}
	clients := make(map[string]fosite.Client, len(r.byClientID))
	for clientID, association := range r.byClientID {
		policy := association.AuthorizationPolicy()
		client, err := registration.NewSPIFFEClient(
			association.ClientID(), policy.Scopes(), policy.Audiences(), policy.Resources(),
		)
		if err != nil {
			return nil, fmt.Errorf("SPIFFE client %q: %w", clientID, err)
		}
		clients[clientID] = spiffeStaticClient{
			SPIFFEClient:        client,
			identityFingerprint: fingerprintSPIFFEAssociation(association),
		}
	}
	return clients, nil
}

// fingerprintSPIFFEAssociation hashes the SPIFFE association identity (trust
// domain reference, principal pattern, and accepted methods) so two
// associations with a differing identity durably reconcile as different
// clients rather than being silently accepted as the same one. Each value is
// length-prefixed with an unambiguous decimal-and-colon marker so
// concatenation can't alias two different value sequences to the same hash,
// and methods are sorted first so their original order never affects the
// result.
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
	methods := association.Methods()
	slices.Sort(methods)
	for _, method := range methods {
		write(string(method))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
