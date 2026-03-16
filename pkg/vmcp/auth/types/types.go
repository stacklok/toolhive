// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
)

// Token exchange variant identifiers.
const (
	// TokenExchangeVariantEntra selects the Microsoft Entra OBO (On-Behalf-Of) flow.
	TokenExchangeVariantEntra = "entra"

	// TokenExchangeVariantRaw selects a custom grant type with explicit URN and parameters.
	TokenExchangeVariantRaw = "raw"
)

// BackendAuthStrategy defines how to authenticate to a specific backend.
//
// This struct provides type-safe configuration for different authentication strategies
// using HeaderInjection or TokenExchange fields based on the Type field.
// +kubebuilder:object:generate=true
// +gendoc
type BackendAuthStrategy struct {
	// Type is the auth strategy: "unauthenticated", "header_injection", "token_exchange"
	Type string `json:"type" yaml:"type"`

	// HeaderInjection contains configuration for header injection auth strategy.
	// Used when Type = "header_injection".
	HeaderInjection *HeaderInjectionConfig `json:"headerInjection,omitempty" yaml:"headerInjection,omitempty"`

	// TokenExchange contains configuration for token exchange auth strategy.
	// Used when Type = "token_exchange".
	TokenExchange *TokenExchangeConfig `json:"tokenExchange,omitempty" yaml:"tokenExchange,omitempty"`
}

// HeaderInjectionConfig configures the header injection auth strategy.
// This strategy injects a static or environment-sourced header value into requests.
// +kubebuilder:object:generate=true
// +gendoc
type HeaderInjectionConfig struct {
	// HeaderName is the name of the header to inject (e.g., "Authorization").
	HeaderName string `json:"headerName" yaml:"headerName"`

	// HeaderValue is the static header value to inject.
	// Either HeaderValue or HeaderValueEnv should be set, not both.
	HeaderValue string `json:"headerValue,omitempty" yaml:"headerValue,omitempty"`

	// HeaderValueEnv is the environment variable name containing the header value.
	// The value will be resolved at runtime from this environment variable.
	// Either HeaderValue or HeaderValueEnv should be set, not both.
	HeaderValueEnv string `json:"headerValueEnv,omitempty" yaml:"headerValueEnv,omitempty"`
}

// TokenExchangeRawAuthConfig holds extension configuration for non-standard token exchange flows.
// For variant "raw": grantTypeUrn and parameters are used directly.
// For named variants (e.g., "entra"): the handler reads variant-specific keys from parameters.
// +kubebuilder:object:generate=true
// +gendoc
type TokenExchangeRawAuthConfig struct {
	// GrantTypeURN is the OAuth 2.0 grant_type value to send in the token request.
	// Required for variant "raw". Not used by named variants (handler sets it).
	GrantTypeURN string `json:"grantTypeUrn,omitempty" yaml:"grantTypeUrn,omitempty"`

	// Parameters are additional key-value pairs passed to the variant handler.
	Parameters map[string]string `json:"parameters,omitempty" yaml:"parameters,omitempty"`
}

// TokenExchangeConfig configures the OAuth 2.0 token exchange auth strategy.
// When no variant is specified, standard RFC 8693 token exchange is used.
// Named variants (e.g., "entra") provide purpose-built configuration for
// specific identity providers. The "raw" variant allows custom grant types.
// +kubebuilder:object:generate=true
// +gendoc
type TokenExchangeConfig struct {
	// TokenURL is the OAuth token endpoint URL for token exchange.
	TokenURL string `json:"tokenUrl" yaml:"tokenUrl"`

	// ClientID is the OAuth client ID for the token exchange request.
	ClientID string `json:"clientId,omitempty" yaml:"clientId,omitempty"`

	// ClientSecret is the OAuth client secret (use ClientSecretEnv for security).
	//nolint:gosec // G117: field legitimately holds sensitive data
	ClientSecret string `json:"clientSecret,omitempty" yaml:"clientSecret,omitempty"`

	// ClientSecretEnv is the environment variable name containing the client secret.
	// The value will be resolved at runtime from this environment variable.
	ClientSecretEnv string `json:"clientSecretEnv,omitempty" yaml:"clientSecretEnv,omitempty"`

	// Audience is the target audience for the exchanged token.
	Audience string `json:"audience,omitempty" yaml:"audience,omitempty"`

	// Scopes are the requested scopes for the exchanged token.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// SubjectTokenType is the token type of the incoming subject token.
	// Defaults to "urn:ietf:params:oauth:token-type:access_token" if not specified.
	SubjectTokenType string `json:"subjectTokenType,omitempty" yaml:"subjectTokenType,omitempty"`

	// Variant selects a token exchange variant with purpose-built configuration.
	// When omitted, standard RFC 8693 token exchange is used (existing behavior).
	Variant string `json:"variant,omitempty" yaml:"variant,omitempty"`

	// Raw holds extension configuration for non-standard token exchange flows.
	// Required when variant is "raw". Optional for named variants.
	Raw *TokenExchangeRawAuthConfig `json:"raw,omitempty" yaml:"raw,omitempty"`
}
