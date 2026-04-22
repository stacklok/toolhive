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

import "errors"

// ErrUpstreamTokenNotFound is returned when a required upstream provider token
// is not present in the identity's UpstreamTokens map.
var ErrUpstreamTokenNotFound = errors.New("upstream token not found")

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

	// StrategyTypeUpstreamInject identifies the upstream inject strategy.
	// This strategy injects an upstream IDP token obtained by the embedded
	// authorization server into requests to the backend service.
	StrategyTypeUpstreamInject = "upstream_inject"

	// StrategyTypeAwsSts identifies the AWS STS authentication strategy.
	// This strategy exchanges incoming tokens for AWS STS temporary credentials
	// and signs requests using SigV4.
	StrategyTypeAwsSts = "aws_sts"
)

// BackendAuthStrategy defines how to authenticate to a specific backend.
//
// This struct provides type-safe configuration for different authentication strategies
// using HeaderInjection or TokenExchange fields based on the Type field.
// +kubebuilder:object:generate=true
// +gendoc
type BackendAuthStrategy struct {
	// Type is the auth strategy: "unauthenticated", "header_injection", "token_exchange", "upstream_inject", "aws_sts"
	Type string `json:"type" yaml:"type"`

	// HeaderInjection contains configuration for header injection auth strategy.
	// Used when Type = "header_injection".
	HeaderInjection *HeaderInjectionConfig `json:"headerInjection,omitempty" yaml:"headerInjection,omitempty"`

	// TokenExchange contains configuration for token exchange auth strategy.
	// Used when Type = "token_exchange".
	TokenExchange *TokenExchangeConfig `json:"tokenExchange,omitempty" yaml:"tokenExchange,omitempty"`

	// UpstreamInject contains configuration for upstream inject auth strategy.
	// Used when Type = "upstream_inject".
	UpstreamInject *UpstreamInjectConfig `json:"upstreamInject,omitempty" yaml:"upstreamInject,omitempty"`

	// AwsSts contains configuration for AWS STS auth strategy.
	// Used when Type = "aws_sts".
	AwsSts *AwsStsConfig `json:"awsSts,omitempty" yaml:"awsSts,omitempty"`
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

// TokenExchangeConfig configures the OAuth 2.0 token exchange auth strategy.
// This strategy exchanges incoming tokens for backend-specific tokens using RFC 8693.
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

	// SubjectProviderName selects which upstream provider's token to use as the
	// subject token. When set, the token is looked up from Identity.UpstreamTokens
	// instead of using Identity.Token.
	// When left empty and an embedded authorization server is configured, the system
	// automatically populates this field with the first configured upstream provider name.
	// Set it explicitly to override that default or to select a specific provider when
	// multiple upstreams are configured.
	SubjectProviderName string `json:"subjectProviderName,omitempty" yaml:"subjectProviderName,omitempty"`
}

// UpstreamInjectConfig configures the upstream inject auth strategy.
// This strategy uses the embedded authorization server to obtain and inject
// upstream IDP tokens into backend requests.
// +kubebuilder:object:generate=true
// +gendoc
type UpstreamInjectConfig struct {
	// ProviderName is the name of the upstream provider configured in the
	// embedded authorization server. Must match an entry in AuthServer.Upstreams.
	ProviderName string `json:"providerName" yaml:"providerName"`
}

// RoleMapping defines a rule for mapping JWT claims to IAM roles.
// Mappings are evaluated in priority order (lower number = higher priority).
// +kubebuilder:object:generate=true
// +gendoc
type RoleMapping struct {
	// Claim is a simple claim value to match against the RoleClaim field.
	Claim string `json:"claim,omitempty" yaml:"claim,omitempty"`

	// Matcher is a CEL expression for complex matching against JWT claims.
	Matcher string `json:"matcher,omitempty" yaml:"matcher,omitempty"`

	// RoleArn is the IAM role ARN to assume when this mapping matches.
	RoleArn string `json:"roleArn" yaml:"roleArn"`

	// Priority determines evaluation order (lower values = higher priority).
	Priority *int32 `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// AwsStsConfig configures AWS STS authentication with SigV4 request signing.
// This strategy exchanges incoming tokens for AWS STS temporary credentials.
// +kubebuilder:object:generate=true
// +gendoc
type AwsStsConfig struct {
	// Region is the AWS region for the STS endpoint and service.
	Region string `json:"region" yaml:"region"`

	// Service is the AWS service name for SigV4 signing.
	Service string `json:"service,omitempty" yaml:"service,omitempty"`

	// FallbackRoleArn is the IAM role ARN to assume when no role mappings match.
	FallbackRoleArn string `json:"fallbackRoleArn,omitempty" yaml:"fallbackRoleArn,omitempty"`

	// RoleMappings defines claim-based role selection rules.
	// +listType=atomic
	RoleMappings []RoleMapping `json:"roleMappings,omitempty" yaml:"roleMappings,omitempty"`

	// RoleClaim is the JWT claim to use for role mapping evaluation.
	RoleClaim string `json:"roleClaim,omitempty" yaml:"roleClaim,omitempty"`

	// SessionDuration is the duration in seconds for the STS session.
	SessionDuration *int32 `json:"sessionDuration,omitempty" yaml:"sessionDuration,omitempty"`

	// SessionNameClaim is the JWT claim to use for the role session name.
	SessionNameClaim string `json:"sessionNameClaim,omitempty" yaml:"sessionNameClaim,omitempty"`

	// TokenProviderName selects which upstream provider's token to use as the
	// web identity token for AssumeRoleWithWebIdentity. When set, the token is
	// looked up from Identity.UpstreamTokens instead of the request's
	// Authorization header.
	TokenProviderName string `json:"tokenProviderName,omitempty" yaml:"tokenProviderName,omitempty"`
}
