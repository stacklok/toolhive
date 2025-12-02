// Package types provides shared auth-related types for Virtual MCP Server.
//
// This package is designed as a leaf package with no dependencies on other
// pkg/vmcp/* packages, breaking potential import cycles between config,
// strategies, and other auth-related packages.
//
// Types defined here include:
//   - Strategy type constants (StrategyTypeUnauthenticated, etc.)
//   - Backend auth configuration structs (BackendAuthStrategy, etc.)
package types

// Strategy type identifiers used to identify authentication strategies.
const (
	// StrategyTypeUnauthenticated identifies the unauthenticated strategy.
	// This strategy performs no authentication and is used when a backend
	// requires no authentication.
	StrategyTypeUnauthenticated = "unauthenticated"

	// StrategyTypeHeaderInjection identifies the header injection strategy.
	// This strategy injects a static header value into request headers.
	StrategyTypeHeaderInjection = "header_injection"

	// StrategyTypeTokenExchange identifies the token exchange strategy.
	// This strategy exchanges an incoming token for a new token to use
	// when authenticating to the backend service.
	StrategyTypeTokenExchange = "token_exchange"

	// StrategyTypeExternalAuthConfigRef identifies an auth strategy that references
	// an external MCPExternalAuthConfig resource. This type is used in configuration
	// and must be resolved at runtime to a concrete strategy (token_exchange or header_injection).
	StrategyTypeExternalAuthConfigRef = "external_auth_config_ref"
)

// BackendAuthStrategy defines how to authenticate to a specific backend.
//
// This struct provides type-safe configuration for different authentication strategies
// using HeaderInjection or TokenExchange fields based on the Type field.
type BackendAuthStrategy struct {
	// Type is the auth strategy: "unauthenticated", "header_injection", "token_exchange", or "external_auth_config_ref"
	Type string `json:"type" yaml:"type"`

	// HeaderInjection contains configuration for header injection auth strategy.
	// Used when Type = "header_injection".
	HeaderInjection *HeaderInjectionConfig `json:"header_injection,omitempty" yaml:"header_injection,omitempty"`

	// TokenExchange contains configuration for token exchange auth strategy.
	// Used when Type = "token_exchange".
	TokenExchange *TokenExchangeConfig `json:"token_exchange,omitempty" yaml:"token_exchange,omitempty"`

	// ExternalAuthConfigRefName stores the MCPExternalAuthConfig name when Type is "external_auth_config_ref".
	// This is resolved at runtime by the auth resolver to a concrete strategy.
	ExternalAuthConfigRefName string `json:"external_auth_config_ref_name,omitempty" yaml:"external_auth_config_ref_name,omitempty"`
}

// HeaderInjectionConfig configures the header injection auth strategy.
// This strategy injects a static or environment-sourced header value into requests.
type HeaderInjectionConfig struct {
	// HeaderName is the name of the header to inject (e.g., "Authorization").
	HeaderName string `json:"header_name" yaml:"header_name"`

	// HeaderValue is the static header value to inject.
	// Either HeaderValue or HeaderValueEnv should be set, not both.
	HeaderValue string `json:"header_value,omitempty" yaml:"header_value,omitempty"`

	// HeaderValueEnv is the environment variable name containing the header value.
	// The value will be resolved at runtime from this environment variable.
	// Either HeaderValue or HeaderValueEnv should be set, not both.
	HeaderValueEnv string `json:"header_value_env,omitempty" yaml:"header_value_env,omitempty"`
}

// TokenExchangeConfig configures the OAuth 2.0 token exchange auth strategy.
// This strategy exchanges incoming tokens for backend-specific tokens using RFC 8693.
type TokenExchangeConfig struct {
	// TokenURL is the OAuth token endpoint URL for token exchange.
	TokenURL string `json:"token_url" yaml:"token_url"`

	// ClientID is the OAuth client ID for the token exchange request.
	ClientID string `json:"client_id,omitempty" yaml:"client_id,omitempty"`

	// ClientSecret is the OAuth client secret (use ClientSecretEnv for security).
	ClientSecret string `json:"client_secret,omitempty" yaml:"client_secret,omitempty"`

	// ClientSecretEnv is the environment variable name containing the client secret.
	// The value will be resolved at runtime from this environment variable.
	ClientSecretEnv string `json:"client_secret_env,omitempty" yaml:"client_secret_env,omitempty"`

	// Audience is the target audience for the exchanged token.
	Audience string `json:"audience,omitempty" yaml:"audience,omitempty"`

	// Scopes are the requested scopes for the exchanged token.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// SubjectTokenType is the token type of the incoming subject token.
	// Defaults to "urn:ietf:params:oauth:token-type:access_token" if not specified.
	SubjectTokenType string `json:"subject_token_type,omitempty" yaml:"subject_token_type,omitempty"`
}
