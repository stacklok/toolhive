// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	// jwksCacheTTL is the time-to-live for cached JWKS fetched from external issuers.
	jwksCacheTTL = 5 * time.Minute

	// httpTimeout is the timeout for HTTP requests to external OIDC endpoints.
	httpTimeout = 10 * time.Second

	// maxResponseBodySize is the maximum size of HTTP response bodies read from
	// external OIDC endpoints (1 MiB). This prevents resource exhaustion from
	// unexpectedly large responses.
	maxResponseBodySize = 1 << 20
)

// Compile-time check that MultiIssuerTokenValidator implements SubjectTokenValidator.
var _ SubjectTokenValidator = (*MultiIssuerTokenValidator)(nil)

// TrustedIssuer configures an external OIDC issuer whose tokens are
// accepted as subject tokens during token exchange.
type TrustedIssuer struct {
	// IssuerURL is the expected "iss" claim value (exact match).
	IssuerURL string
	// ExpectedAudience is the expected "aud" claim value that must appear
	// in the token's audience list. Required; NewMultiIssuerTokenValidator
	// rejects any TrustedIssuer with an empty ExpectedAudience.
	ExpectedAudience string
	// JWKSURL is the URL to fetch the issuer's JSON Web Key Set from.
	// If empty, it is resolved via OIDC discovery at {IssuerURL}/.well-known/openid-configuration.
	JWKSURL string
}

// MultiIssuerTokenValidator validates subject tokens from the authorization
// server itself or from configured external OIDC issuers.
//
// For self-issued tokens (where the "iss" claim matches selfIssuer), validation
// is delegated to the SelfIssuedTokenValidator. For tokens from trusted external
// issuers, the validator resolves the issuer's JWKS (via OIDC discovery if needed),
// verifies the JWT signature, and validates standard claims.
type MultiIssuerTokenValidator struct {
	selfIssuer    string
	selfValidator *SelfIssuedTokenValidator
	issuers       map[string]*externalIssuerConfig
	httpClient    *http.Client

	// insecureSkipJWKSURLValidation disables HTTPS enforcement on discovered
	// JWKS URLs. This MUST only be set for testing with httptest servers.
	insecureSkipJWKSURLValidation bool
}

// externalIssuerConfig holds the configuration and cached state for an external
// OIDC issuer. The mutex protects lazy JWKS URL discovery and JWKS caching.
type externalIssuerConfig struct {
	TrustedIssuer

	mu      sync.Mutex
	jwksURL string              // resolved from OIDC discovery or preconfigured
	jwks    *jose.JSONWebKeySet // cached JWKS
	jwksExp time.Time           // when the cached JWKS expires
}

// NewMultiIssuerTokenValidator creates a validator that accepts tokens from the
// authorization server itself and from the provided list of trusted external issuers.
// The httpClient parameter is used for OIDC discovery and JWKS fetching. When nil,
// http.DefaultClient is used (which won't trust custom CAs like SPIFFE).
// Returns an error if any TrustedIssuer has an empty ExpectedAudience.
func NewMultiIssuerTokenValidator(
	selfValidator *SelfIssuedTokenValidator,
	selfIssuer string,
	trustedIssuers []TrustedIssuer,
	httpClient *http.Client,
) (*MultiIssuerTokenValidator, error) {
	for _, issuer := range trustedIssuers {
		if issuer.ExpectedAudience == "" {
			return nil, fmt.Errorf("trusted issuer %q: ExpectedAudience is required", issuer.IssuerURL)
		}
	}

	issuers := make(map[string]*externalIssuerConfig, len(trustedIssuers))
	for _, ti := range trustedIssuers {
		issuers[ti.IssuerURL] = &externalIssuerConfig{
			TrustedIssuer: ti,
			jwksURL:       ti.JWKSURL,
		}
	}

	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: httpTimeout,
		}
	}

	return &MultiIssuerTokenValidator{
		selfIssuer:    selfIssuer,
		selfValidator: selfValidator,
		issuers:       issuers,
		httpClient:    httpClient,
	}, nil
}

// Validate parses the raw JWT to extract the issuer claim, then routes validation
// to either the self-issued validator or the appropriate external issuer validator.
// Returns an error if the issuer is not trusted.
func (v *MultiIssuerTokenValidator) Validate(ctx context.Context, rawToken string) (*ValidatedClaims, error) {
	// Parse the JWT without verification to peek at the issuer claim.
	// This is safe because we verify the signature in a subsequent step.
	issuer, err := peekIssuer(rawToken)
	if err != nil {
		return nil, fmt.Errorf("failed to determine token issuer: %w", err)
	}

	slog.Debug("multi-issuer validator: routing token",
		"issuer", issuer,
		"self_issuer", v.selfIssuer,
		"trusted_issuers_count", len(v.issuers),
	)

	// Self-issued tokens are delegated to the existing validator.
	if issuer == v.selfIssuer {
		slog.Debug("multi-issuer validator: routing to self-issued validator")
		return v.selfValidator.Validate(ctx, rawToken)
	}

	// Look up the external issuer configuration.
	issuerConfig, ok := v.issuers[issuer]
	if !ok {
		knownIssuers := make([]string, 0, len(v.issuers))
		for k := range v.issuers {
			knownIssuers = append(knownIssuers, k)
		}
		slog.Debug("multi-issuer validator: issuer not trusted",
			"issuer", issuer,
			"known_issuers", knownIssuers,
		)
		return nil, fmt.Errorf("untrusted issuer: %q", issuer)
	}

	slog.Debug("multi-issuer validator: routing to external validator",
		"issuer", issuer,
		"expected_audience", issuerConfig.ExpectedAudience,
	)
	result, err := v.validateExternalToken(ctx, rawToken, issuerConfig)
	if err != nil {
		slog.Debug("multi-issuer validator: external validation failed",
			"issuer", issuer,
			"error", err,
		)
	}
	return result, err
}

// validateExternalToken verifies a JWT from a trusted external issuer by
// fetching the issuer's JWKS (with caching) and validating the signature and claims.
func (v *MultiIssuerTokenValidator) validateExternalToken(
	ctx context.Context,
	rawToken string,
	issuerConfig *externalIssuerConfig,
) (*ValidatedClaims, error) {
	parsedToken, err := jwt.ParseSigned(rawToken, allowedSignatureAlgorithms)
	if err != nil {
		return nil, fmt.Errorf("subject token is not a valid JWT: %w", err)
	}

	jwks, err := v.resolveJWKS(ctx, issuerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS for issuer %s: %w", issuerConfig.IssuerURL, err)
	}

	var standardClaims jwt.Claims
	var extraClaims map[string]interface{}

	if err := verifySignature(parsedToken, jwks, &standardClaims, &extraClaims); err != nil {
		return nil, err
	}

	// Validate standard claims: issuer must match, token must not be expired.
	// Audience is checked only when ExpectedAudience is configured.
	expected := jwt.Expected{
		Issuer: issuerConfig.IssuerURL,
	}
	if issuerConfig.ExpectedAudience != "" {
		expected.AnyAudience = jwt.Audience{issuerConfig.ExpectedAudience}
	}
	if err := standardClaims.ValidateWithLeeway(expected, 0); err != nil {
		return nil, fmt.Errorf("subject token claims validation failed: %w", err)
	}

	// Subject is required for delegation.
	if standardClaims.Subject == "" {
		return nil, fmt.Errorf("subject token is missing required 'sub' claim")
	}

	return buildValidatedClaims(standardClaims, extraClaims), nil
}

// resolveJWKS returns the cached JWKS for an external issuer, fetching it if
// the cache is empty or expired. If the JWKS URL is not configured, it is first
// resolved via OIDC discovery.
func (v *MultiIssuerTokenValidator) resolveJWKS(
	ctx context.Context,
	issuerConfig *externalIssuerConfig,
) (*jose.JSONWebKeySet, error) {
	issuerConfig.mu.Lock()
	defer issuerConfig.mu.Unlock()

	// Return cached JWKS if still valid.
	if issuerConfig.jwks != nil && time.Now().Before(issuerConfig.jwksExp) {
		return issuerConfig.jwks, nil
	}

	// Cache expired — clear the discovered URL so we re-discover on next fetch.
	// This handles the (rare) case where an issuer rotates its JWKS endpoint URL.
	// The explicitly configured JWKSURL (from TrustedIssuer) is preserved.
	if issuerConfig.JWKSURL == "" {
		issuerConfig.jwksURL = ""
	}

	// Discover the JWKS URL if not yet resolved.
	if issuerConfig.jwksURL == "" {
		jwksURL, err := v.discoverJWKSURL(ctx, issuerConfig.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("OIDC discovery failed for %s: %w", issuerConfig.IssuerURL, err)
		}
		issuerConfig.jwksURL = jwksURL
	}

	// Fetch and cache the JWKS.
	jwks, err := v.fetchJWKS(ctx, issuerConfig.jwksURL)
	if err != nil {
		return nil, err
	}

	issuerConfig.jwks = jwks
	issuerConfig.jwksExp = time.Now().Add(jwksCacheTTL)

	return jwks, nil
}

// peekIssuer parses a JWT without signature verification to extract the "iss" claim.
func peekIssuer(rawToken string) (string, error) {
	token, err := jwt.ParseSigned(rawToken, allowedSignatureAlgorithms)
	if err != nil {
		return "", fmt.Errorf("subject token is not a valid JWT: %w", err)
	}

	var claims jwt.Claims
	if err := token.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return "", fmt.Errorf("failed to extract claims from subject token: %w", err)
	}

	if claims.Issuer == "" {
		return "", fmt.Errorf("subject token is missing 'iss' claim")
	}

	return claims.Issuer, nil
}

// discoverJWKSURL performs OIDC discovery to resolve the JWKS URL for an issuer.
// It fetches the OpenID Connect discovery document at {issuerURL}/.well-known/openid-configuration
// and extracts the jwks_uri field.
func (v *MultiIssuerTokenValidator) discoverJWKSURL(ctx context.Context, issuerURL string) (string, error) {
	discoveryURL := issuerURL + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create discovery request: %w", err)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("discovery request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return "", fmt.Errorf("failed to read discovery response: %w", err)
	}

	var doc oauthproto.OIDCDiscoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("failed to parse discovery document: %w", err)
	}

	if doc.JWKSURI == "" {
		return "", fmt.Errorf("discovery document missing 'jwks_uri'")
	}

	if !v.insecureSkipJWKSURLValidation {
		if err := validateJWKSURL(doc.JWKSURI); err != nil {
			return "", fmt.Errorf("discovered jwks_uri is invalid: %w", err)
		}
	}

	return doc.JWKSURI, nil
}

// validateJWKSURL checks that the JWKS URL uses HTTPS and is not a private/loopback address.
// This prevents SSRF attacks where a compromised discovery document points to internal services.
func validateJWKSURL(jwksURL string) error {
	u, err := url.Parse(jwksURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "https" {
		return fmt.Errorf("must use HTTPS, got %q", u.Scheme)
	}

	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return fmt.Errorf("must not point to a private or loopback address")
	}

	return nil
}

// fetchJWKS fetches a JSON Web Key Set from the given URL.
func (v *MultiIssuerTokenValidator) fetchJWKS(ctx context.Context, jwksURL string) (*jose.JSONWebKeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS request: %w", err)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("JWKS request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("JWKS endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, fmt.Errorf("failed to read JWKS response: %w", err)
	}

	var jwks jose.JSONWebKeySet
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, fmt.Errorf("failed to parse JWKS: %w", err)
	}

	return &jwks, nil
}
