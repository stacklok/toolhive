// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/env"
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
//  1. Skips authentication if this is a health check request
//  2. Retrieves the client's identity and token from the context
//  3. Parses and validates the token exchange configuration from strategy
//  4. Gets or creates a cached ExchangeConfig for this backend configuration
//  5. Creates a TokenSource with the current identity token
//  6. Obtains an access token by performing the exchange
//  7. Injects the token into the backend request's Authorization header
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
//   - No identity is found in the context
//   - The identity has no token
//   - Strategy configuration is invalid or incomplete
//   - Token exchange fails
func (s *TokenExchangeStrategy) Authenticate(
	ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
	// Skip authentication for health checks
	if health.IsHealthCheck(ctx) {
		return nil
	}

	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("no identity found in context")
	}

	if identity.Token == "" {
		return fmt.Errorf("identity has no token")
	}

	config, err := s.parseTokenExchangeConfig(strategy)
	if err != nil {
		return fmt.Errorf("invalid strategy configuration: %w", err)
	}

	// Get user-specific exchange config. This creates a fresh config instance
	// with the current user's token. The underlying server config is cached.
	exchangeConfig := s.createUserConfig(config, identity.Token)
	tokenSource := exchangeConfig.TokenSource(ctx)

	token, err := tokenSource.Token()
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	// Inject exchanged token into request
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
	ClientSecret     string
	Audience         string
	Scopes           []string
	SubjectTokenType string
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

	// Required: TokenURL
	if tokenExchangeCfg.TokenURL == "" {
		return nil, fmt.Errorf("TokenURL is required in token_exchange configuration")
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

	// Optional: SubjectTokenType
	if tokenExchangeCfg.SubjectTokenType != "" {
		// Validate if provided
		normalized, err := tokenexchange.NormalizeTokenType(tokenExchangeCfg.SubjectTokenType)
		if err != nil {
			return nil, fmt.Errorf("invalid SubjectTokenType: %w", err)
		}
		config.SubjectTokenType = normalized
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
//   - token_url: OAuth token endpoint
//   - client_id: OAuth client identifier
//   - audience: Target audience
//   - scopes: Requested scopes (sorted for consistency)
//   - subject_token_type: Type of subject token
//
// Note: No user identity is included - server configs are shared across users.
func buildCacheKey(config *tokenExchangeConfig) string {
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

	// Format: token_url:client_id:audience:scopes:subject_token_type
	return fmt.Sprintf("%s:%s:%s:%s:%s",
		config.TokenURL,
		clientID,
		audience,
		scopesStr,
		tokenType,
	)
}
