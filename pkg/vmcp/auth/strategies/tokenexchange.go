package strategies

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
)

// TokenExchangeStrategy exchanges the client's token for a backend-specific token
// using RFC 8693 token exchange protocol.
//
// This strategy implements OAuth 2.0 Token Exchange (RFC 8693) to convert a client's
// token (typically from the vMCP server's identity provider) into a backend-specific
// token that the backend MCP server can validate.
//
// The strategy caches TokenSource instances per (identity, backend) combination to
// avoid redundant token exchanges. The oauth2.TokenSource automatically handles:
//   - Token caching
//   - Token refresh based on expiry
//   - Concurrent access safety
//
// Required metadata fields:
//   - token_url: The OAuth 2.0 token endpoint URL for token exchange
//
// Optional metadata fields:
//   - client_id: OAuth 2.0 client identifier (required for some token endpoints)
//   - client_secret: OAuth 2.0 client secret (required if client_id is provided)
//   - audience: Target audience for the exchanged token
//   - scopes: Array of scope strings to request
//   - subject_token_type: Type of the subject token (default: "access_token")
//
// This strategy is appropriate when:
//   - The backend uses a different identity provider than the vMCP server
//   - Token exchange relationships are configured between the identity providers
//   - Per-user token exchange is required (not static credentials)
type TokenExchangeStrategy struct {
	// tokenSources caches TokenSource instances by cache key
	// Key format: "subject:token_url:audience:scopes"
	tokenSources map[string]oauth2.TokenSource
	mu           sync.RWMutex
}

// NewTokenExchangeStrategy creates a new TokenExchangeStrategy instance.
func NewTokenExchangeStrategy() *TokenExchangeStrategy {
	return &TokenExchangeStrategy{
		tokenSources: make(map[string]oauth2.TokenSource),
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
//  2. Parses and validates the token exchange configuration from metadata
//  3. Gets or creates a cached TokenSource for this (identity, backend) pair
//  4. Obtains an access token (from cache or via exchange)
//  5. Injects the token into the backend request's Authorization header
//
// The oauth2.TokenSource handles token caching and refresh automatically.
//
// Parameters:
//   - ctx: Request context containing the authenticated identity
//   - req: The HTTP request to authenticate
//   - metadata: Strategy-specific configuration for token exchange
//
// Returns an error if:
//   - No identity is found in the context
//   - The identity has no token
//   - Metadata is invalid or incomplete
//   - Token exchange fails
func (s *TokenExchangeStrategy) Authenticate(ctx context.Context, req *http.Request, metadata map[string]any) error {
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("no identity found in context")
	}

	if identity.Token == "" {
		return fmt.Errorf("identity has no token")
	}

	config, err := parseTokenExchangeMetadata(metadata)
	if err != nil {
		return fmt.Errorf("invalid metadata: %w", err)
	}

	// Get or create TokenSource for this (identity, backend)
	tokenSource := s.getOrCreateTokenSource(ctx, identity, config)

	// Get token (automatically handles caching and refresh)
	token, err := tokenSource.Token()
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	// Inject exchanged token into request
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	return nil
}

// Validate checks if the required metadata fields are present and valid.
//
// This method verifies that:
//   - token_url is present and valid
//   - Optional fields (if present) have correct types and values
//   - client_secret is only provided when client_id is present
//
// This validation is typically called during configuration parsing to fail fast
// if the strategy is misconfigured.
func (*TokenExchangeStrategy) Validate(metadata map[string]any) error {
	_, err := parseTokenExchangeMetadata(metadata)
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

// parseTokenExchangeMetadata parses and validates token exchange metadata.
func parseTokenExchangeMetadata(metadata map[string]any) (*tokenExchangeConfig, error) {
	config := &tokenExchangeConfig{}

	// Required: token_url
	tokenURL, ok := metadata["token_url"].(string)
	if !ok || tokenURL == "" {
		return nil, fmt.Errorf("token_url required in metadata")
	}
	config.TokenURL = tokenURL

	// Optional: client_id
	if clientID, ok := metadata["client_id"].(string); ok {
		config.ClientID = clientID
	}

	// Optional: client_secret (only valid if client_id is present)
	if clientSecret, ok := metadata["client_secret"].(string); ok {
		if config.ClientID == "" {
			return nil, fmt.Errorf("client_secret cannot be provided without client_id")
		}
		config.ClientSecret = clientSecret
	}

	// Optional: audience
	if audience, ok := metadata["audience"].(string); ok {
		config.Audience = audience
	}

	// Optional: scopes (can be string array or single string)
	if scopesRaw, ok := metadata["scopes"]; ok {
		switch v := scopesRaw.(type) {
		case []string:
			config.Scopes = v
		case []any:
			// Handle case where JSON unmarshaling gives []any
			scopes := make([]string, 0, len(v))
			for i, s := range v {
				str, ok := s.(string)
				if !ok {
					return nil, fmt.Errorf("scopes[%d] must be a string", i)
				}
				scopes = append(scopes, str)
			}
			config.Scopes = scopes
		case string:
			// Single scope as string
			config.Scopes = []string{v}
		default:
			return nil, fmt.Errorf("scopes must be a string or array of strings")
		}
	}

	// Optional: subject_token_type
	if subjectTokenType, ok := metadata["subject_token_type"].(string); ok {
		// Validate if provided
		normalized, err := tokenexchange.NormalizeTokenType(subjectTokenType)
		if err != nil {
			return nil, fmt.Errorf("invalid subject_token_type: %w", err)
		}
		config.SubjectTokenType = normalized
	}

	return config, nil
}

// getOrCreateTokenSource returns a cached TokenSource or creates a new one.
// This method is thread-safe and uses double-checked locking for efficiency.
func (s *TokenExchangeStrategy) getOrCreateTokenSource(
	ctx context.Context,
	identity *auth.Identity,
	config *tokenExchangeConfig,
) oauth2.TokenSource {
	// Create cache key from identity and config
	cacheKey := buildCacheKey(identity.Subject, config)

	// Fast path: check if TokenSource already exists (read lock)
	s.mu.RLock()
	if ts, exists := s.tokenSources[cacheKey]; exists {
		s.mu.RUnlock()
		return ts
	}
	s.mu.RUnlock()

	// Slow path: create new TokenSource (write lock)
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check in case another goroutine created it
	if ts, exists := s.tokenSources[cacheKey]; exists {
		return ts
	}

	// Create new TokenSource with automatic caching and refresh
	exchangeConfig := &tokenexchange.ExchangeConfig{
		TokenURL:     config.TokenURL,
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		Audience:     config.Audience,
		Scopes:       config.Scopes,
		SubjectTokenProvider: func() (string, error) {
			return identity.Token, nil
		},
		SubjectTokenType: config.SubjectTokenType,
	}

	// Wrap with ReuseTokenSource for automatic caching and refresh
	tokenSource := oauth2.ReuseTokenSource(nil, exchangeConfig.TokenSource(ctx))
	s.tokenSources[cacheKey] = tokenSource

	return tokenSource
}

// buildCacheKey creates a unique cache key for a (identity, config) pair.
func buildCacheKey(subject string, config *tokenExchangeConfig) string {
	// Simple key format: subject:token_url:audience
	// This ensures separate caches for different backends and users
	return fmt.Sprintf("%s:%s:%s", subject, config.TokenURL, config.Audience)
}
