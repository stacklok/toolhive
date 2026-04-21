// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	jwt "github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/oauthproto/jwtbearer"
	"github.com/stacklok/toolhive/pkg/oauthproto/tokenexchange"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// XAAStrategy implements XAA (Cross-Application Access) as a two-step token
// exchange, following draft-ietf-oauth-identity-assertion-authz-grant (ID-JAG):
//
//   - Step A (RFC 8693): Exchange the user's ID token at the IdP for an ID-JAG JWT.
//   - Step B (RFC 7523): Exchange the ID-JAG at the target AS for an access token.
//
// Both steps run on every Authenticate call. The upper vMCP TokenCache layer is
// responsible for reusing the resulting access token across requests; this
// strategy holds no local cache.
//
// The subject ID token is not validated locally before Step A. The IdP enforces
// its own exp check; if the token is expired, Step A returns an OAuth
// invalid_grant error which propagates to the caller. Callers that need
// fine-grained re-auth signals can inspect it with:
//
//	var re *oauth2.RetrieveError
//	if errors.As(err, &re) && re.ErrorCode == "invalid_grant" { ... }
type XAAStrategy struct {
	envReader env.Reader
}

// NewXAAStrategy creates a new XAAStrategy instance.
func NewXAAStrategy(envReader env.Reader) *XAAStrategy {
	return &XAAStrategy{
		envReader: envReader,
	}
}

// Name returns the strategy identifier.
func (*XAAStrategy) Name() string {
	return authtypes.StrategyTypeXAA
}

// Authenticate performs the two-step XAA (ID-JAG) flow and injects the resulting
// access token into the backend request.
//
// This method:
//  1. Parses and validates the XAA configuration from the strategy
//  2. For health check requests: uses a client credentials grant at TargetTokenURL
//     if target client credentials are configured; otherwise skips authentication
//  3. For regular requests: retrieves the user's ID token from the identity's
//     UpstreamIDTokens map, performs Step A (token exchange for ID-JAG) and
//     Step B (JWT Bearer grant for access token), then injects the access token
//
// Parameters:
//   - ctx: Request context containing the authenticated identity (or health check marker)
//   - req: The HTTP request to authenticate
//   - strategy: Backend auth strategy containing XAA configuration
//
// Returns an error if:
//   - Strategy configuration is invalid or incomplete
//   - No identity is found in the context (regular requests only)
//   - The upstream ID token for the configured provider is not found
//   - Step A (token exchange) or Step B (JWT Bearer grant) fails
func (s *XAAStrategy) Authenticate(
	ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
	config, err := s.parseXAAConfig(strategy)
	if err != nil {
		return fmt.Errorf("invalid strategy configuration: %w", err)
	}

	// For health checks there is no user identity. If target client credentials
	// are configured, use a client credentials grant to authenticate the probe.
	// Otherwise skip authentication — the backend will be probed unauthenticated.
	if healthcontext.IsHealthCheck(ctx) {
		if config.targetClientID != "" && config.targetClientSecret != "" {
			return s.authenticateWithClientCredentials(ctx, req, config)
		}
		return nil
	}

	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("no identity found in context")
	}

	slog.Debug("xaa: authenticate called",
		"subject", identity.Subject,
		"subjectProviderName", config.subjectProviderName,
		"upstreamTokenProviders", mapKeys(identity.UpstreamTokens),
		"upstreamIDTokenProviders", mapKeys(identity.UpstreamIDTokens),
	)

	// Look up the ID token for the configured upstream provider.
	idToken := identity.UpstreamIDTokens[config.subjectProviderName] // nil map safe in Go
	if idToken == "" {
		slog.Debug("xaa: ID token not found for provider",
			"provider", config.subjectProviderName,
			"availableProviders", mapKeys(identity.UpstreamIDTokens),
		)
		return fmt.Errorf("provider %q: %w", config.subjectProviderName, authtypes.ErrUpstreamTokenNotFound)
	}

	slog.Debug("xaa: found ID token for provider", "provider", config.subjectProviderName)

	// Step A: Exchange the user's ID token for an ID-JAG at the IdP.
	assertion, err := s.performStepA(ctx, idToken, config)
	if err != nil {
		slog.Debug("xaa: Step A failed", "error", err)
		return fmt.Errorf("step A (ID-JAG exchange) failed: %w", err)
	}

	slog.Debug("xaa: Step A succeeded, got ID-JAG")

	// Step B: Exchange the ID-JAG for an access token at the target AS.
	accessToken, err := s.performStepB(ctx, assertion, config)
	if err != nil {
		slog.Debug("xaa: Step B failed", "error", err)
		return fmt.Errorf("step B (JWT Bearer grant) failed: %w", err)
	}

	slog.Debug("xaa: Step B succeeded, setting Bearer token")

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	return nil
}

// Validate checks if the required configuration fields are present and valid.
//
// This method verifies that:
//   - IDPTokenURL is present and a valid HTTPS (or localhost HTTP) URL with a host and no fragment
//   - TargetTokenURL is present and a valid HTTPS (or localhost HTTP) URL with a host and no fragment
//   - TargetAudience is present
//   - Client secrets are only provided when client IDs are present, and a
//     configured client ID has an accompanying secret source
//
// As a side effect, this resolves client secrets from the environment when a
// *ClientSecretEnv var is configured, and returns an error if that var is unset.
//
// This validation is typically called during configuration parsing to fail fast
// if the strategy is misconfigured.
func (s *XAAStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
	_, err := s.parseXAAConfig(strategy)
	return err
}

// xaaParsedConfig holds the parsed and resolved XAA configuration.
type xaaParsedConfig struct {
	idpTokenURL         string
	idpClientID         string
	idpClientSecret     string //nolint:gosec // G101: field name, not a credential
	targetTokenURL      string
	targetClientID      string
	targetClientSecret  string //nolint:gosec // G101: field name, not a credential
	targetAudience      string
	targetResource      string
	scopes              []string
	subjectProviderName string
	allowInsecureHTTP   bool
}

// authenticateWithClientCredentials performs an OAuth2 client credentials grant at
// the target AS and injects the resulting token. Used for health check probes.
func (*XAAStrategy) authenticateWithClientCredentials(
	ctx context.Context, req *http.Request, config *xaaParsedConfig,
) error {
	ccConfig := clientcredentials.Config{
		ClientID:       config.targetClientID,
		ClientSecret:   config.targetClientSecret,
		TokenURL:       config.targetTokenURL,
		Scopes:         config.scopes,
		EndpointParams: url.Values{"audience": {config.targetAudience}},
	}

	token, err := ccConfig.Token(ctx)
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			re.Body = nil
		}
		return fmt.Errorf("client credentials grant failed: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	return nil
}

// performStepA runs the RFC 8693 token exchange at the IdP to obtain an ID-JAG.
// It verifies the response's issued_token_type matches the ID-JAG URN per
// draft-ietf-oauth-identity-assertion-authz-grant §4.3.3.
func (*XAAStrategy) performStepA(
	ctx context.Context, idToken string, config *xaaParsedConfig,
) (string, error) {
	exchangeCfg := &tokenexchange.ExchangeConfig{
		TokenURL:           config.idpTokenURL,
		ClientID:           config.idpClientID,
		ClientSecret:       config.idpClientSecret,
		SubjectTokenType:   oauthproto.TokenTypeIDToken,
		RequestedTokenType: oauthproto.TokenTypeIDJAG,
		Audience:           config.targetAudience,
		Resource:           config.targetResource,
		Scopes:             config.scopes,
		SubjectTokenProvider: func() (string, error) {
			return idToken, nil
		},
	}

	token, err := exchangeCfg.TokenSource(ctx).Token()
	if err != nil {
		return "", fmt.Errorf("step A token exchange: %w", err)
	}

	// Per draft-ietf-oauth-identity-assertion-authz-grant §4.3.3, the IdP MUST
	// return issued_token_type = urn:ietf:params:oauth:token-type:id-jag. Fail
	// loudly if it returned anything else so Step B cannot accidentally be fed
	// a non-assertion token.
	got, _ := token.Extra("issued_token_type").(string)
	if got != oauthproto.TokenTypeIDJAG {
		return "", fmt.Errorf("step A: IdP returned issued_token_type=%q, want %q", got, oauthproto.TokenTypeIDJAG)
	}

	// Per draft-ietf-oauth-identity-assertion-authz-grant §5.2, the RFC 8693
	// response MUST carry token_type=N_A for an assertion that is not usable
	// as a bearer token at the IdP.
	if !strings.EqualFold(token.TokenType, "N_A") {
		return "", fmt.Errorf("step A: IdP returned token_type=%q, want N_A (draft §5.2)", token.TokenType)
	}

	// The AccessToken field holds the ID-JAG per RFC 8693 response format.
	assertion := token.AccessToken
	if assertion == "" {
		return "", fmt.Errorf("step A: IdP returned an empty ID-JAG assertion")
	}

	// Defence-in-depth: inspect the JWT typ header. The draft §3.1 MUST for
	// typ: oauth-id-jag+jwt is on the issuer (IdP); ToolHive is the
	// holder/presenter — the target AS validates the ID-JAG in Step B. A
	// mismatch is deliberately logged rather than fatal; the fatal contract
	// enforced here is issued_token_type (RFC 8693 §2.2.1, checked above).
	const idJAGJWTType = "oauth-id-jag+jwt"
	parser := jwt.NewParser()
	parsed, _, parseErr := parser.ParseUnverified(assertion, jwt.MapClaims{})
	if parseErr == nil {
		if typ, _ := parsed.Header["typ"].(string); !strings.EqualFold(typ, idJAGJWTType) {
			slog.Debug("xaa: ID-JAG JWT typ header mismatch",
				"got", typ, "want", idJAGJWTType,
			)
		}
	} else {
		slog.Debug("xaa: could not parse ID-JAG JWT header for typ check", "error", parseErr)
	}

	return assertion, nil
}

// performStepB runs the RFC 7523 JWT Bearer grant at the target AS using the
// ID-JAG assertion returned by Step A.
func (*XAAStrategy) performStepB(
	ctx context.Context, assertion string, config *xaaParsedConfig,
) (string, error) {
	bearerCfg := &jwtbearer.Config{
		TokenURL:          config.targetTokenURL,
		ClientID:          config.targetClientID,
		ClientSecret:      config.targetClientSecret,
		Scopes:            config.scopes,
		InsecureAllowHTTP: config.allowInsecureHTTP,
		AssertionProvider: func() (string, error) {
			return assertion, nil
		},
	}

	token, err := bearerCfg.TokenSource(ctx).Token()
	if err != nil {
		return "", fmt.Errorf("step B jwt-bearer grant: %w", err)
	}

	return token.AccessToken, nil
}

// validateTokenURL checks that a configured token endpoint is a syntactically
// valid HTTPS (or localhost HTTP) URL with a host and no fragment. Mirrors the
// validator in pkg/oauthproto/jwtbearer.Config.Validate.
func validateTokenURL(label, endpoint string) error {
	return validateTokenURLWithInsecure(label, endpoint, false)
}

// validateTokenURLWithInsecure is like validateTokenURL but allows plain HTTP
// when allowInsecureHTTP is true. Used for the TargetTokenURL when
// InsecureTargetTokenURL is set.
func validateTokenURLWithInsecure(label, endpoint string, allowInsecureHTTP bool) error {
	if err := networking.ValidateEndpointURLWithInsecure(endpoint, allowInsecureHTTP); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", label, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("%s must use http or https scheme", label)
	}
	if u.Host == "" {
		return fmt.Errorf("%s must include a host", label)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment", label)
	}
	if u.User != nil {
		return fmt.Errorf("%s must not contain embedded credentials", label)
	}
	return nil
}

// resolveClientSecret resolves a client secret from either a direct value or an
// environment variable. If both are provided, the direct value takes precedence.
// Returns an error if a secret source requires a client ID but none is provided,
// or if a non-empty clientID is configured but no secret source is available.
func resolveClientSecret(envReader env.Reader, clientID, secret, secretEnv string) (string, error) {
	// Check direct secret first (takes precedence).
	if secret != "" {
		if clientID == "" {
			return "", fmt.Errorf("client secret cannot be provided without client ID")
		}
		return secret, nil
	}

	// Check environment variable.
	if secretEnv != "" {
		if clientID == "" {
			return "", fmt.Errorf("client secret env cannot be provided without client ID")
		}
		resolved := envReader.Getenv(secretEnv)
		if resolved == "" {
			return "", fmt.Errorf("environment variable %s not set or empty", secretEnv)
		}
		return resolved, nil
	}

	// A configured client ID without any secret source is a misconfiguration —
	// fail loudly at Validate time rather than deferring to the first request.
	if clientID != "" {
		return "", fmt.Errorf("client %q requires either client secret or client secret env var", clientID)
	}

	return "", nil
}

// mapKeys returns the keys of a string map (for debug logging).
func mapKeys(m map[string]string) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// parseXAAConfig parses and validates the XAA configuration from BackendAuthStrategy.
func (s *XAAStrategy) parseXAAConfig(strategy *authtypes.BackendAuthStrategy) (*xaaParsedConfig, error) {
	if strategy == nil || strategy.XAA == nil {
		return nil, fmt.Errorf("XAA configuration is required")
	}

	cfg := strategy.XAA

	// Required fields.
	if cfg.IDPTokenURL == "" {
		return nil, fmt.Errorf("IDPTokenURL is required in xaa configuration")
	}
	if cfg.TargetTokenURL == "" {
		return nil, fmt.Errorf("TargetTokenURL is required in xaa configuration")
	}
	if cfg.TargetAudience == "" {
		return nil, fmt.Errorf("TargetAudience is required in xaa configuration")
	}

	// Token endpoint URL shape: must be https (or http on localhost), have a
	// host, and not carry a fragment. Applied to both IdP and target endpoints.
	if err := validateTokenURL("IDPTokenURL", cfg.IDPTokenURL); err != nil {
		return nil, err
	}
	if err := validateTokenURLWithInsecure("TargetTokenURL", cfg.TargetTokenURL, cfg.InsecureTargetTokenURL); err != nil {
		return nil, err
	}

	// Resolve IdP client secret.
	idpSecret, err := resolveClientSecret(s.envReader, cfg.IDPClientID, cfg.IDPClientSecret, cfg.IDPClientSecretEnv)
	if err != nil {
		return nil, fmt.Errorf("IdP credentials: %w", err)
	}

	// Resolve target client secret.
	targetSecret, err := resolveClientSecret(s.envReader, cfg.TargetClientID, cfg.TargetClientSecret, cfg.TargetClientSecretEnv)
	if err != nil {
		return nil, fmt.Errorf("target credentials: %w", err)
	}

	return &xaaParsedConfig{
		idpTokenURL:         cfg.IDPTokenURL,
		idpClientID:         cfg.IDPClientID,
		idpClientSecret:     idpSecret,
		targetTokenURL:      cfg.TargetTokenURL,
		targetClientID:      cfg.TargetClientID,
		targetClientSecret:  targetSecret,
		targetAudience:      cfg.TargetAudience,
		targetResource:      cfg.TargetResource,
		scopes:              cfg.Scopes,
		subjectProviderName: cfg.SubjectProviderName,
		allowInsecureHTTP:   cfg.InsecureTargetTokenURL,
	}, nil
}
