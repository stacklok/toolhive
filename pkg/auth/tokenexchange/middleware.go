package tokenexchange

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

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

// Environment variable names
const (
	// EnvClientSecret is the environment variable name for the OAuth client secret
	// This corresponds to the "client_secret" field in the token exchange configuration
	//nolint:gosec // G101: This is an environment variable name, not a credential
	EnvClientSecret = "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET"
)

var errUnknownStrategy = errors.New("unknown token injection strategy")

// envGetter is a function that retrieves environment variables
// This can be overridden for testing
type envGetter func(string) string

// defaultEnvGetter is the default environment variable getter using os.Getenv
var defaultEnvGetter envGetter = os.Getenv

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

	// SubjectTokenType specifies the type of the subject token being exchanged.
	// Common values: tokenTypeAccessToken (default), tokenTypeIDToken, tokenTypeJWT.
	// If empty, defaults to tokenTypeAccessToken.
	SubjectTokenType string `json:"subject_token_type,omitempty"`

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

	// Token exchange config is required when this middleware type is specified
	if params.TokenExchangeConfig == nil {
		return fmt.Errorf("token exchange configuration is required but not provided")
	}

	// Validate configuration
	if err := validateTokenExchangeConfig(params.TokenExchangeConfig); err != nil {
		return fmt.Errorf("invalid token exchange configuration: %w", err)
	}

	middleware, err := createTokenExchangeMiddleware(*params.TokenExchangeConfig, nil, defaultEnvGetter)
	if err != nil {
		return fmt.Errorf("invalid token exchange middleware config: %w", err)
	}

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

// injectionFunc is a function that injects a token into an HTTP request
type injectionFunc func(*http.Request, string) error

// createReplaceInjector creates an injection function that replaces the Authorization header
func createReplaceInjector() injectionFunc {
	return func(r *http.Request, token string) error {
		logger.Debugf("Token exchange successful, replacing Authorization header")
		r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		return nil
	}
}

// createCustomInjector creates an injection function that adds the token to a custom header
func createCustomInjector(headerName string) injectionFunc {
	// Validate header name at creation time
	if headerName == "" {
		return func(_ *http.Request, _ string) error {
			return fmt.Errorf("external_token_header_name must be specified when header_strategy is '%s'", HeaderStrategyCustom)
		}
	}

	return func(r *http.Request, token string) error {
		logger.Debugf("Token exchange successful, adding token to custom header: %s", headerName)
		r.Header.Set(headerName, fmt.Sprintf("Bearer %s", token))
		return nil
	}
}

// SubjectTokenProvider is a function that provides the subject token for exchange.
// This is used when the token comes from an external source (e.g., OAuth flow)
// rather than from incoming request headers.
type SubjectTokenProvider func() (string, error)

// CreateMiddlewareFromHeader creates token exchange middleware that extracts
// the subject token from the incoming request's Authorization header.
// This is the recommended approach when the proxy receives authenticated requests
// and needs to exchange those tokens for backend access.
//
// For external authentication flows (OAuth/OIDC), use CreateMiddlewareFromTokenSource instead.
func CreateMiddlewareFromHeader(config Config) (types.MiddlewareFunction, error) {
	return createTokenExchangeMiddleware(config, nil, defaultEnvGetter)
}

// CreateMiddlewareFromTokenSource creates token exchange middleware using an oauth2.TokenSource.
// This is the recommended approach for external authentication flows (OAuth/OIDC).
//
// The middleware will automatically select the appropriate token based on config.SubjectTokenType:
//   - tokenTypeAccessToken: Uses token.AccessToken
//   - tokenTypeIDToken or tokenTypeJWT: Uses token.Extra("id_token")
//
// This moves the token selection logic into the middleware where it belongs,
// keeping the command layer focused on configuration.
func CreateMiddlewareFromTokenSource(
	config Config,
	tokenSource oauth2.TokenSource,
) (types.MiddlewareFunction, error) {
	if tokenSource == nil {
		return nil, fmt.Errorf("tokenSource cannot be nil")
	}

	// Validate SubjectTokenType early to catch configuration errors at startup
	if config.SubjectTokenType != "" &&
		config.SubjectTokenType != tokenTypeAccessToken &&
		config.SubjectTokenType != tokenTypeIDToken &&
		config.SubjectTokenType != tokenTypeJWT {
		return nil, fmt.Errorf("invalid SubjectTokenType: %s (must be one of: %s, %s, %s)",
			config.SubjectTokenType, tokenTypeAccessToken, tokenTypeIDToken, tokenTypeJWT)
	}

	// Create a SubjectTokenProvider that handles token selection based on config
	subjectTokenProvider := func() (string, error) {
		token, err := tokenSource.Token()
		if err != nil {
			return "", fmt.Errorf("failed to get token: %w", err)
		}

		// Select appropriate token based on configured type
		switch config.SubjectTokenType {
		case tokenTypeIDToken:
			// Extract ID token from Extra field (standard OIDC approach)
			idToken, ok := token.Extra("id_token").(string)
			if !ok || idToken == "" {
				logger.Error("ID token not available in token response")
				return "", errors.New("required token not available")
			}
			return idToken, nil

		case "", tokenTypeAccessToken:
			// Use access token (default)
			if token.AccessToken == "" {
				logger.Error("Access token not available")
				return "", errors.New("required token not available")
			}
			return token.AccessToken, nil

		default:
			// This should never happen due to early validation, but handle defensively
			logger.Errorf("Invalid subject token type: %s", config.SubjectTokenType)
			return "", errors.New("invalid token configuration")
		}
	}

	return createTokenExchangeMiddleware(config, subjectTokenProvider, defaultEnvGetter)
}

// createTokenExchangeMiddleware is the internal implementation that accepts an envGetter
// This allows for dependency injection in tests
func createTokenExchangeMiddleware(
	config Config,
	subjectTokenProvider SubjectTokenProvider,
	getEnv envGetter,
) (types.MiddlewareFunction, error) {
	// Determine injection strategy at startup time
	strategy := config.HeaderStrategy
	if strategy == "" {
		strategy = HeaderStrategyReplace // Default to replace for backwards compatibility
	}

	var injectToken injectionFunc
	switch strategy {
	case HeaderStrategyReplace:
		injectToken = createReplaceInjector()
	case HeaderStrategyCustom:
		injectToken = createCustomInjector(config.ExternalTokenHeaderName)
	default:
		return nil, fmt.Errorf("%w: invalid header injection strategy %s", errUnknownStrategy, strategy)
	}

	// Resolve client secret from config or environment variable
	clientSecret := config.ClientSecret
	if clientSecret == "" {
		// If not provided in config, try to read from environment variable
		if envSecret := getEnv(EnvClientSecret); envSecret != "" {
			clientSecret = envSecret
			logger.Debug("Using client secret from environment variable")
		}
	}

	// Create base exchange config at startup time with all static fields
	baseExchangeConfig := ExchangeConfig{
		TokenURL:         config.TokenURL,
		ClientID:         config.ClientID,
		ClientSecret:     clientSecret,
		Audience:         config.Audience,
		Scopes:           config.Scopes,
		SubjectTokenType: config.SubjectTokenType,
		// SubjectTokenProvider will be set per request
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get claims from the auth middleware
			claims, ok := r.Context().Value(auth.ClaimsContextKey{}).(jwt.MapClaims)
			if !ok {
				logger.Debug("No claims found in context, proceeding without token exchange")
				next.ServeHTTP(w, r)
				return
			}

			var tokenProvider SubjectTokenProvider

			// Determine token source based on whether external provider was given
			if subjectTokenProvider != nil {
				// if the subjectTokenProvider is provided, use it, e.g. for passing in id_tokens
				logger.Debug("Using provided token source for token exchange")
				tokenProvider = subjectTokenProvider
			} else {
				// otherwise, extract token from incoming request's Authorization header
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

				tokenProvider = func() (string, error) {
					return subjectToken, nil
				}
			}

			// Log some claim information for debugging
			if sub, exists := claims["sub"]; exists {
				logger.Debugf("Performing token exchange for subject: %v", sub)
			}

			// Create a copy of the base config with the request-specific subject token
			exchangeConfig := baseExchangeConfig
			exchangeConfig.SubjectTokenProvider = tokenProvider

			// Get token from token source
			tokenSource := exchangeConfig.TokenSource(r.Context())
			exchangedToken, err := tokenSource.Token()
			if err != nil {
				logger.Warnf("Token exchange failed: %v", err)
				http.Error(w, "Token exchange failed", http.StatusUnauthorized)
				return
			}

			// Inject the exchanged token into the request using the pre-selected strategy
			if err := injectToken(r, exchangedToken.AccessToken); err != nil {
				logger.Warnf("Failed to inject token: %v", err)
				http.Error(w, "Token injection failed", http.StatusInternalServerError)
				return
			}

			next.ServeHTTP(w, r)
		})
	}, nil
}
