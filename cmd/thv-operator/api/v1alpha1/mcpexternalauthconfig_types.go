// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// External auth configuration types
const (
	// ExternalAuthTypeTokenExchange is the type for RFC-8693 token exchange
	ExternalAuthTypeTokenExchange ExternalAuthType = "tokenExchange"

	// ExternalAuthTypeHeaderInjection is the type for custom header injection
	ExternalAuthTypeHeaderInjection ExternalAuthType = "headerInjection"

	// ExternalAuthTypeBearerToken is the type for bearer token authentication
	// This allows authenticating to remote MCP servers using bearer tokens stored in Kubernetes Secrets
	ExternalAuthTypeBearerToken ExternalAuthType = "bearerToken"

	// ExternalAuthTypeUnauthenticated is the type for no authentication
	// This should only be used for backends on trusted networks (e.g., localhost, VPC)
	// or when authentication is handled by network-level security
	ExternalAuthTypeUnauthenticated ExternalAuthType = "unauthenticated"

	// ExternalAuthTypeEmbeddedAuthServer is the type for embedded OAuth2/OIDC authorization server
	// This enables running an embedded auth server that delegates to upstream IDPs
	ExternalAuthTypeEmbeddedAuthServer ExternalAuthType = "embeddedAuthServer"

	// ExternalAuthTypeAWSSts is the type for AWS STS authentication
	ExternalAuthTypeAWSSts ExternalAuthType = "awsSts"
)

// ExternalAuthType represents the type of external authentication
type ExternalAuthType string

// MCPExternalAuthConfigSpec defines the desired state of MCPExternalAuthConfig.
// MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources in the same namespace.
//
// +kubebuilder:validation:XValidation:rule="self.type == 'tokenExchange' ? has(self.tokenExchange) : !has(self.tokenExchange)",message="tokenExchange configuration must be set if and only if type is 'tokenExchange'"
// +kubebuilder:validation:XValidation:rule="self.type == 'headerInjection' ? has(self.headerInjection) : !has(self.headerInjection)",message="headerInjection configuration must be set if and only if type is 'headerInjection'"
// +kubebuilder:validation:XValidation:rule="self.type == 'bearerToken' ? has(self.bearerToken) : !has(self.bearerToken)",message="bearerToken configuration must be set if and only if type is 'bearerToken'"
// +kubebuilder:validation:XValidation:rule="self.type == 'embeddedAuthServer' ? has(self.embeddedAuthServer) : !has(self.embeddedAuthServer)",message="embeddedAuthServer configuration must be set if and only if type is 'embeddedAuthServer'"
// +kubebuilder:validation:XValidation:rule="self.type == 'awsSts' ? has(self.awsSts) : !has(self.awsSts)",message="awsSts configuration must be set if and only if type is 'awsSts'"
// +kubebuilder:validation:XValidation:rule="self.type == 'unauthenticated' ? (!has(self.tokenExchange) && !has(self.headerInjection) && !has(self.bearerToken) && !has(self.embeddedAuthServer) && !has(self.awsSts)) : true",message="no configuration must be set when type is 'unauthenticated'"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPExternalAuthConfigSpec struct {
	// Type is the type of external authentication to configure
	// +kubebuilder:validation:Enum=tokenExchange;headerInjection;bearerToken;unauthenticated;embeddedAuthServer;awsSts
	// +kubebuilder:validation:Required
	Type ExternalAuthType `json:"type"`

	// TokenExchange configures RFC-8693 OAuth 2.0 Token Exchange
	// Only used when Type is "tokenExchange"
	// +optional
	TokenExchange *TokenExchangeConfig `json:"tokenExchange,omitempty"`

	// HeaderInjection configures custom HTTP header injection
	// Only used when Type is "headerInjection"
	// +optional
	HeaderInjection *HeaderInjectionConfig `json:"headerInjection,omitempty"`

	// BearerToken configures bearer token authentication
	// Only used when Type is "bearerToken"
	// +optional
	BearerToken *BearerTokenConfig `json:"bearerToken,omitempty"`

	// EmbeddedAuthServer configures an embedded OAuth2/OIDC authorization server
	// Only used when Type is "embeddedAuthServer"
	// +optional
	EmbeddedAuthServer *EmbeddedAuthServerConfig `json:"embeddedAuthServer,omitempty"`

	// AWSSts configures AWS STS authentication with SigV4 request signing
	// Only used when Type is "awsSts"
	// +optional
	AWSSts *AWSStsConfig `json:"awsSts,omitempty"`
}

// TokenExchangeConfig holds configuration for RFC-8693 OAuth 2.0 Token Exchange.
// This configuration is used to exchange incoming authentication tokens for tokens
// that can be used with external services.
// The structure matches the tokenexchange.Config from pkg/auth/tokenexchange/middleware.go
type TokenExchangeConfig struct {
	// TokenURL is the OAuth 2.0 token endpoint URL for token exchange
	// +kubebuilder:validation:Required
	TokenURL string `json:"tokenUrl"`

	// ClientID is the OAuth 2.0 client identifier
	// Optional for some token exchange flows (e.g., Google Cloud Workforce Identity)
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ClientSecretRef is a reference to a secret containing the OAuth 2.0 client secret
	// Optional for some token exchange flows (e.g., Google Cloud Workforce Identity)
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// Audience is the target audience for the exchanged token
	// +kubebuilder:validation:Required
	Audience string `json:"audience"`

	// Scopes is a list of OAuth 2.0 scopes to request for the exchanged token
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// SubjectTokenType is the type of the incoming subject token.
	// Accepts short forms: "access_token" (default), "id_token", "jwt"
	// Or full URNs: "urn:ietf:params:oauth:token-type:access_token",
	//               "urn:ietf:params:oauth:token-type:id_token",
	//               "urn:ietf:params:oauth:token-type:jwt"
	// For Google Workload Identity Federation with OIDC providers (like Okta), use "id_token"
	// +kubebuilder:validation:Pattern=`^(access_token|id_token|jwt|urn:ietf:params:oauth:token-type:(access_token|id_token|jwt))?$`
	// +optional
	SubjectTokenType string `json:"subjectTokenType,omitempty"`

	// ExternalTokenHeaderName is the name of the custom header to use for the exchanged token.
	// If set, the exchanged token will be added to this custom header (e.g., "X-Upstream-Token").
	// If empty or not set, the exchanged token will replace the Authorization header (default behavior).
	// +optional
	ExternalTokenHeaderName string `json:"externalTokenHeaderName,omitempty"`
}

// HeaderInjectionConfig holds configuration for custom HTTP header injection authentication.
// This allows injecting a secret-based header value into requests to backend MCP servers.
// For security reasons, only secret references are supported (no plaintext values).
type HeaderInjectionConfig struct {
	// HeaderName is the name of the HTTP header to inject
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	HeaderName string `json:"headerName"`

	// ValueSecretRef references a Kubernetes Secret containing the header value
	// +kubebuilder:validation:Required
	ValueSecretRef *SecretKeyRef `json:"valueSecretRef"`
}

// BearerTokenConfig holds configuration for bearer token authentication.
// This allows authenticating to remote MCP servers using bearer tokens stored in Kubernetes Secrets.
// For security reasons, only secret references are supported (no plaintext values).
type BearerTokenConfig struct {
	// TokenSecretRef references a Kubernetes Secret containing the bearer token
	// +kubebuilder:validation:Required
	TokenSecretRef *SecretKeyRef `json:"tokenSecretRef"`
}

// EmbeddedAuthServerConfig holds configuration for the embedded OAuth2/OIDC authorization server.
// This enables running an authorization server that delegates authentication to upstream IDPs.
type EmbeddedAuthServerConfig struct {
	// Issuer is the issuer identifier for this authorization server.
	// This will be included in the "iss" claim of issued tokens.
	// Must be a valid HTTPS URL (or HTTP for localhost) without query, fragment, or trailing slash (per RFC 8414).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://[^\s?#]+[^/\s?#]$`
	Issuer string `json:"issuer"`

	// SigningKeySecretRefs references Kubernetes Secrets containing signing keys for JWT operations.
	// Supports key rotation by allowing multiple keys (oldest keys are used for verification only).
	// If not specified, an ephemeral signing key will be auto-generated (development only -
	// JWTs will be invalid after restart).
	// +kubebuilder:validation:MaxItems=5
	// +optional
	SigningKeySecretRefs []SecretKeyRef `json:"signingKeySecretRefs,omitempty"`

	// HMACSecretRefs references Kubernetes Secrets containing symmetric secrets for signing
	// authorization codes and refresh tokens (opaque tokens).
	// Current secret must be at least 32 bytes and cryptographically random.
	// Supports secret rotation via multiple entries (first is current, rest are for verification).
	// If not specified, an ephemeral secret will be auto-generated (development only -
	// auth codes and refresh tokens will be invalid after restart).
	// +optional
	HMACSecretRefs []SecretKeyRef `json:"hmacSecretRefs,omitempty"`

	// TokenLifespans configures the duration that various tokens are valid.
	// If not specified, defaults are applied (access: 1h, refresh: 7d, authCode: 10m).
	// +optional
	TokenLifespans *TokenLifespanConfig `json:"tokenLifespans,omitempty"`

	// UpstreamProviders configures connections to upstream Identity Providers.
	// The embedded auth server delegates authentication to these providers.
	// Currently only a single upstream provider is supported (validated at runtime).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	UpstreamProviders []UpstreamProviderConfig `json:"upstreamProviders"`

	// Storage configures the storage backend for the embedded auth server.
	// If not specified, defaults to in-memory storage.
	// +optional
	Storage *AuthServerStorageConfig `json:"storage,omitempty"`

	// AllowedAudiences is the list of valid resource URIs that tokens can be issued for.
	// For an embedded auth server, this can be determined by the servers (MCP or vMCP) it serves.

	// ScopesSupported is the list of OAuth 2.0 scopes that this authorization server supports.
	// For an embedded auth server, this can be derived from the server's (MCP or vMCP) OIDC configuration.
}

// TokenLifespanConfig holds configuration for token lifetimes.
type TokenLifespanConfig struct {
	// AccessTokenLifespan is the duration that access tokens are valid.
	// Format: Go duration string (e.g., "1h", "30m", "24h").
	// If empty, defaults to 1 hour.
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +optional
	AccessTokenLifespan string `json:"accessTokenLifespan,omitempty"`

	// RefreshTokenLifespan is the duration that refresh tokens are valid.
	// Format: Go duration string (e.g., "168h", "7d" as "168h").
	// If empty, defaults to 7 days (168h).
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +optional
	RefreshTokenLifespan string `json:"refreshTokenLifespan,omitempty"`

	// AuthCodeLifespan is the duration that authorization codes are valid.
	// Format: Go duration string (e.g., "10m", "5m").
	// If empty, defaults to 10 minutes.
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +optional
	AuthCodeLifespan string `json:"authCodeLifespan,omitempty"`
}

// UpstreamProviderType identifies the type of upstream Identity Provider.
type UpstreamProviderType string

const (
	// UpstreamProviderTypeOIDC is for OIDC providers with discovery support
	UpstreamProviderTypeOIDC UpstreamProviderType = "oidc"

	// UpstreamProviderTypeOAuth2 is for pure OAuth 2.0 providers with explicit endpoints
	UpstreamProviderTypeOAuth2 UpstreamProviderType = "oauth2"
)

// UpstreamProviderConfig defines configuration for an upstream Identity Provider.
type UpstreamProviderConfig struct {
	// Name uniquely identifies this upstream provider.
	// Used for routing decisions and session binding in multi-upstream scenarios.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type specifies the provider type: "oidc" or "oauth2"
	// +kubebuilder:validation:Enum=oidc;oauth2
	// +kubebuilder:validation:Required
	Type UpstreamProviderType `json:"type"`

	// OIDCConfig contains OIDC-specific configuration.
	// Required when Type is "oidc", must be nil when Type is "oauth2".
	// +optional
	OIDCConfig *OIDCUpstreamConfig `json:"oidcConfig,omitempty"`

	// OAuth2Config contains OAuth 2.0-specific configuration.
	// Required when Type is "oauth2", must be nil when Type is "oidc".
	// +optional
	OAuth2Config *OAuth2UpstreamConfig `json:"oauth2Config,omitempty"`
}

// OIDCUpstreamConfig contains configuration for OIDC providers.
// OIDC providers support automatic endpoint discovery via the issuer URL.
type OIDCUpstreamConfig struct {
	// IssuerURL is the OIDC issuer URL for automatic endpoint discovery.
	// Must be a valid HTTPS URL.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https://.*$`
	IssuerURL string `json:"issuerUrl"`

	// ClientID is the OAuth 2.0 client identifier registered with the upstream IDP.
	// +kubebuilder:validation:Required
	ClientID string `json:"clientId"`

	// ClientSecretRef references a Kubernetes Secret containing the OAuth 2.0 client secret.
	// Optional for public clients using PKCE instead of client secret.
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// RedirectURI is the callback URL where the upstream IDP will redirect after authentication.
	// When not specified, defaults to `{resourceUrl}/oauth/callback` where `resourceUrl` is the
	// URL associated with the resource (e.g., MCPServer or vMCP) using this config.
	// +optional
	RedirectURI string `json:"redirectUri,omitempty"`

	// Scopes are the OAuth scopes to request from the upstream IDP.
	// If not specified, defaults to ["openid", "offline_access"].
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// UserInfoOverride allows customizing UserInfo fetching behavior for OIDC providers.
	// By default, the UserInfo endpoint is discovered automatically via OIDC discovery.
	// Use this to override the endpoint URL, HTTP method, or field mappings for providers
	// that return non-standard claim names in their UserInfo response.
	// +optional
	UserInfoOverride *UserInfoConfig `json:"userInfoOverride,omitempty"`
}

// OAuth2UpstreamConfig contains configuration for pure OAuth 2.0 providers.
// OAuth 2.0 providers require explicit endpoint configuration.
type OAuth2UpstreamConfig struct {
	// AuthorizationEndpoint is the URL for the OAuth authorization endpoint.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://.*$`
	AuthorizationEndpoint string `json:"authorizationEndpoint"`

	// TokenEndpoint is the URL for the OAuth token endpoint.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://.*$`
	TokenEndpoint string `json:"tokenEndpoint"`

	// UserInfo contains configuration for fetching user information from the upstream provider.
	// Required for OAuth2 providers to resolve user identity.
	// +kubebuilder:validation:Required
	UserInfo *UserInfoConfig `json:"userInfo"`

	// ClientID is the OAuth 2.0 client identifier registered with the upstream IDP.
	// +kubebuilder:validation:Required
	ClientID string `json:"clientId"`

	// ClientSecretRef references a Kubernetes Secret containing the OAuth 2.0 client secret.
	// Optional for public clients using PKCE instead of client secret.
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// RedirectURI is the callback URL where the upstream IDP will redirect after authentication.
	// When not specified, defaults to `{resourceUrl}/oauth/callback` where `resourceUrl` is the
	// URL associated with the resource (e.g., MCPServer or vMCP) using this config.
	// +optional
	RedirectURI string `json:"redirectUri,omitempty"`

	// Scopes are the OAuth scopes to request from the upstream IDP.
	// +optional
	Scopes []string `json:"scopes,omitempty"`
}

// UserInfoConfig contains configuration for fetching user information from an upstream provider.
// This supports both standard OIDC UserInfo endpoints and custom provider-specific endpoints
// like GitHub's /user API.
type UserInfoConfig struct {
	// EndpointURL is the URL of the userinfo endpoint.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://.*$`
	EndpointURL string `json:"endpointUrl"`

	// HTTPMethod is the HTTP method to use for the userinfo request.
	// If not specified, defaults to GET.
	// +kubebuilder:validation:Enum=GET;POST
	// +optional
	HTTPMethod string `json:"httpMethod,omitempty"`

	// AdditionalHeaders contains extra headers to include in the userinfo request.
	// Useful for providers that require specific headers (e.g., GitHub's Accept header).
	// +optional
	AdditionalHeaders map[string]string `json:"additionalHeaders,omitempty"`

	// FieldMapping contains custom field mapping configuration for non-standard providers.
	// If nil, standard OIDC field names are used ("sub", "name", "email").
	// +optional
	FieldMapping *UserInfoFieldMapping `json:"fieldMapping,omitempty"`
}

// UserInfoFieldMapping maps provider-specific field names to standard UserInfo fields.
// This allows adapting non-standard provider responses to the canonical UserInfo structure.
// Each field supports an ordered list of claim names to try. The first non-empty value
// found will be used.
//
// Example for GitHub:
//
//	fieldMapping:
//	  subjectFields: ["id", "login"]
//	  nameFields: ["name", "login"]
//	  emailFields: ["email"]
type UserInfoFieldMapping struct {
	// SubjectFields is an ordered list of field names to try for the user ID.
	// The first non-empty value found will be used.
	// Default: ["sub"]
	// +optional
	SubjectFields []string `json:"subjectFields,omitempty"`

	// NameFields is an ordered list of field names to try for the display name.
	// The first non-empty value found will be used.
	// Default: ["name"]
	// +optional
	NameFields []string `json:"nameFields,omitempty"`

	// EmailFields is an ordered list of field names to try for the email address.
	// The first non-empty value found will be used.
	// Default: ["email"]
	// +optional
	EmailFields []string `json:"emailFields,omitempty"`
}

// Auth server storage types
const (
	// AuthServerStorageTypeMemory is the in-memory storage backend (default)
	AuthServerStorageTypeMemory AuthServerStorageType = "memory"

	// AuthServerStorageTypeRedis is the Redis storage backend
	AuthServerStorageTypeRedis AuthServerStorageType = "redis"
)

// AuthServerStorageType represents the type of storage backend for the embedded auth server
type AuthServerStorageType string

// AuthServerStorageConfig configures the storage backend for the embedded auth server.
type AuthServerStorageConfig struct {
	// Type specifies the storage backend type.
	// Valid values: "memory" (default), "redis".
	// +kubebuilder:validation:Enum=memory;redis
	// +kubebuilder:default=memory
	Type AuthServerStorageType `json:"type,omitempty"`

	// Redis configures the Redis storage backend.
	// Required when type is "redis".
	// +optional
	Redis *RedisStorageConfig `json:"redis,omitempty"`
}

// RedisStorageConfig configures Redis connection for auth server storage.
// Redis is deployed in Sentinel mode with ACL user authentication (the only supported configuration).
type RedisStorageConfig struct {
	// SentinelConfig holds Redis Sentinel configuration.
	// +kubebuilder:validation:Required
	SentinelConfig *RedisSentinelConfig `json:"sentinelConfig"`

	// ACLUserConfig configures Redis ACL user authentication.
	// +kubebuilder:validation:Required
	ACLUserConfig *RedisACLUserConfig `json:"aclUserConfig"`

	// DialTimeout is the timeout for establishing connections.
	// Format: Go duration string (e.g., "5s", "1m").
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +kubebuilder:default="5s"
	// +optional
	DialTimeout string `json:"dialTimeout,omitempty"`

	// ReadTimeout is the timeout for socket reads.
	// Format: Go duration string (e.g., "3s", "1m").
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +kubebuilder:default="3s"
	// +optional
	ReadTimeout string `json:"readTimeout,omitempty"`

	// WriteTimeout is the timeout for socket writes.
	// Format: Go duration string (e.g., "3s", "1m").
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +kubebuilder:default="3s"
	// +optional
	WriteTimeout string `json:"writeTimeout,omitempty"`
}

// RedisSentinelConfig configures Redis Sentinel connection.
type RedisSentinelConfig struct {
	// MasterName is the name of the Redis master monitored by Sentinel.
	// +kubebuilder:validation:Required
	MasterName string `json:"masterName"`

	// SentinelAddrs is a list of Sentinel host:port addresses.
	// Mutually exclusive with SentinelService.
	// +optional
	SentinelAddrs []string `json:"sentinelAddrs,omitempty"`

	// SentinelService enables automatic discovery from a Kubernetes Service.
	// Mutually exclusive with SentinelAddrs.
	// +optional
	SentinelService *SentinelServiceRef `json:"sentinelService,omitempty"`

	// DB is the Redis database number.
	// +kubebuilder:default=0
	// +optional
	DB int32 `json:"db,omitempty"`
}

// SentinelServiceRef references a Kubernetes Service for Sentinel discovery.
type SentinelServiceRef struct {
	// Name of the Sentinel Service.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace of the Sentinel Service (defaults to same namespace).
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Port of the Sentinel service.
	// +kubebuilder:default=26379
	// +optional
	Port int32 `json:"port,omitempty"`
}

// RedisACLUserConfig configures Redis ACL user authentication.
type RedisACLUserConfig struct {
	// UsernameSecretRef references a Secret containing the Redis ACL username.
	// +kubebuilder:validation:Required
	UsernameSecretRef *SecretKeyRef `json:"usernameSecretRef"`

	// PasswordSecretRef references a Secret containing the Redis ACL password.
	// +kubebuilder:validation:Required
	PasswordSecretRef *SecretKeyRef `json:"passwordSecretRef"`
}

// SecretKeyRef is a reference to a key within a Secret
type SecretKeyRef struct {
	// Name is the name of the secret
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key is the key within the secret
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// AWSStsConfig holds configuration for AWS STS authentication with SigV4 request signing.
// This configuration exchanges incoming authentication tokens (typically OIDC JWT) for AWS STS
// temporary credentials, then signs requests to AWS services using SigV4.
type AWSStsConfig struct {
	// Region is the AWS region for the STS endpoint and service (e.g., "us-east-1", "eu-west-1")
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z]{2}(-[a-z]+)+-\d+$`
	Region string `json:"region"`

	// Service is the AWS service name for SigV4 signing
	// Defaults to "aws-mcp" for AWS MCP Server endpoints
	// +kubebuilder:default="aws-mcp"
	// +optional
	Service string `json:"service,omitempty"`

	// FallbackRoleArn is the IAM role ARN to assume when no role mappings match
	// Used as the default role when RoleMappings is empty or no mapping matches
	// At least one of FallbackRoleArn or RoleMappings must be configured (enforced by webhook)
	// +kubebuilder:validation:Pattern=`^arn:(aws|aws-cn|aws-us-gov):iam::\d{12}:role/[\w+=,.@\-_/]+$`
	// +optional
	FallbackRoleArn string `json:"fallbackRoleArn,omitempty"`

	// RoleMappings defines claim-based role selection rules
	// Allows mapping JWT claims (e.g., groups, roles) to specific IAM roles
	// Lower priority values are evaluated first (higher priority)
	// +optional
	RoleMappings []RoleMapping `json:"roleMappings,omitempty"`

	// RoleClaim is the JWT claim to use for role mapping evaluation
	// Defaults to "groups" to match common OIDC group claims
	// +kubebuilder:default="groups"
	// +optional
	RoleClaim string `json:"roleClaim,omitempty"`

	// SessionDuration is the duration in seconds for the STS session
	// Must be between 900 (15 minutes) and 43200 (12 hours)
	// Defaults to 3600 (1 hour) if not specified
	// +kubebuilder:validation:Minimum=900
	// +kubebuilder:validation:Maximum=43200
	// +kubebuilder:default=3600
	// +optional
	SessionDuration *int32 `json:"sessionDuration,omitempty"`

	// SessionNameClaim is the JWT claim to use for role session name
	// Defaults to "sub" to use the subject claim
	// +kubebuilder:default="sub"
	// +optional
	SessionNameClaim string `json:"sessionNameClaim,omitempty"`
}

// RoleMapping defines a rule for mapping JWT claims to IAM roles.
// Mappings are evaluated in priority order (lower number = higher priority), and the first
// matching rule determines which IAM role to assume.
// Exactly one of Claim or Matcher must be specified.
type RoleMapping struct {
	// Claim is a simple claim value to match against
	// The claim type is specified by AWSStsConfig.RoleClaim
	// For example, if RoleClaim is "groups", this would be a group name
	// Internally compiled to a CEL expression: "<claim_value>" in claims["<role_claim>"]
	// Mutually exclusive with Matcher
	// +kubebuilder:validation:MinLength=1
	// +optional
	Claim string `json:"claim,omitempty"`

	// Matcher is a CEL expression for complex matching against JWT claims
	// The expression has access to a "claims" variable containing all JWT claims as map[string]any
	// Examples:
	//   - "admins" in claims["groups"]
	//   - claims["sub"] == "user123" && !("act" in claims)
	// Mutually exclusive with Claim
	// +kubebuilder:validation:MinLength=1
	// +optional
	Matcher string `json:"matcher,omitempty"`

	// RoleArn is the IAM role ARN to assume when this mapping matches
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^arn:(aws|aws-cn|aws-us-gov):iam::\d{12}:role/[\w+=,.@\-_/]+$`
	RoleArn string `json:"roleArn"`

	// Priority determines evaluation order (lower values = higher priority)
	// Allows fine-grained control over role selection precedence
	// When omitted, this mapping has the lowest possible priority and
	// configuration order acts as tie-breaker via stable sort
	// +kubebuilder:validation:Minimum=0
	// +optional
	Priority *int32 `json:"priority,omitempty"`
}

// MCPExternalAuthConfigStatus defines the observed state of MCPExternalAuthConfig
type MCPExternalAuthConfigStatus struct {
	// Conditions represent the latest available observations of the MCPExternalAuthConfig's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed for this MCPExternalAuthConfig.
	// It corresponds to the MCPExternalAuthConfig's generation, which is updated on mutation by the API Server.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the current configuration for change detection
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ReferencingServers is a list of MCPServer resources that reference this MCPExternalAuthConfig
	// This helps track which servers need to be reconciled when this config changes
	// +optional
	ReferencingServers []string `json:"referencingServers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=extauth;mcpextauth
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPExternalAuthConfig is the Schema for the mcpexternalauthconfigs API.
// MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources within the same namespace. Cross-namespace references
// are not supported for security and isolation reasons.
type MCPExternalAuthConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPExternalAuthConfigSpec   `json:"spec,omitempty"`
	Status MCPExternalAuthConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPExternalAuthConfigList contains a list of MCPExternalAuthConfig
type MCPExternalAuthConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPExternalAuthConfig `json:"items"`
}

// Validate performs validation on the MCPExternalAuthConfig spec.
// This method is called by the controller during reconciliation.
//
// Note: These validations provide defense-in-depth alongside CEL validation rules (lines 44-49).
// CEL catches issues at API admission time, but this method also validates stored objects
// to catch any that bypassed CEL or were stored before CEL rules were added.
func (r *MCPExternalAuthConfig) Validate() error {
	// First, validate type/config consistency (defense-in-depth with CEL)
	if err := r.validateTypeConfigConsistency(); err != nil {
		return err
	}

	// Then perform type-specific complex validation
	switch r.Spec.Type {
	case ExternalAuthTypeEmbeddedAuthServer:
		return r.validateEmbeddedAuthServer()
	case ExternalAuthTypeAWSSts:
		return r.validateAWSSts()
	case ExternalAuthTypeTokenExchange,
		ExternalAuthTypeHeaderInjection,
		ExternalAuthTypeBearerToken,
		ExternalAuthTypeUnauthenticated:
		// No complex validation needed for these types
		return nil
	default:
		// Unknown type - should be caught by enum validation, but handle defensively
		return fmt.Errorf("unsupported auth type: %s", r.Spec.Type)
	}
}

// validateTypeConfigConsistency validates that the correct config is set for the selected type.
// This mirrors the CEL validation rules but provides defense-in-depth for stored objects.
func (r *MCPExternalAuthConfig) validateTypeConfigConsistency() error {
	// Check that each type has its corresponding config
	if (r.Spec.TokenExchange == nil) == (r.Spec.Type == ExternalAuthTypeTokenExchange) {
		return fmt.Errorf("tokenExchange configuration must be set if and only if type is 'tokenExchange'")
	}
	if (r.Spec.HeaderInjection == nil) == (r.Spec.Type == ExternalAuthTypeHeaderInjection) {
		return fmt.Errorf("headerInjection configuration must be set if and only if type is 'headerInjection'")
	}
	if (r.Spec.BearerToken == nil) == (r.Spec.Type == ExternalAuthTypeBearerToken) {
		return fmt.Errorf("bearerToken configuration must be set if and only if type is 'bearerToken'")
	}
	if (r.Spec.EmbeddedAuthServer == nil) == (r.Spec.Type == ExternalAuthTypeEmbeddedAuthServer) {
		return fmt.Errorf("embeddedAuthServer configuration must be set if and only if type is 'embeddedAuthServer'")
	}
	if (r.Spec.AWSSts == nil) == (r.Spec.Type == ExternalAuthTypeAWSSts) {
		return fmt.Errorf("awsSts configuration must be set if and only if type is 'awsSts'")
	}

	// Check that unauthenticated has no config
	if r.Spec.Type == ExternalAuthTypeUnauthenticated {
		if r.Spec.TokenExchange != nil ||
			r.Spec.HeaderInjection != nil ||
			r.Spec.BearerToken != nil ||
			r.Spec.EmbeddedAuthServer != nil ||
			r.Spec.AWSSts != nil {
			return fmt.Errorf("no configuration must be set when type is 'unauthenticated'")
		}
	}

	return nil
}

// validateEmbeddedAuthServer validates embeddedAuthServer type configuration.
// This performs complex business logic validation that CEL cannot express.
func (r *MCPExternalAuthConfig) validateEmbeddedAuthServer() error {
	// Validate upstream providers
	cfg := r.Spec.EmbeddedAuthServer
	if cfg == nil {
		return nil
	}

	// Note: MinItems=1 is enforced by kubebuilder markers,
	// but we add runtime validation for clarity and future-proofing
	if len(cfg.UpstreamProviders) == 0 {
		return fmt.Errorf("at least one upstream provider is required")
	}
	// Note: we add runtime validation for 'max items = 1' here since multi-provider support is not yet implemented.
	if len(cfg.UpstreamProviders) > 1 {
		return fmt.Errorf("currently only one upstream provider is supported (found %d)", len(cfg.UpstreamProviders))
	}

	for i, provider := range cfg.UpstreamProviders {
		if err := r.validateUpstreamProvider(i, &provider); err != nil {
			return err
		}
	}

	return nil
}

// validateUpstreamProvider validates a single upstream provider configuration
func (*MCPExternalAuthConfig) validateUpstreamProvider(index int, provider *UpstreamProviderConfig) error {
	prefix := fmt.Sprintf("upstreamProviders[%d]", index)

	if (provider.OIDCConfig == nil) == (provider.Type == UpstreamProviderTypeOIDC) {
		return fmt.Errorf("%s: oidcConfig must be set when type is 'oidc' and must not be set otherwise", prefix)
	}
	if (provider.OAuth2Config == nil) == (provider.Type == UpstreamProviderTypeOAuth2) {
		return fmt.Errorf("%s: oauth2Config must be set when type is 'oauth2' and must not be set otherwise", prefix)
	}
	if provider.Type != UpstreamProviderTypeOIDC && provider.Type != UpstreamProviderTypeOAuth2 {
		return fmt.Errorf("%s: unsupported provider type: %s", prefix, provider.Type)
	}

	return nil
}

// validateAWSSts validates awsSts type configuration.
// This performs complex business logic validation that CEL cannot express.
func (r *MCPExternalAuthConfig) validateAWSSts() error {
	cfg := r.Spec.AWSSts
	if cfg == nil {
		return nil
	}

	// Region is required
	if cfg.Region == "" {
		return fmt.Errorf("awsSts.region is required")
	}

	// At least one of fallbackRoleArn or roleMappings must be configured
	// Both can be set: fallbackRoleArn is used when no mapping matches
	hasRoleArn := cfg.FallbackRoleArn != ""
	hasRoleMappings := len(cfg.RoleMappings) > 0

	if !hasRoleArn && !hasRoleMappings {
		return fmt.Errorf("awsSts: at least one of fallbackRoleArn or roleMappings must be configured")
	}

	// Validate role mappings if present
	for i, mapping := range cfg.RoleMappings {
		if mapping.RoleArn == "" {
			return fmt.Errorf("awsSts.roleMappings[%d].roleArn is required", i)
		}
		// Exactly one of claim or matcher must be set
		if mapping.Claim == "" && mapping.Matcher == "" {
			return fmt.Errorf("awsSts.roleMappings[%d]: exactly one of claim or matcher must be set", i)
		}
		if mapping.Claim != "" && mapping.Matcher != "" {
			return fmt.Errorf("awsSts.roleMappings[%d]: claim and matcher are mutually exclusive", i)
		}
	}

	// Validate session duration if set
	// Bounds match AWS STS limits: 900s (15 min) to 43200s (12 hours)
	if cfg.SessionDuration != nil {
		duration := *cfg.SessionDuration
		const (
			minSessionDuration int32 = 900   // 15 minutes
			maxSessionDuration int32 = 43200 // 12 hours
		)
		if duration < minSessionDuration || duration > maxSessionDuration {
			return fmt.Errorf("awsSts.sessionDuration must be between %d and %d seconds",
				minSessionDuration, maxSessionDuration)
		}
	}

	return nil
}

func init() {
	SchemeBuilder.Register(&MCPExternalAuthConfig{}, &MCPExternalAuthConfigList{})
}
