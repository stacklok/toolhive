// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"
	"slices"

	"github.com/ory/fosite"
)

// SPIFFEClient is the immutable OAuth client representation of a configured
// SPIFFE principal association. It is neither public nor secret-bearing: it
// authenticates with an SVID (see docs/arch/18-spiffe-association-declarations.md).
type SPIFFEClient struct {
	BackChannelOnlyMarker
	id         string
	grantTypes fosite.Arguments
	scopes     fosite.Arguments
	resources  []string
}

// NewSPIFFEClient creates an immutable, secretless, non-public client for a
// configured SPIFFE association. resources is the RFC 8707 resource allowlist,
// available via Resources(). GetAudience returns nil: SPIFFE clients are
// targeted by resource only.
func NewSPIFFEClient(id string, grantTypes, scopes, resources []string) (*SPIFFEClient, error) {
	if id == "" {
		return nil, fmt.Errorf("SPIFFE client ID is required")
	}
	if len(grantTypes) == 0 {
		return nil, fmt.Errorf("SPIFFE client grant types are required")
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("SPIFFE client scopes are required")
	}

	return &SPIFFEClient{
		id:         id,
		grantTypes: slices.Clone(grantTypes),
		scopes:     slices.Clone(scopes),
		resources:  slices.Clone(resources),
	}, nil
}

// GetID returns the configured association client ID.
func (c *SPIFFEClient) GetID() string { return c.id }

// GetHashedSecret returns nil because no OAuth client secret is assigned.
// JWT-SVID and X.509-SVID authentication do not use a client secret.
func (*SPIFFEClient) GetHashedSecret() []byte { return nil }

// GetRedirectURIs returns nil because SPIFFE clients do not use authorization redirects.
func (*SPIFFEClient) GetRedirectURIs() []string { return nil }

// GetGrantTypes returns a copy of the configured grant types.
func (c *SPIFFEClient) GetGrantTypes() fosite.Arguments { return slices.Clone(c.grantTypes) }

// GetResponseTypes returns nil because SPIFFE clients do not use authorization responses.
func (*SPIFFEClient) GetResponseTypes() fosite.Arguments { return nil }

// GetScopes returns a copy of the configured scopes.
func (c *SPIFFEClient) GetScopes() fosite.Arguments { return slices.Clone(c.scopes) }

// Resources returns a copy of the allowed RFC 8707 resource request values.
// Token exchange checks the "resource" request parameter against this, never
// against GetAudience.
func (c *SPIFFEClient) Resources() []string { return slices.Clone(c.resources) }

// GetAudience returns nil: SPIFFE clients carry no audience allowlist.
func (*SPIFFEClient) GetAudience() fosite.Arguments { return nil }

// IsPublic returns false so Fosite does not treat unauthenticated requests as
// public-client requests.
func (*SPIFFEClient) IsPublic() bool { return false }

var _ fosite.Client = (*SPIFFEClient)(nil)
