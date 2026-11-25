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

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/oauthproto/idjag"
	"github.com/stacklok/toolhive/pkg/oauthproto/jwtbearer"
	"github.com/stacklok/toolhive/pkg/oauthproto/tokenexchange"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// XAAStrategy implements XAA (Cross-Application Access) as a two-step token
// exchange, following draft-ietf-oauth-identity-assertion-authz-grant (ID-JAG):
//
//   - IdP exchange (RFC 8693): Exchange the user's ID token at the IdP for an ID-JAG JWT.
//   - Target grant (RFC 7523): Exchange the ID-JAG at the target AS for an access token.
//
// Both steps run on every Authenticate call. The upper vMCP TokenCache layer is
// responsible for reusing the resulting access token across requests; this
// strategy holds no local cache.
//
// The subject ID token is not validated locally before IdP exchange. The IdP
// enforces its own exp check; if the token is expired, IdP exchange returns an
// OAuth invalid_grant error which propagates to the caller. Callers that need
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
//     UpstreamIDTokens map, performs IdP exchange (token exchange for ID-JAG) and
//     target grant (JWT Bearer grant for access token), then injects the access token
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
//   - IdP exchange (token exchange) or target grant (JWT Bearer grant) fails
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

	// IdP exchange: Exchange the user's ID token for an ID-JAG at the IdP.
	assertion, err := s.performIDPExchange(ctx, idToken, config)
	if err != nil {
		slog.Debug("xaa: IdP exchange failed", "error", err)
		return fmt.Errorf("IdP exchange failed: %w", err)
	}

	slog.Debug("xaa: IdP exchange succeeded, got ID-JAG")

	// Target grant: Exchange the ID-JAG for an access token at the target AS.
	accessToken, err := s.performTargetGrant(ctx, assertion, config)
	if err != nil {
		slog.Debug("xaa: target grant failed", "error", err)
		return fmt.Errorf("target grant failed: %w", err)
	}

	slog.Debug("xaa: target grant succeeded, setting Bearer token")

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
	config, err := s.parseXAAConfig(strategy)
	if err != nil {
		return err
	}

	// Emit configuration warnings here rather than in parseXAAConfig: Validate
	// runs once per backend at wire-up, whereas parseXAAConfig also runs on every
	// Authenticate call and would spam these on every request.
	if config.allowInsecureHTTP {
		slog.Warn("xaa: plain HTTP enabled for TargetTokenURL; do not use in production",
			"targetTokenURL", config.targetTokenURL,
		)
	}
	if config.targetClientID == "" {
		slog.Warn("xaa: target grant will run without client authentication; " +
			"most authorization servers reject unauthenticated JWT-bearer grants " +
			"(ID-JAG draft §9.1 recommends confidential clients)")
	}

	return nil
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
	subjectTokenType    string
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

// performIDPExchange runs the RFC 8693 token exchange at the IdP to obtain an
// ID-JAG, then hands the response to idjag.FromExchange, which checks it
// against what was requested (draft §4.3.4): issued_token_type, token_type,
// a parsable JWT, and the aud and resource binding claims (confused-deputy
// defence).
func (*XAAStrategy) performIDPExchange(
	ctx context.Context, idToken string, config *xaaParsedConfig,
) (string, error) {
	exchangeCfg := &tokenexchange.ExchangeConfig{
		TokenURL:           config.idpTokenURL,
		ClientID:           config.idpClientID,
		ClientSecret:       config.idpClientSecret,
		SubjectTokenType:   config.subjectTokenType,
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
		return "", fmt.Errorf("IdP exchange: %w", err)
	}

	assertion, err := idjag.FromExchange(token, config.targetAudience, config.targetResource)
	if err != nil {
		return "", fmt.Errorf("IdP exchange: %w", err)
	}
	return assertion, nil
}

// performTargetGrant runs the RFC 7523 JWT Bearer grant at the target AS using
// the ID-JAG assertion returned by IdP exchange.
func (*XAAStrategy) performTargetGrant(
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
		return "", fmt.Errorf("target grant: %w", err)
	}

	return token.AccessToken, nil
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
	if err := jwtbearer.ValidateTokenURL("IDPTokenURL", cfg.IDPTokenURL, false); err != nil {
		return nil, err
	}
	if err := jwtbearer.ValidateTokenURL("TargetTokenURL", cfg.TargetTokenURL, cfg.InsecureTargetTokenURL); err != nil {
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

	subjectTokenType := cfg.SubjectTokenType
	if subjectTokenType == "" {
		subjectTokenType = oauthproto.TokenTypeIDToken
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
		subjectTokenType:    subjectTokenType,
		allowInsecureHTTP:   cfg.InsecureTargetTokenURL,
	}, nil
}
