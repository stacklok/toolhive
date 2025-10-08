package tokenexchange

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Middleware type constant
const (
	MiddlewareType = "tokenexchange"
)

// Header injection strategy constants
const (
	// HeaderStrategyReplace replaces the Authorization header with the exchanged token
	HeaderStrategyReplace = "replace"
	// HeaderStrategyCustom adds the exchanged token to a custom header
	HeaderStrategyCustom = "custom"
)

// MiddlewareParams represents the parameters for token exchange middleware
type MiddlewareParams struct {
	TokenExchangeConfig *Config `json:"token_exchange_config,omitempty"`
}

// Config holds configuration for token exchange middleware
type Config struct {
	// TokenURL is the OAuth 2.0 token endpoint URL
	TokenURL string `json:"token_url"`

	// ClientID is the OAuth 2.0 client identifier
	ClientID string `json:"client_id"`

	// ClientSecret is the OAuth 2.0 client secret
	ClientSecret string `json:"client_secret"`

	// Audience is the target audience for the exchanged token
	Audience string `json:"audience"`

	// Scopes is the list of scopes to request for the exchanged token
	Scopes []string `json:"scopes,omitempty"`

	// HeaderStrategy determines how to inject the token
	// Valid values: HeaderStrategyReplace (default), HeaderStrategyCustom
	HeaderStrategy string `json:"header_strategy,omitempty"`

	// ExternalTokenHeaderName is the name of the custom header to use when HeaderStrategy is "custom"
	ExternalTokenHeaderName string `json:"external_token_header_name,omitempty"`
}

// Middleware wraps token exchange middleware functionality
type Middleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*Middleware) Close() error {
	// Token exchange middleware doesn't need cleanup
	return nil
}

// CreateMiddleware factory function for token exchange middleware
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal token exchange middleware parameters: %w", err)
	}

	// If no token exchange config provided, skip middleware
	if params.TokenExchangeConfig == nil {
		logger.Debug("No token exchange config provided, skipping token exchange middleware")
		return nil
	}

	// Validate configuration
	if err := validateTokenExchangeConfig(params.TokenExchangeConfig); err != nil {
		return fmt.Errorf("invalid token exchange configuration: %w", err)
	}

	middleware := CreateTokenExchangeMiddlewareFromClaims(*params.TokenExchangeConfig)

	tokenExchangeMw := &Middleware{
		middleware: middleware,
	}

	// Add middleware to runner
	runner.AddMiddleware(tokenExchangeMw)

	return nil
}

// validateTokenExchangeConfig validates the token exchange configuration
func validateTokenExchangeConfig(config *Config) error {
	if config.HeaderStrategy == HeaderStrategyCustom && config.ExternalTokenHeaderName == "" {
		return fmt.Errorf("external_token_header_name must be specified when header_strategy is '%s'", HeaderStrategyCustom)
	}

	if config.HeaderStrategy != "" &&
		config.HeaderStrategy != HeaderStrategyReplace &&
		config.HeaderStrategy != HeaderStrategyCustom {
		return fmt.Errorf("invalid header_strategy: %s (valid values: '%s', '%s')",
			config.HeaderStrategy, HeaderStrategyReplace, HeaderStrategyCustom)
	}

	return nil
}

// injectToken handles token injection based on the configured strategy
func injectToken(r *http.Request, token string, config Config) error {
	strategy := config.HeaderStrategy
	if strategy == "" {
		strategy = HeaderStrategyReplace // Default to replace for backwards compatibility
	}

	switch strategy {
	case HeaderStrategyReplace:
		logger.Debugf("Token exchange successful, replacing Authorization header")
		r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		return nil

	case HeaderStrategyCustom:
		if config.ExternalTokenHeaderName == "" {
			return fmt.Errorf("external_token_header_name must be specified when header_strategy is '%s'", HeaderStrategyCustom)
		}
		logger.Debugf("Token exchange successful, adding token to custom header: %s", config.ExternalTokenHeaderName)
		r.Header.Set(config.ExternalTokenHeaderName, fmt.Sprintf("Bearer %s", token))
		return nil

	default:
		return fmt.Errorf("unsupported header_strategy: %s (valid values: '%s', '%s')",
			strategy, HeaderStrategyReplace, HeaderStrategyCustom)
	}
}

// CreateTokenExchangeMiddlewareFromClaims creates a middleware that uses token claims
// from the auth middleware to perform token exchange.
// This is a public function for direct usage in proxy commands.
func CreateTokenExchangeMiddlewareFromClaims(config Config) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get claims from the auth middleware
			claims, ok := r.Context().Value(auth.ClaimsContextKey{}).(jwt.MapClaims)
			if !ok {
				logger.Debug("No claims found in context, proceeding without token exchange")
				next.ServeHTTP(w, r)
				return
			}

			// Extract the original token from the Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
				logger.Debug("No valid Bearer token found, proceeding without token exchange")
				next.ServeHTTP(w, r)
				return
			}

			subjectToken := strings.TrimPrefix(authHeader, "Bearer ")
			if subjectToken == "" {
				logger.Debug("Empty Bearer token, proceeding without token exchange")
				next.ServeHTTP(w, r)
				return
			}

			// Log some claim information for debugging
			if sub, exists := claims["sub"]; exists {
				logger.Debugf("Performing token exchange for subject: %v", sub)
			}

			// Create RFC-8693 token exchange config with subject token provider
			exchangeConfig := &ExchangeConfig{
				TokenURL:     config.TokenURL,
				ClientID:     config.ClientID,
				ClientSecret: config.ClientSecret,
				Audience:     config.Audience,
				Scopes:       config.Scopes,
				SubjectTokenProvider: func() (string, error) {
					return subjectToken, nil
				},
			}

			// Get token from token source
			tokenSource := exchangeConfig.TokenSource(r.Context())
			exchangedToken, err := tokenSource.Token()
			if err != nil {
				logger.Warnf("Token exchange failed: %v", err)
				http.Error(w, "Token exchange failed", http.StatusUnauthorized)
				return
			}

			// Inject the exchanged token into the request
			if err := injectToken(r, exchangedToken.AccessToken, config); err != nil {
				logger.Warnf("Failed to inject token: %v", err)
				http.Error(w, "Token injection failed", http.StatusInternalServerError)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
