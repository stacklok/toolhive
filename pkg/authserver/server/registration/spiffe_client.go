// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"
	"slices"

	"github.com/ory/fosite"
)

// SPIFFEClient is the immutable OAuth client representation of a configured
// SPIFFE principal association. It is neither public nor secret-bearing. A
// future credential-validation implementation will authenticate its SPIFFE
// credentials; this configuration-only implementation does not authenticate
// any credentials.
type SPIFFEClient struct {
	id                   string
	grantTypes           fosite.Arguments
	scopes               fosite.Arguments
	resources            []string
	audiences            []string
	tokenExchangeEnabled bool
}

// NewSPIFFEClient creates an immutable, secretless, non-public client for a
// configured SPIFFE association. Resources and token audiences remain distinct
// from Fosite's GetAudience field because existing token-exchange handling uses
// that field for its established semantics.
func NewSPIFFEClient(
	id string,
	grantTypes, scopes, resources, audiences []string,
	tokenExchangeEnabled bool,
) (*SPIFFEClient, error) {
	if id == "" {
		return nil, fmt.Errorf("SPIFFE client ID is required")
	}

	return &SPIFFEClient{
		id:                   id,
		grantTypes:           slices.Clone(grantTypes),
		scopes:               slices.Clone(scopes),
		resources:            slices.Clone(resources),
		audiences:            slices.Clone(audiences),
		tokenExchangeEnabled: tokenExchangeEnabled,
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

// Resources returns a copy of the configured RFC 8707 resource indicators.
func (c *SPIFFEClient) Resources() []string { return slices.Clone(c.resources) }

// Audiences returns a copy of the configured token audiences.
func (c *SPIFFEClient) Audiences() []string { return slices.Clone(c.audiences) }

// TokenExchangeEnabled reports whether RFC 8693 token exchange is permitted.
func (c *SPIFFEClient) TokenExchangeEnabled() bool { return c.tokenExchangeEnabled }

// GetAudience returns nil. Resource and token-audience policies are deliberately
// not represented through Fosite's single audience field, which existing token
// exchange code uses for both request parameters.
func (*SPIFFEClient) GetAudience() fosite.Arguments { return nil }

// IsPublic returns false so Fosite does not treat unauthenticated requests as
// public-client requests. Future SPIFFE credential validation remains separate.
func (*SPIFFEClient) IsPublic() bool { return false }

var _ fosite.Client = (*SPIFFEClient)(nil)
