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
// The strategy uses the typed TokenExchangeConfig from BackendAuthStrategy with:
//   - TokenURL: The OAuth 2.0 token endpoint URL for token exchange (required)
//   - ClientID: OAuth 2.0 client identifier (optional, required for some token endpoints)
//   - ClientSecret: OAuth 2.0 client secret (optional, resolved from config or env)
//   - Audience: Target audience for the exchanged token (optional)
//   - Scopes: Array of scope strings to request (optional)
//   - SubjectTokenType: Type of the subject token (optional, default: "access_token")
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
	return "token_exchange"
}

// Authenticate exchanges the client's token for a backend token and injects it.
//
// This method:
//  1. Retrieves the client's identity and token from the context
//  2. Validates the token exchange configuration from the typed BackendAuthStrategy
//  3. Gets or creates a cached ExchangeConfig for this backend configuration
//  4. Creates a TokenSource with the current identity token
//  5. Obtains an access token by performing the exchange
//  6. Injects the token into the backend request's Authorization header
//
// Token caching per user is handled by the upper vMCP TokenCache layer.
// This strategy only caches the ExchangeConfig template per backend.
//
// Parameters:
//   - ctx: Request context containing the authenticated identity
//   - req: The HTTP request to authenticate
//   - strategy: The typed BackendAuthStrategy containing TokenExchange configuration
//
// Returns an error if:
//   - No identity is found in the context
//   - The identity has no token
//   - strategy or TokenExchange config is nil or invalid
//   - Token exchange fails
func (s *TokenExchangeStrategy) Authenticate(ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy) error {
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("no identity found in context")
	}

	if identity.Token == "" {
		return fmt.Errorf("identity has no token")
	}

	cfg, err := s.extractAndValidateConfig(strategy)
	if err != nil {
		return err
	}

	// Get user-specific exchange config. This creates a fresh config instance
	// with the current user's token. The underlying server config is cached.
	exchangeConfig := s.createUserConfig(cfg, identity.Token)
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
//   - strategy and TokenExchange config are not nil
//   - TokenURL is present and valid
//   - Optional fields (if present) have valid values
//   - ClientSecret/ClientSecretEnv is only provided when ClientID is present
//
// This validation is typically called during configuration parsing to fail fast
// if the strategy is misconfigured.
func (s *TokenExchangeStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
	_, err := s.extractAndValidateConfig(strategy)
	return err
}

// tokenExchangeConfig holds the resolved token exchange configuration.
// This is an internal representation used for caching and token exchange operations.
type tokenExchangeConfig struct {
	TokenURL         string
	ClientID         string
	ClientSecret     string
	Audience         string
	Scopes           []string
	SubjectTokenType string
}

// extractAndValidateConfig extracts and validates configuration from a typed BackendAuthStrategy.
// Returns the resolved tokenExchangeConfig, or an error if validation fails.
func (s *TokenExchangeStrategy) extractAndValidateConfig(strategy *authtypes.BackendAuthStrategy) (*tokenExchangeConfig, error) {
	if strategy == nil || strategy.TokenExchange == nil {
		return nil, fmt.Errorf("token_exchange configuration required")
	}

	cfg := strategy.TokenExchange
	result := &tokenExchangeConfig{}

	// Required: TokenURL
	if cfg.TokenURL == "" {
		return nil, fmt.Errorf("token_url required in token_exchange configuration")
	}
	result.TokenURL = cfg.TokenURL

	// Optional: ClientID
	result.ClientID = cfg.ClientID

	// Optional: ClientSecret or ClientSecretEnv
	clientSecret, err := s.resolveClientSecret(cfg)
	if err != nil {
		return nil, err
	}
	result.ClientSecret = clientSecret

	// Optional: Audience
	result.Audience = cfg.Audience

	// Optional: Scopes (already typed as []string)
	result.Scopes = cfg.Scopes

	// Optional: SubjectTokenType
	if cfg.SubjectTokenType != "" {
		normalized, err := tokenexchange.NormalizeTokenType(cfg.SubjectTokenType)
		if err != nil {
			return nil, fmt.Errorf("invalid subject_token_type: %w", err)
		}
		result.SubjectTokenType = normalized
	}

	return result, nil
}

// resolveClientSecret resolves the client secret from the typed TokenExchangeConfig.
// Returns the resolved client secret, or an error if validation fails.
func (s *TokenExchangeStrategy) resolveClientSecret(cfg *authtypes.TokenExchangeConfig) (string, error) {
	// ClientSecret takes precedence over ClientSecretEnv
	if cfg.ClientSecret != "" {
		if cfg.ClientID == "" {
			return "", fmt.Errorf("client_secret cannot be provided without client_id")
		}
		return cfg.ClientSecret, nil
	}

	// Check ClientSecretEnv
	if cfg.ClientSecretEnv != "" {
		if cfg.ClientID == "" {
			return "", fmt.Errorf("client_secret_env cannot be provided without client_id")
		}
		// Resolve the environment variable
		secret := s.envReader.Getenv(cfg.ClientSecretEnv)
		if secret == "" {
			return "", fmt.Errorf("environment variable %s not set or empty", cfg.ClientSecretEnv)
		}
		return secret, nil
	}

	// No client secret provided (which is valid)
	return "", nil
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
