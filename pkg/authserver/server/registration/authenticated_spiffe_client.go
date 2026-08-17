// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"

	"github.com/ory/fosite"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

// AuthenticatedSPIFFEClient binds a static SPIFFE client to the exact principal
// resolved after a workload credential was verified. It keeps static client
// lookup distinct from authenticated workload provenance.
type AuthenticatedSPIFFEClient struct {
	client    *SPIFFEClient
	principal spiffeauth.NormalizedSPIFFEPrincipal
}

// NewAuthenticatedSPIFFEClient binds a storage-resolved static SPIFFE client to
// its resolved principal. Both values must describe the same configured client.
func NewAuthenticatedSPIFFEClient(
	client *SPIFFEClient, principal spiffeauth.NormalizedSPIFFEPrincipal,
) (*AuthenticatedSPIFFEClient, error) {
	if client == nil {
		return nil, fmt.Errorf("SPIFFE client is required")
	}
	if principal.ClientID() == "" || principal.SPIFFEID() == "" || principal.TrustDomain() == "" {
		return nil, fmt.Errorf("resolved SPIFFE principal identity is required")
	}
	if principal.AuthenticationMethod() != spiffeauth.SPIFFEAuthenticationMethodX509 &&
		principal.AuthenticationMethod() != spiffeauth.SPIFFEAuthenticationMethodJWT {
		return nil, fmt.Errorf("resolved SPIFFE principal has invalid authentication method")
	}
	if client.GetID() == "" || client.GetID() != principal.ClientID() {
		return nil, fmt.Errorf("SPIFFE client ID does not match resolved principal")
	}
	return &AuthenticatedSPIFFEClient{client: client, principal: principal}, nil
}

// Principal returns the exact resolved SPIFFE principal for this authentication.
func (c *AuthenticatedSPIFFEClient) Principal() spiffeauth.NormalizedSPIFFEPrincipal {
	return c.principal
}

// GetID returns the configured OAuth client ID.
func (c *AuthenticatedSPIFFEClient) GetID() string { return c.client.GetID() }

// GetHashedSecret returns nil because SPIFFE clients do not use client secrets.
func (c *AuthenticatedSPIFFEClient) GetHashedSecret() []byte { return c.client.GetHashedSecret() }

// GetRedirectURIs returns the static client's redirect URIs.
func (c *AuthenticatedSPIFFEClient) GetRedirectURIs() []string { return c.client.GetRedirectURIs() }

// GetGrantTypes returns the configured grants.
func (c *AuthenticatedSPIFFEClient) GetGrantTypes() fosite.Arguments { return c.client.GetGrantTypes() }

// GetResponseTypes returns the static client's response types.
func (c *AuthenticatedSPIFFEClient) GetResponseTypes() fosite.Arguments {
	return c.client.GetResponseTypes()
}

// GetScopes returns the configured scopes.
func (c *AuthenticatedSPIFFEClient) GetScopes() fosite.Arguments { return c.client.GetScopes() }

// GetAudience returns the configured audience matching set.
func (c *AuthenticatedSPIFFEClient) GetAudience() fosite.Arguments { return c.client.GetAudience() }

// IsPublic reports that SPIFFE clients are confidential clients.
func (c *AuthenticatedSPIFFEClient) IsPublic() bool { return c.client.IsPublic() }

var _ fosite.Client = (*AuthenticatedSPIFFEClient)(nil)
