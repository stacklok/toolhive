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

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUpstreamTokenNotFound is returned when a required upstream provider token
// is not present in the identity's UpstreamTokens map.
var ErrUpstreamTokenNotFound = errors.New("upstream token not found")

// ConsentRequiredMarker is the machine-detectable prefix of the tool-result
// text emitted when a tool call fails because the user has not consented an
// upstream provider. The marker is followed by a single space and a JSON
// payload (consentRequiredPayload). It is a convention, not a wire-protocol
// change: it travels in the existing IsError/text channel that untrusted
// servers and stock MCP clients already carry untouched.
const ConsentRequiredMarker = "UPSTREAM_CONSENT_REQUIRED"

// ConsentRequiredError carries the provider and authorize endpoint for a
// missing upstream consent, so the tool-call layer can render an actionable
// IsError result (see FormatConsentRequired). AuthorizeURL may be empty when
// no authorization server issuer is configured.
type ConsentRequiredError struct {
	Provider     string
	AuthorizeURL string
}

// Error returns a human-readable message. It contains the provider name and
// authorize endpoint only — never token material.
func (e *ConsentRequiredError) Error() string {
	if e.AuthorizeURL != "" {
		return fmt.Sprintf("consent required for upstream provider %q (authorize at %s)",
			e.Provider, e.AuthorizeURL)
	}
	return fmt.Sprintf("consent required for upstream provider %q", e.Provider)
}

// Unwrap returns ErrUpstreamTokenNotFound so existing errors.Is classification
// (health monitors, pre-check middleware) keeps working while errors.As
// extracts the structured payload.
func (*ConsentRequiredError) Unwrap() error {
	return ErrUpstreamTokenNotFound
}

// consentRequiredPayload is the JSON object following ConsentRequiredMarker.
type consentRequiredPayload struct {
	Provider     string `json:"provider"`
	AuthorizeURL string `json:"authorize_url,omitempty"`
}

// FormatConsentRequired renders the consent-required tool-result text:
// the marker, a space, then a JSON payload carrying the provider and (when
// configured) the authorize endpoint the client should open to consent.
// The payload never contains token material.
func FormatConsentRequired(provider, authorizeURL string) string {
	payload, err := json.Marshal(consentRequiredPayload{Provider: provider, AuthorizeURL: authorizeURL})
	if err != nil {
		// Only non-UTF8 control characters could fail marshaling; fall back to
		// the marker plus provider so the message is still detectable.
		return fmt.Sprintf("%s {\"provider\":%q}", ConsentRequiredMarker, provider)
	}
	return fmt.Sprintf("%s %s", ConsentRequiredMarker, payload)
}

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

	// StrategyTypeOBO identifies the on-behalf-of (OBO) authentication strategy.
	// The default upstream implementation returns ErrEnterpriseRequired from
	// every method; an out-of-tree build registers a real OBO strategy executor
	// via auth.RegisterOBOStrategy.
	StrategyTypeOBO = "obo"

	// StrategyTypeXAA identifies the XAA (Cross-Application Access) strategy.
	// This strategy implements cross-application access using the Identity
	// Assertion JWT Authorization Grant (draft-ietf-oauth-identity-assertion-authz-grant,
	// also known as ID-JAG). It performs a two-step token exchange:
	// (A) exchange an ID token for an ID-JAG at the IdP, then
	// (B) exchange the ID-JAG for an access token at the target AS.
	StrategyTypeXAA = "xaa"
)

// BackendAuthStrategy defines how to authenticate to a specific backend.
//
// This struct provides type-safe configuration for different authentication strategies
// using HeaderInjection or TokenExchange fields based on the Type field.
// +kubebuilder:object:generate=true
// +gendoc
type BackendAuthStrategy struct {
	// Type is the auth strategy: "unauthenticated", "header_injection", "token_exchange", "upstream_inject", "aws_sts", "obo", "xaa"
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

	// OBO contains configuration for on-behalf-of (OBO) auth strategy.
	// Used when Type = "obo". The default upstream build returns ErrEnterpriseRequired;
	// an out-of-tree build registers a real strategy via auth.RegisterOBOStrategy.
	OBO *OBOConfig `json:"obo,omitempty" yaml:"obo,omitempty"`

	// XAA contains configuration for XAA (Cross-Application Access) auth strategy.
	// Used when Type = "xaa".
	XAA *XAAConfig `json:"xaa,omitempty" yaml:"xaa,omitempty"`
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
	// +listType=atomic
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

	// AuthorizeURL is the ToolHive authorization server's authorize-endpoint
	// URL ({issuer}/oauth/authorize). When set, it is carried in the
	// ConsentRequiredError returned when the provider token is absent, so
	// clients can direct the user to consent. The URL cannot be a complete
	// one-click link: the client must merge its own client_id, redirect_uri,
	// and PKCE parameters. Optional; when empty the error carries no URL.
	// +optional
	AuthorizeURL string `json:"authorizeUrl,omitempty" yaml:"authorizeUrl,omitempty"`
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
	// Mirrors awssts.RoleMapping.Priority, which is *int because the role mapper
	// uses math.MaxInt for nil-priority semantics in effectivePriority.
	Priority *int `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// OBOConfig configures the on-behalf-of (OBO) authentication strategy.
// This strategy uses the Entra jwt-bearer / on_behalf_of grant to exchange
// the incoming user token for a backend-scoped token on behalf of the user.
//
// Field names follow the OBO runtime contract (the enterprise obo.MiddlewareParameters),
// not the RFC-8693 TokenExchangeConfig, because OBO uses a distinct Entra-specific grant.
// +kubebuilder:object:generate=true
// +gendoc
type OBOConfig struct {
	// TokenURL is the Entra token endpoint URL for the OBO exchange.
	// +kubebuilder:validation:Required
	TokenURL string `json:"tokenUrl" yaml:"tokenUrl"`

	// ClientID is the OAuth client ID for the OBO request.
	ClientID string `json:"clientId,omitempty" yaml:"clientId,omitempty"`

	// ClientSecret is the OAuth client secret (use ClientSecretEnv for security).
	//nolint:gosec // G117: field legitimately holds sensitive data
	ClientSecret string `json:"clientSecret,omitempty" yaml:"clientSecret,omitempty"`

	// ClientSecretEnv is the environment variable name containing the client secret.
	// The value will be resolved at runtime from this environment variable.
	ClientSecretEnv string `json:"clientSecretEnv,omitempty" yaml:"clientSecretEnv,omitempty"`

	// Audience is the target audience (resource URI) for the exchanged token.
	Audience string `json:"audience,omitempty" yaml:"audience,omitempty"`

	// Scopes are the requested scopes for the exchanged token.
	// +listType=atomic
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// SubjectTokenProviderName selects which upstream provider's token to use as the
	// subject (assertion) token for the OBO exchange. When set, the token is looked
	// up from Identity.UpstreamTokens[SubjectTokenProviderName]; when omitted, the
	// inbound end-user token (Identity.Token) is used directly.
	// Matches the operator CRD's SubjectTokenProviderName field; the enterprise OBO
	// converter maps both to the runtime contract without renaming.
	SubjectTokenProviderName string `json:"subjectTokenProviderName,omitempty" yaml:"subjectTokenProviderName,omitempty"`

	// CacheSkewSeconds is the number of seconds to subtract from a cached token's
	// expiry when deciding whether to refresh it. Defaults to zero (no skew).
	// The operator CRD stores this as CacheSkew *metav1.Duration and converts it
	// to an integer-seconds value for the vMCP runtime contract.
	CacheSkewSeconds *int32 `json:"cacheSkewSeconds,omitempty" yaml:"cacheSkewSeconds,omitempty"`
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

	// SubjectProviderName selects which upstream provider's token to use as the
	// web identity token for AssumeRoleWithWebIdentity. When set, the token is
	// looked up from Identity.UpstreamTokens instead of the request's
	// Authorization header.
	SubjectProviderName string `json:"subjectProviderName,omitempty" yaml:"subjectProviderName,omitempty"`
}

// XAAConfig configures the XAA (Cross-Application Access) auth strategy.
// XAA implements draft-ietf-oauth-identity-assertion-authz-grant (ID-JAG) as a
// two-step flow:
//   - IdP exchange (RFC 8693): Exchange the user's ID token at their IdP for an ID-JAG JWT
//   - Target grant (RFC 7523): Exchange the ID-JAG at the target app's AS for an access token
//
// +kubebuilder:object:generate=true
// +gendoc
type XAAConfig struct {
	// IDPTokenURL is the IdP token endpoint for IdP exchange (RFC 8693 exchange).
	IDPTokenURL string `json:"idpTokenUrl" yaml:"idpTokenUrl"`

	// IDPClientID is the OAuth client ID at the IdP for IdP exchange.
	IDPClientID string `json:"idpClientId,omitempty" yaml:"idpClientId,omitempty"`

	// IDPClientSecret is the client secret at the IdP for IdP exchange.
	//nolint:gosec // G101: field legitimately holds sensitive data
	IDPClientSecret string `json:"idpClientSecret,omitempty" yaml:"idpClientSecret,omitempty"`

	// IDPClientSecretEnv is the env var containing the IdP client secret.
	IDPClientSecretEnv string `json:"idpClientSecretEnv,omitempty" yaml:"idpClientSecretEnv,omitempty"`

	// TargetTokenURL is the target AS token endpoint for target grant (JWT Bearer grant).
	TargetTokenURL string `json:"targetTokenUrl" yaml:"targetTokenUrl"`

	// InsecureTargetTokenURL allows plain HTTP for TargetTokenURL.
	// WARNING: this is insecure and must only be set for in-cluster or
	// development/testing endpoints — never in production.
	InsecureTargetTokenURL bool `json:"insecureTargetTokenUrl,omitempty" yaml:"insecureTargetTokenUrl,omitempty"`

	// TargetClientID is the OAuth client ID at the target AS for target grant.
	TargetClientID string `json:"targetClientId,omitempty" yaml:"targetClientId,omitempty"`

	// TargetClientSecret is the client secret at the target AS for target grant.
	//nolint:gosec // G101: field legitimately holds sensitive data
	TargetClientSecret string `json:"targetClientSecret,omitempty" yaml:"targetClientSecret,omitempty"`

	// TargetClientSecretEnv is the env var containing the target AS client secret.
	TargetClientSecretEnv string `json:"targetClientSecretEnv,omitempty" yaml:"targetClientSecretEnv,omitempty"`

	// TargetAudience is the resource AS URL for the ID-JAG audience claim (required).
	TargetAudience string `json:"targetAudience" yaml:"targetAudience"`

	// TargetResource is the RFC 8707 resource indicator sent as the `resource`
	// parameter in IdP exchange's RFC 8693 token exchange (draft §4.3, OPTIONAL). It
	// identifies the target resource server — not the access-token audience, which
	// is governed by TargetAudience. For MCP backends, set to the MCP server URL.
	TargetResource string `json:"targetResource,omitempty" yaml:"targetResource,omitempty"`

	// Scopes are the requested scopes for IdP exchange and target grant.
	// +listType=atomic
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// SubjectProviderName selects which upstream provider's ID token to use.
	// Auto-populated when embedded AS is active.
	SubjectProviderName string `json:"subjectProviderName,omitempty" yaml:"subjectProviderName,omitempty"`

	// SubjectTokenType is the token-type URN of the upstream subject token
	// used in IdP exchange. Defaults to TokenTypeIDToken when empty. Currently only
	// urn:ietf:params:oauth:token-type:id_token is accepted; the field exists
	// to allow future expansion to SAML upstreams without an API break.
	SubjectTokenType string `json:"subjectTokenType,omitempty" yaml:"subjectTokenType,omitempty"`
}
