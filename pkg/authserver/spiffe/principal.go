// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spiffeauth

import "slices"

const (
	// SPIFFEAuthenticationMethodX509 authenticates a workload with an X.509-SVID.
	SPIFFEAuthenticationMethodX509 SPIFFEAuthenticationMethod = "spiffe_x509"
	// SPIFFEAuthenticationMethodJWT authenticates a workload with a JWT-SVID.
	SPIFFEAuthenticationMethodJWT SPIFFEAuthenticationMethod = "spiffe_jwt"
)

// SPIFFEAuthenticationMethod identifies the credential type permitted for a
// SPIFFE workload. Methods are explicit so introducing another credential type
// cannot silently broaden a policy.
type SPIFFEAuthenticationMethod string

// SPIFFEAuthorizationPolicy is the immutable authorization policy selected by a
// validated SPIFFE association. Resources and audiences remain separate.
type SPIFFEAuthorizationPolicy struct {
	grantTypes    []string
	scopes        []string
	resources     []string
	audiences     []string
	tokenExchange bool
}

// NewSPIFFEAuthorizationPolicy builds an immutable policy from already-validated
// fields, cloning slices so the caller's copies cannot mutate the result.
func NewSPIFFEAuthorizationPolicy(
	grantTypes, scopes, resources, audiences []string, tokenExchange bool,
) SPIFFEAuthorizationPolicy {
	return SPIFFEAuthorizationPolicy{
		grantTypes:    slices.Clone(grantTypes),
		scopes:        slices.Clone(scopes),
		resources:     slices.Clone(resources),
		audiences:     slices.Clone(audiences),
		tokenExchange: tokenExchange,
	}
}

// GrantTypes returns a copy of the permitted OAuth grant types.
func (p SPIFFEAuthorizationPolicy) GrantTypes() []string { return slices.Clone(p.grantTypes) }

// Scopes returns a copy of the permitted OAuth scopes.
func (p SPIFFEAuthorizationPolicy) Scopes() []string { return slices.Clone(p.scopes) }

// Resources returns a copy of the permitted RFC 8707 resource indicators.
func (p SPIFFEAuthorizationPolicy) Resources() []string { return slices.Clone(p.resources) }

// Audiences returns a copy of the permitted RFC 8693 audiences.
func (p SPIFFEAuthorizationPolicy) Audiences() []string { return slices.Clone(p.audiences) }

// TokenExchangeEnabled reports whether RFC 8693 token exchange is permitted.
func (p SPIFFEAuthorizationPolicy) TokenExchangeEnabled() bool { return p.tokenExchange }

// NormalizedSPIFFEPrincipal is an immutable, validated identity selected from a
// SPIFFE association. It represents policy only; it does not authenticate a
// credential or enable an authentication method.
type NormalizedSPIFFEPrincipal struct {
	clientID      string
	spiffeID      string
	trustDomain   string
	authMethod    SPIFFEAuthenticationMethod
	authorization SPIFFEAuthorizationPolicy
}

// NewNormalizedSPIFFEPrincipal builds an immutable principal from an
// already-resolved association. Callers must have validated the SPIFFE ID,
// client ownership, and authentication method before calling this.
func NewNormalizedSPIFFEPrincipal(
	clientID, spiffeID, trustDomain string,
	authMethod SPIFFEAuthenticationMethod,
	authorization SPIFFEAuthorizationPolicy,
) NormalizedSPIFFEPrincipal {
	return NormalizedSPIFFEPrincipal{
		clientID:      clientID,
		spiffeID:      spiffeID,
		trustDomain:   trustDomain,
		authMethod:    authMethod,
		authorization: authorization,
	}
}

// ClientID returns the configured OAuth client ID.
func (p NormalizedSPIFFEPrincipal) ClientID() string { return p.clientID }

// SPIFFEID returns the canonical concrete SPIFFE ID.
func (p NormalizedSPIFFEPrincipal) SPIFFEID() string { return p.spiffeID }

// TrustDomain returns the canonical SPIFFE trust domain.
func (p NormalizedSPIFFEPrincipal) TrustDomain() string { return p.trustDomain }

// AuthenticationMethod returns the selected credential-method discriminator.
func (p NormalizedSPIFFEPrincipal) AuthenticationMethod() SPIFFEAuthenticationMethod {
	return p.authMethod
}

// AuthorizationPolicy returns a defensive copy of the selected policy.
func (p NormalizedSPIFFEPrincipal) AuthorizationPolicy() SPIFFEAuthorizationPolicy {
	return NewSPIFFEAuthorizationPolicy(
		p.authorization.grantTypes, p.authorization.scopes,
		p.authorization.resources, p.authorization.audiences,
		p.authorization.tokenExchange,
	)
}
