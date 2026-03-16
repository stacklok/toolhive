// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"golang.org/x/oauth2/clientcredentials"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

const (
	// nonePlaceholder is used to represent empty or missing values in cache keys
	nonePlaceholder = "<none>"
)

// TokenExchangeStrategy exchanges the client's token for a backend-specific token
// using RFC 8693 token exchange protocol.
//
// This strategy implements OAuth 2.0 Token Exchange (RFC 8693) to convert a client's
// token into a backend-specific token that the backend MCP server can validate.
//
// The strategy caches ExchangeConfig instances per backend configuration to avoid
// recreating configuration objects. Per-user token caching is handled by the upper
// vMCP TokenCache layer.
//
// Required metadata fields:
//   - token_url: The OAuth 2.0 token endpoint URL for token exchange
//
// Optional metadata fields:
//   - client_id: OAuth 2.0 client identifier (required for some token endpoints)
//   - client_secret: OAuth 2.0 client secret (directly provided, mutually exclusive with client_secret_env)
//   - client_secret_env: Name of environment variable containing the client secret (mutually exclusive with client_secret)
//   - audience: Target audience for the exchanged token
//   - scopes: Array of scope strings to request
//   - subject_token_type: Type of the subject token (default: "access_token")
//
// This strategy is appropriate when:
//   - The backend uses a different identity provider than the vMCP server
//   - Token exchange relationships are configured between the identity providers
//   - Per-user token exchange is required (not static credentials)
type TokenExchangeStrategy struct {
	// exchangeConfigs caches server-level ExchangeConfig templates.
	// Key: buildCacheKey(config) - one entry per backend server.
	// Each template is shared across all users connecting to that server.
	exchangeConfigs map[string]*tokenexchange.ExchangeConfig
	mu              sync.RWMutex
	envReader       env.Reader
}

// NewTokenExchangeStrategy creates a new TokenExchangeStrategy instance.
func NewTokenExchangeStrategy(envReader env.Reader) *TokenExchangeStrategy {
	return &TokenExchangeStrategy{
		exchangeConfigs: make(map[string]*tokenexchange.ExchangeConfig),
		envReader:       envReader,
	}
}

// Name returns the strategy identifier.
func (*TokenExchangeStrategy) Name() string {
	return authtypes.StrategyTypeTokenExchange
}

// Authenticate exchanges the client's token for a backend token and injects it.
//
// This method:
//  1. Parses and validates the token exchange configuration from strategy
//  2. For health check requests: uses a client credentials grant if client_id and
//     client_secret are configured; otherwise skips authentication
//  3. For regular requests: retrieves the client's identity and token from the context,
//     gets or creates a cached ExchangeConfig, performs the token exchange, and injects
//     the token into the backend request's Authorization header
//
// Token caching per user is handled by the upper vMCP TokenCache layer.
// This strategy only caches the ExchangeConfig template per backend.
//
// Parameters:
//   - ctx: Request context containing the authenticated identity (or health check marker)
//   - req: The HTTP request to authenticate
//   - strategy: Backend auth strategy containing token exchange configuration
//
// Returns an error if:
//   - Strategy configuration is invalid or incomplete
//   - No identity is found in the context (regular requests only)
//   - The identity has no token (regular requests only)
//   - Token exchange or client credentials grant fails
func (s *TokenExchangeStrategy) Authenticate(
	ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
	config, err := s.parseTokenExchangeConfig(strategy)
	if err != nil {
		return fmt.Errorf("invalid strategy configuration: %w", err)
	}

	// For health checks there is no user identity to exchange. If client credentials
	// are configured, use a client credentials grant to authenticate the probe request.
	// Otherwise skip authentication — the backend will be probed unauthenticated.
	if health.IsHealthCheck(ctx) {
		if config.ClientID != "" && config.ClientSecret != "" {
			return s.authenticateWithClientCredentials(ctx, req, config)
		}
		return nil
	}

	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("no identity found in context")
	}

	if identity.Token == "" {
		return fmt.Errorf("identity has no token")
	}

	// Get user-specific exchange config. This creates a fresh config instance
	// with the current user's token. The underlying server config is cached.
	exchangeConfig := s.createUserConfig(config, identity.Token)
	tokenSource, err := exchangeConfig.TokenSource(ctx)
	if err != nil {
		return fmt.Errorf("token exchange configuration failed: %w", err)
	}

	token, err := tokenSource.Token()
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	// Inject exchanged token into request
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	return nil
}

// authenticateWithClientCredentials performs an OAuth2 client credentials grant and
// injects the resulting token into the request. Used for health check probes when
// client_id and client_secret are configured.
func (*TokenExchangeStrategy) authenticateWithClientCredentials(
	ctx context.Context, req *http.Request, config *tokenExchangeConfig,
) error {
	ccConfig := clientcredentials.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		TokenURL:     config.TokenURL,
		Scopes:       config.Scopes,
	}
	if config.Audience != "" {
		ccConfig.EndpointParams = url.Values{"audience": {config.Audience}}
	}

	token, err := ccConfig.Token(ctx)
	if err != nil {
		return fmt.Errorf("client credentials grant failed: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	return nil
}

// Validate checks if the required configuration fields are present and valid.
//
// This method verifies that:
//   - TokenURL is present and valid
//   - Optional fields (if present) have correct types and values
//   - ClientSecret is only provided when ClientID is present
//
// For named variants (e.g., entra), some fields like TokenURL may be optional.
//
// This validation is typically called during configuration parsing to fail fast
// if the strategy is misconfigured.
func (s *TokenExchangeStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
	_, err := s.parseTokenExchangeConfig(strategy)
	return err
}

// tokenExchangeConfig holds the parsed token exchange configuration.
type tokenExchangeConfig struct {
	TokenURL         string
	ClientID         string
	ClientSecret     string //nolint:gosec // G117: field legitimately holds sensitive data
	Audience         string
	Scopes           []string
	SubjectTokenType string
	Variant          string
	RawConfig        *tokenexchange.RawExchangeConfig
}

// parseClientSecret parses and validates ClientSecret or ClientSecretEnv from TokenExchangeConfig.
// Returns the resolved client secret, or an error if validation fails.
func (s *TokenExchangeStrategy) parseClientSecret(config *authtypes.TokenExchangeConfig, clientID string) (string, error) {
	// Check for ClientSecret first (takes precedence)
	if config.ClientSecret != "" {
		if clientID == "" {
			return "", fmt.Errorf("ClientSecret cannot be provided without ClientID")
		}
		return config.ClientSecret, nil
	}

	// Check for ClientSecretEnv
	if config.ClientSecretEnv != "" {
		if clientID == "" {
			return "", fmt.Errorf("ClientSecretEnv cannot be provided without ClientID")
		}
		// Resolve the environment variable
		secret := s.envReader.Getenv(config.ClientSecretEnv)
		if secret == "" {
			return "", fmt.Errorf("environment variable %s not set or empty", config.ClientSecretEnv)
		}
		return secret, nil
	}

	// No client secret provided (which is valid)
	return "", nil
}

// parseTokenExchangeConfig parses and validates token exchange configuration from BackendAuthStrategy.
func (s *TokenExchangeStrategy) parseTokenExchangeConfig(strategy *authtypes.BackendAuthStrategy) (*tokenExchangeConfig, error) {
	if strategy == nil || strategy.TokenExchange == nil {
		return nil, fmt.Errorf("TokenExchange configuration is required")
	}

	config := &tokenExchangeConfig{}
	tokenExchangeCfg := strategy.TokenExchange

	// Normalize variant to lowercase to match VariantRegistry and config validator behavior.
	variant := strings.ToLower(tokenExchangeCfg.Variant)

	// TokenURL is required for standard RFC 8693 and "raw" variants.
	// Named variants (e.g., "entra") may derive tokenUrl from their parameters.
	if variant == "" || variant == authtypes.TokenExchangeVariantRaw {
		if tokenExchangeCfg.TokenURL == "" {
			return nil, fmt.Errorf("TokenURL is required in token_exchange configuration")
		}
	}
	config.TokenURL = tokenExchangeCfg.TokenURL

	// Optional: ClientID
	config.ClientID = tokenExchangeCfg.ClientID

	// Optional: ClientSecret or ClientSecretEnv
	clientSecret, err := s.parseClientSecret(tokenExchangeCfg, config.ClientID)
	if err != nil {
		return nil, err
	}
	config.ClientSecret = clientSecret

	// Optional: Audience
	config.Audience = tokenExchangeCfg.Audience

	// Optional: Scopes (already parsed as []string from the typed config)
	if len(tokenExchangeCfg.Scopes) > 0 {
		config.Scopes = tokenExchangeCfg.Scopes
	}

	// SubjectTokenType normalization only for standard RFC 8693
	if variant == "" && tokenExchangeCfg.SubjectTokenType != "" {
		normalized, err := tokenexchange.NormalizeTokenType(tokenExchangeCfg.SubjectTokenType)
		if err != nil {
			return nil, fmt.Errorf("invalid SubjectTokenType: %w", err)
		}
		config.SubjectTokenType = normalized
	} else {
		config.SubjectTokenType = tokenExchangeCfg.SubjectTokenType
	}

	// Variant passthrough (already normalized to lowercase above)
	config.Variant = variant

	// Map types.TokenExchangeRawAuthConfig → tokenexchange.RawExchangeConfig.
	// Deep-copy the Parameters map to prevent aliasing with the cached server template.
	if tokenExchangeCfg.Raw != nil {
		var params map[string]string
		if tokenExchangeCfg.Raw.Parameters != nil {
			params = make(map[string]string, len(tokenExchangeCfg.Raw.Parameters))
			for k, v := range tokenExchangeCfg.Raw.Parameters {
				params[k] = v
			}
		}
		config.RawConfig = &tokenexchange.RawExchangeConfig{
			GrantTypeURN: tokenExchangeCfg.Raw.GrantTypeURN,
			Parameters:   params,
		}
	}

	return config, nil
}

// getOrCreateServerConfig retrieves or creates a cached server-level ExchangeConfig.
//
// Server configs are cached per backend and shared across all users. This prevents
// redundant config parsing and validation. The cached config does NOT include
// SubjectTokenProvider - that's set per-user in createUserConfig().
//
// Thread-safe: Uses double-checked locking pattern.
func (s *TokenExchangeStrategy) getOrCreateServerConfig(
	config *tokenExchangeConfig,
) *tokenexchange.ExchangeConfig {
	cacheKey := buildCacheKey(config)

	// Fast path: read lock
	s.mu.RLock()
	if cached, exists := s.exchangeConfigs[cacheKey]; exists {
		s.mu.RUnlock()
		return cached
	}
	s.mu.RUnlock()

	// Slow path: write lock
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check in case another goroutine created it
	if cached, exists := s.exchangeConfigs[cacheKey]; exists {
		return cached
	}

	// Create template (without SubjectTokenProvider)
	template := &tokenexchange.ExchangeConfig{
		TokenURL:         config.TokenURL,
		ClientID:         config.ClientID,
		ClientSecret:     config.ClientSecret,
		Audience:         config.Audience,
		Scopes:           config.Scopes,
		SubjectTokenType: config.SubjectTokenType,
		Variant:          config.Variant,
		RawConfig:        config.RawConfig,
	}

	s.exchangeConfigs[cacheKey] = template
	return template
}

// createUserConfig creates a user-specific ExchangeConfig instance.
//
// This function:
//  1. Gets the cached server config template
//  2. Creates a copy for this user
//  3. Sets SubjectTokenProvider to return the user's token
//
// The identityToken parameter is a string value (not reference) to ensure
// the closure captures an immutable value, preventing bugs if the token
// changes after this call.
func (s *TokenExchangeStrategy) createUserConfig(
	config *tokenExchangeConfig,
	identityToken string,
) *tokenexchange.ExchangeConfig {
	// Get cached server template
	serverTemplate := s.getOrCreateServerConfig(config)

	// Create user-specific copy
	userConfig := *serverTemplate
	userConfig.SubjectTokenProvider = func() (string, error) {
		return identityToken, nil
	}

	return &userConfig
}

// buildCacheKey creates a unique cache key for server-level configs.
// The key includes all parameters that differentiate backend servers:
//   - variant: Token exchange variant (e.g., "entra", "raw", or empty for RFC 8693)
//   - token_url: OAuth token endpoint
//   - client_id: OAuth client identifier
//   - audience: Target audience
//   - scopes: Requested scopes (sorted for consistency)
//   - subject_token_type: Type of subject token
//   - raw_config: Variant-specific extension parameters (GrantTypeURN + sorted Parameters)
//
// Note: No user identity is included - server configs are shared across users.
func buildCacheKey(config *tokenExchangeConfig) string {
	// Handle variant (empty becomes nonePlaceholder)
	variant := config.Variant
	if variant == "" {
		variant = nonePlaceholder
	}

	// Handle client_id (empty becomes nonePlaceholder)
	clientID := config.ClientID
	if clientID == "" {
		clientID = nonePlaceholder
	}

	// Handle audience (empty becomes nonePlaceholder)
	audience := config.Audience
	if audience == "" {
		audience = nonePlaceholder
	}

	// Handle scopes (sort and join, empty becomes nonePlaceholder)
	scopesStr := nonePlaceholder
	if len(config.Scopes) > 0 {
		sortedScopes := make([]string, len(config.Scopes))
		copy(sortedScopes, config.Scopes)
		sort.Strings(sortedScopes)
		scopesStr = strings.Join(sortedScopes, ",")
	}

	// Handle subject_token_type (empty becomes nonePlaceholder)
	tokenType := config.SubjectTokenType
	if tokenType == "" {
		tokenType = nonePlaceholder
	}

	// Handle RawConfig (GrantTypeURN + sorted Parameters)
	rawStr := nonePlaceholder
	if config.RawConfig != nil {
		parts := []string{config.RawConfig.GrantTypeURN}
		if len(config.RawConfig.Parameters) > 0 {
			keys := make([]string, 0, len(config.RawConfig.Parameters))
			for k := range config.RawConfig.Parameters {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				parts = append(parts, k+"="+config.RawConfig.Parameters[k])
			}
		}
		rawStr = strings.Join(parts, ";")
	}

	// Format: variant:token_url:client_id:audience:scopes:subject_token_type:raw_config
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s:%s",
		variant,
		config.TokenURL,
		clientID,
		audience,
		scopesStr,
		tokenType,
		rawStr,
	)
}
