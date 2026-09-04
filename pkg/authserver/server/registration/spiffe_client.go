// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"
	"slices"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// SPIFFEClient is the immutable OAuth client representation of a configured
// SPIFFE principal association. It is neither public nor secret-bearing. A
// future credential-validation implementation will authenticate its SPIFFE
// credentials; this configuration-only implementation does not authenticate
// any credentials.
type SPIFFEClient struct {
	BackChannelOnlyMarker
	id         string
	grantTypes fosite.Arguments
	scopes     fosite.Arguments
	audiences  []string
	resources  []string
}

// NewSPIFFEClient creates an immutable, secretless, non-public client for a
// configured SPIFFE association. GetAudience returns the configured audience
// allowlist. resources is the independent RFC 8707 resource allowlist,
// available via Resources(); it may be empty.
func NewSPIFFEClient(id string, scopes, audiences, resources []string) (*SPIFFEClient, error) {
	if id == "" {
		return nil, fmt.Errorf("SPIFFE client ID is required")
	}
	if len(scopes) == 0 || len(audiences) == 0 {
		return nil, fmt.Errorf("SPIFFE client scopes and audiences are required")
	}

	return &SPIFFEClient{
		id:         id,
		grantTypes: fosite.Arguments{oauthproto.GrantTypeTokenExchange},
		scopes:     slices.Clone(scopes),
		audiences:  slices.Clone(audiences),
		resources:  slices.Clone(resources),
	}, nil
}

// GetID returns the configured association client ID.
func (c *SPIFFEClient) GetID() string { return c.id }

// GetHashedSecret returns nil because no OAuth client secret is assigned. Future
// SPIFFE credential validation is outside this configuration-only implementation.
func (*SPIFFEClient) GetHashedSecret() []byte { return nil }

// GetRedirectURIs returns nil because SPIFFE clients do not use authorization redirects.
func (*SPIFFEClient) GetRedirectURIs() []string { return nil }

// GetGrantTypes returns a copy of the configured grant types.
func (c *SPIFFEClient) GetGrantTypes() fosite.Arguments { return slices.Clone(c.grantTypes) }

// GetResponseTypes returns nil because SPIFFE clients do not use authorization responses.
func (*SPIFFEClient) GetResponseTypes() fosite.Arguments { return nil }

// GetScopes returns a copy of the configured scopes.
func (c *SPIFFEClient) GetScopes() fosite.Arguments { return slices.Clone(c.scopes) }

// Audiences returns a copy of the allowed RFC 8693 audience request values.
func (c *SPIFFEClient) Audiences() []string { return slices.Clone(c.audiences) }

// Resources returns a copy of the allowed RFC 8707 resource request values.
// Resources and audiences are independent request dimensions; the token
// exchange handler checks the "resource" request parameter against this,
// not against GetAudience.
func (c *SPIFFEClient) Resources() []string { return slices.Clone(c.resources) }

// GetAudience returns the allowed RFC 8693 audience request values.
func (c *SPIFFEClient) GetAudience() fosite.Arguments { return slices.Clone(c.audiences) }

// IsPublic returns false so Fosite does not treat unauthenticated requests as
// public-client requests. Future SPIFFE credential validation remains separate.
func (*SPIFFEClient) IsPublic() bool { return false }

var _ fosite.Client = (*SPIFFEClient)(nil)
