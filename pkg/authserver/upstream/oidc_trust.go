// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"context"
	"fmt"
)

// ProviderTypeOIDCTrust is the provider type for OIDC trust-only providers.
const ProviderTypeOIDCTrust ProviderType = "oidc-trust"

// Compile-time check that OIDCTrustProvider implements OAuth2Provider.
var _ OAuth2Provider = (*OIDCTrustProvider)(nil)

// OIDCTrustProvider provides OIDC discovery and JWKS trust material
// for token exchange validation. It does NOT participate in redirect-based
// authentication flows.
type OIDCTrustProvider struct {
	issuerURL        string
	expectedAudience string
	caBundlePath     string
	allowPrivateIP   bool
}

// NewOIDCTrustProvider creates a new OIDC trust-only provider.
// The issuerURL is the OIDC issuer whose JWKS will be used for token validation.
// The expectedAudience is the expected "aud" claim value; it may be empty for
// issuers where audience validation is not required.
// The caBundlePath is optional; when set, it is used to verify the issuer's TLS cert.
// When allowPrivateIP is true, the HTTP client used for OIDC discovery and JWKS
// fetching will permit connections to private IP addresses.
func NewOIDCTrustProvider(issuerURL, expectedAudience, caBundlePath string, allowPrivateIP bool) *OIDCTrustProvider {
	return &OIDCTrustProvider{
		issuerURL:        issuerURL,
		expectedAudience: expectedAudience,
		caBundlePath:     caBundlePath,
		allowPrivateIP:   allowPrivateIP,
	}
}

// Type returns the provider type identifier.
func (*OIDCTrustProvider) Type() ProviderType {
	return ProviderTypeOIDCTrust
}

// IssuerURL returns the OIDC issuer URL for this trust provider.
func (p *OIDCTrustProvider) IssuerURL() string {
	return p.issuerURL
}

// ExpectedAudience returns the expected audience for token validation.
func (p *OIDCTrustProvider) ExpectedAudience() string {
	return p.expectedAudience
}

// CABundlePath returns the CA bundle path for TLS verification of issuer endpoints.
func (p *OIDCTrustProvider) CABundlePath() string {
	return p.caBundlePath
}

// AllowPrivateIP returns whether the HTTP client should permit private IP addresses.
func (p *OIDCTrustProvider) AllowPrivateIP() bool {
	return p.allowPrivateIP
}

// AuthorizationURL is not supported. oidc-trust providers only contribute
// trust material for token exchange; they do not participate in the
// authorization code flow.
func (*OIDCTrustProvider) AuthorizationURL(_, _ string, _ ...AuthorizationOption) (string, error) {
	return "", fmt.Errorf("oidc-trust provider does not support authorization flows")
}

// ExchangeCodeForIdentity is not supported for trust-only providers.
func (*OIDCTrustProvider) ExchangeCodeForIdentity(_ context.Context, _, _, _ string) (*Identity, error) {
	return nil, fmt.Errorf("oidc-trust provider does not support authorization flows")
}

// RefreshTokens is not supported for trust-only providers.
func (*OIDCTrustProvider) RefreshTokens(_ context.Context, _, _ string) (*Tokens, error) {
	return nil, fmt.Errorf("oidc-trust provider does not support token refresh")
}
