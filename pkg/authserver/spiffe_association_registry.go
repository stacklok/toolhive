// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"fmt"

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

// staticClients builds immutable OAuth clients for all configured associations.
func (r *SPIFFEAssociationRegistry) staticClients() (map[string]fosite.Client, error) {
	if r == nil {
		return nil, nil
	}
	clients := make(map[string]fosite.Client, len(r.byClientID))
	for clientID, association := range r.byClientID {
		policy := association.AuthorizationPolicy()
		client, err := registration.NewSPIFFEClient(
			association.ClientID(), policy.Scopes(), policy.Audiences(),
		)
		if err != nil {
			return nil, fmt.Errorf("SPIFFE client %q: %w", clientID, err)
		}
		clients[clientID] = client
	}
	return clients, nil
}
