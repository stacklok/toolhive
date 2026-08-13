// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"fmt"
	"strings"

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
// validated trust configuration. A nil trust configuration represents an
// absent SPIFFE configuration and returns nil without enabling SPIFFE clients.
func NewSPIFFEAssociationRegistry(trust *SPIFFETrustConfig) (*SPIFFEAssociationRegistry, error) {
	if trust == nil {
		return nil, nil
	}
	if !trust.validated {
		return nil, fmt.Errorf("SPIFFE trust config must be constructed with NewSPIFFETrustConfig")
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

// Resolve selects a configured association for a canonical concrete SPIFFE ID,
// requested OAuth client ID, and explicitly selected authentication method.
// It fails closed for unknown IDs, mismatched client ownership, and disabled
// methods. This method does not validate a credential.
func (r *SPIFFEAssociationRegistry) Resolve(
	spiffeID, clientID string, method SPIFFEAuthenticationMethod,
) (NormalizedSPIFFEPrincipal, error) {
	if r == nil {
		return NormalizedSPIFFEPrincipal{}, fmt.Errorf("no SPIFFE associations are configured")
	}
	canonicalID, err := NormalizeSPIFFEPrincipal(spiffeID)
	if err != nil {
		return NormalizedSPIFFEPrincipal{}, fmt.Errorf("invalid SPIFFE ID: %w", err)
	}
	parsedID, err := parseSPIFFEID(canonicalID)
	if err != nil {
		return NormalizedSPIFFEPrincipal{}, fmt.Errorf("invalid SPIFFE ID: %w", err)
	}
	association, ok := r.associationForSPIFFEID(canonicalID)
	if !ok {
		return NormalizedSPIFFEPrincipal{}, fmt.Errorf("no SPIFFE association for ID %q", canonicalID)
	}
	clientAssociation, ok := r.byClientID[clientID]
	if !ok {
		return NormalizedSPIFFEPrincipal{}, fmt.Errorf("no SPIFFE association for client ID %q", clientID)
	}
	if clientAssociation.ClientID() != association.ClientID() {
		return NormalizedSPIFFEPrincipal{}, fmt.Errorf("SPIFFE ID is not associated with client ID %q", clientID)
	}
	if !containsSPIFFEAuthenticationMethod(association.Methods(), method) {
		return NormalizedSPIFFEPrincipal{}, fmt.Errorf(
			"SPIFFE authentication method %q is not enabled for client ID %q", method, clientID,
		)
	}

	return NormalizedSPIFFEPrincipal{
		clientID:      association.ClientID(),
		spiffeID:      canonicalID,
		trustDomain:   parsedID.TrustDomain().String(),
		authMethod:    method,
		authorization: association.AuthorizationPolicy(),
	}, nil
}

// staticClient returns the configured immutable OAuth client for clientID.
func (r *SPIFFEAssociationRegistry) staticClient(clientID string) (*registration.SPIFFEClient, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	association, ok := r.byClientID[clientID]
	if !ok {
		return nil, false, nil
	}
	policy := association.AuthorizationPolicy()
	client, err := registration.NewSPIFFEClient(
		association.ClientID(),
		policy.GrantTypes(),
		policy.Scopes(),
		policy.Resources(),
		policy.Audiences(),
		policy.TokenExchangeEnabled(),
	)
	if err != nil {
		return nil, false, fmt.Errorf("SPIFFE client %q: %w", clientID, err)
	}
	return client, true, nil
}

func (r *SPIFFEAssociationRegistry) clientIDs() []string {
	if r == nil {
		return nil
	}
	clientIDs := make([]string, 0, len(r.byClientID))
	for clientID := range r.byClientID {
		clientIDs = append(clientIDs, clientID)
	}
	return clientIDs
}

func (r *SPIFFEAssociationRegistry) associationForSPIFFEID(spiffeID string) (SPIFFEClientAuthConfig, bool) {
	if association, ok := r.byPattern[spiffeID]; ok {
		return association.clone(), true
	}
	for pattern, association := range r.byPattern {
		if strings.HasSuffix(pattern, "/*") && MatchSPIFFEPrincipalPattern(pattern, spiffeID) {
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
