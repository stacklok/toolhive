// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/authserver/oauthparams"
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

	// ExternalAuthTypeUpstreamInject is the type for upstream token injection
	// This injects an upstream IdP access token as the Authorization: Bearer header
	ExternalAuthTypeUpstreamInject ExternalAuthType = "upstreamInject"

	// ExternalAuthTypeOBO is the type for on-behalf-of (OBO) flows.
	// This type requires a build with an OBO handler registered via
	// controllerutil.RegisterOBOHandler; an upstream-only build surfaces
	// status.conditions[Valid] = False with Reason: EnterpriseRequired
	// when an obo-typed MCPExternalAuthConfig is applied.
	ExternalAuthTypeOBO ExternalAuthType = "obo"

	// ExternalAuthTypeXAA is the type for XAA (Cross-Application Access) auth.
	// XAA performs a two-step token exchange to obtain access tokens for target services:
	//   - IdP exchange (RFC 8693): Exchange the user's ID token at their IdP for an ID-JAG JWT
	//   - Target grant (RFC 7523): Exchange the ID-JAG at the target app's AS for an access token
	ExternalAuthTypeXAA ExternalAuthType = "xaa"
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
// +kubebuilder:validation:XValidation:rule="self.type == 'upstreamInject' ? has(self.upstreamInject) : !has(self.upstreamInject)",message="upstreamInject configuration must be set if and only if type is 'upstreamInject'"
// +kubebuilder:validation:XValidation:rule="self.type == 'obo' ? has(self.obo) : !has(self.obo)",message="obo configuration must be set if and only if type is 'obo'"
// +kubebuilder:validation:XValidation:rule="self.type == 'xaa' ? has(self.xaa) : !has(self.xaa)",message="xaa configuration must be set if and only if type is 'xaa'"
// +kubebuilder:validation:XValidation:rule="self.type == 'unauthenticated' ? (!has(self.tokenExchange) && !has(self.headerInjection) && !has(self.bearerToken) && !has(self.embeddedAuthServer) && !has(self.awsSts) && !has(self.upstreamInject) && !has(self.obo) && !has(self.xaa)) : true",message="no configuration must be set when type is 'unauthenticated'"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPExternalAuthConfigSpec struct {
	// Type is the type of external authentication to configure.
	// When set to "obo", the cluster must run a build that has registered an
	// OBO handler via controllerutil.RegisterOBOHandler; upstream-only builds
	// surface status.conditions[Valid] = False with Reason: EnterpriseRequired
	// for obo-typed configs.
	// +kubebuilder:validation:Enum=tokenExchange;headerInjection;bearerToken;unauthenticated;embeddedAuthServer;awsSts;upstreamInject;obo;xaa
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

	// UpstreamInject configures upstream token injection for backend requests.
	// Only used when Type is "upstreamInject".
	// +optional
	UpstreamInject *UpstreamInjectSpec `json:"upstreamInject,omitempty"`

	// OBO configures On-Behalf-Of (OBO) authentication.
	// Only used when Type is "obo". Setting this field on an upstream-only build
	// causes the MCPExternalAuthConfig to transition to
	// status.conditions[Valid] = False with Reason: EnterpriseRequired, because
	// no OBO handler is registered. See OBOConfig for the field-to-runtime
	// contract mapping.
	// +optional
	OBO *OBOConfig `json:"obo,omitempty"`

	// XAA configures XAA (Cross-Application Access) auth for backend requests.
	// Only used when Type is "xaa".
	// +optional
	XAA *XAASpec `json:"xaa,omitempty"`
}

// OBOConfig holds configuration for the On-Behalf-Of (OBO) external auth type.
// Only used when Type is "obo".
//
// This is the user-facing CRD surface for the Microsoft Entra OBO flow. It is
// structurally valid in upstream (OSS) builds but inert: an upstream-only build
// returns obo.ErrEnterpriseRequired at reconcile (Valid=False, Reason:
// EnterpriseRequired) because no OBO handler is registered via
// controllerutil.RegisterOBOHandler. A build with the enterprise OBO handler
// translates these fields into the runtime wire contract obo.MiddlewareParameters,
// so the field names and semantics here track that contract rather than the
// upstream TokenExchangeConfig (which uses different names, e.g.
// subjectProviderName / externalTokenHeaderName). In particular there is no
// externalTokenHeaderName: the OBO subject is sourced from the authenticated
// Identity, never from an inbound request header.
//
// Field-to-contract mapping performed by the operator's OBO handler:
//   - tenantId (+ optional authority) → tokenUrl
//     (https://login.microsoftonline.com/<tenantId>/oauth2/v2.0/token, or the
//     configured authority base joined with the tenant for sovereign clouds)
//   - clientSecretRef → resolved into a pod env var; only the env var name
//     travels in the contract, as clientSecretEnvVar
//   - audience / scopes → collapsed to a single exchange target by
//     obo.MiddlewareParameters.ExchangeTarget() (space-joined scopes win,
//     otherwise audience)
//   - cacheSkew → the contract's integer-seconds cacheSkewSeconds
//
// Every field is optional at the CRD level, and the schema deliberately carries
// no required field and no cross-field CEL rule. spec.obo shipped as an empty
// placeholder ({}) in earlier releases, so adding a required field or an
// admission rule that rejects {} would be a backward-incompatible narrowing of
// an already-stored, round-trippable object. Presence and combination
// requirements — a tenant, a client-auth credential, and at least one of
// audience/scopes — are therefore enforced by the registered OBO handler at
// reconcile, which reports a violation as Valid=False / Reason=InvalidConfig.
// The per-field patterns below still apply, but only to a value that is present.
//
//nolint:lll // the tenantId GUID/domain pattern exceeds the line length limit
type OBOConfig struct {
	// TenantID is the Microsoft Entra (Azure AD) directory (tenant) identifier.
	// Optional at the CRD level (see the type doc); the operator enforces its
	// presence, since an OBO confidential-client exchange must target a specific
	// tenant. When set, it must be one of the two forms the Entra v2.0 token
	// endpoint addresses: a directory GUID, or a verified domain name (e.g.
	// contoso.onmicrosoft.com). Well-known aliases such as "common",
	// "organizations", and "consumers" are NOT accepted. The operator
	// interpolates it into the token endpoint
	// (<authority>/<tenantId>/oauth2/v2.0/token), so the value is constrained to
	// the GUID/domain shape (no path metacharacters); the pattern and 253-char
	// cap mirror the enterprise exchanger's validateTenant, so any tenantId
	// admitted here is one the runtime can consume.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,})$`
	// +optional
	TenantID string `json:"tenantId,omitempty"`

	// Authority overrides the default Entra login host
	// (https://login.microsoftonline.com) for sovereign or national clouds, e.g.
	// https://login.microsoftonline.us (US Gov) or
	// https://login.partner.microsoftonline.cn (China). When set, the operator
	// builds the token endpoint by joining <authority>, <tenantId>, and the
	// v2.0 token path. Must be an HTTPS URL with no userinfo, query, fragment,
	// or trailing slash; a path IS permitted and is prefixed before the tenant
	// segment, as some sovereign / B2C / CIAM endpoints require. The OBO exchange
	// POSTs the client secret and the end-user assertion to this host, so it is a
	// credential trust boundary: HTTPS is required and userinfo (user@host) is
	// rejected to prevent host confusion (per RFC 3986 the real host follows the
	// "@", so https://login.microsoftonline.com@attacker.example targets
	// attacker.example). This is intentionally stricter than the downstream
	// exchanger's validateHTTPSURL, which also accepts http for loopback hosts
	// and tolerates a trailing slash — rejecting those at admission is the safe
	// direction.
	// +kubebuilder:validation:Pattern=`^https://[^\s?#@]+[^/\s?#@]$`
	// +optional
	Authority string `json:"authority,omitempty"`

	// ClientID is the confidential client's application (client) ID registered
	// in Entra. Emitted verbatim as the runtime contract's clientId.
	// Optional at the CRD level so future client-authentication methods (e.g.
	// certificate or workload-identity credentials, planned fast-follows) can be
	// added without a breaking schema change. The operator enforces that clientId
	// and clientSecretRef are both present for the v1 shared-secret flow.
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ClientSecretRef references a Kubernetes Secret containing the confidential
	// client's secret. v1 supports a shared client secret only. The operator
	// injects the resolved value into the proxyrunner pod as an environment
	// variable and emits only that variable's name in the runtime contract, as
	// clientSecretEnvVar — the secret value never travels in the contract.
	// Optional at the CRD level for the same forward-compatibility reason as
	// clientId (a certificate/workload-identity flow needs no client secret);
	// the operator enforces presence for the v1 shared-secret flow.
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// Audience is the backend target identifier requested in the exchanged
	// token. Used as the exchange target when Scopes is empty. At least one of
	// audience or scopes must be set; the operator enforces that at reconcile
	// (it is not an admission-time rule — see the type doc).
	// +optional
	Audience string `json:"audience,omitempty"`

	// Scopes are the delegated scopes to request for the exchanged token, e.g.
	// ["api://<backend>/.default"]. When non-empty they take precedence over
	// Audience. At least one of audience or scopes must be set; the operator
	// enforces that at reconcile. The MaxItems and per-item length caps are
	// defensive bounds on an otherwise unbounded list.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	// +listType=atomic
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// SubjectTokenProviderName selects the source of the OBO subject (assertion)
	// token from the request's authenticated Identity:
	//   - Omitted: use the inbound end-user token the client presented
	//     (Identity.Token) — the deployment with no embedded auth server, where
	//     the client holds an Entra token directly.
	//   - Set: use the named upstream provider's token
	//     (Identity.UpstreamTokens[<name>]) — the embedded-auth-server
	//     deployment, where the inbound token is the proxy's own session token.
	//     The value must match a configured upstream provider name.
	// The subject is always sourced from the authenticated Identity, never from
	// an inbound request header, so the upstream auth middleware must run first.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	// +optional
	SubjectTokenProviderName string `json:"subjectTokenProviderName,omitempty"`

	// CacheSkew overrides the OBO token cache's default expiry skew (the margin
	// by which a cached token is treated as expired before its real expiry),
	// e.g. "30s". The operator converts it to the runtime contract's
	// integer-seconds cacheSkewSeconds. Should not be negative, but the schema
	// does not enforce that — metav1.Duration carries no numeric minimum — and
	// upstream builds do not reject it. A negative value is rejected only by an
	// enterprise build's OBO handler once that handler validates the converted
	// parameters; it is not enforced at admission or in upstream-only builds.
	// When omitted, the cache default applies.
	// +optional
	CacheSkew *metav1.Duration `json:"cacheSkew,omitempty"`
}

// TokenExchangeConfig holds configuration for RFC-8693 OAuth 2.0 Token Exchange.
// This configuration is used to exchange incoming authentication tokens for tokens
// that can be used with external services.
// The structure matches the tokenexchange.Config from pkg/oauthproto/tokenexchange/middleware.go
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
	// +listType=atomic
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

	// SubjectProviderName is the name of the upstream provider whose token is used as the
	// RFC 8693 subject token instead of identity.Token when performing token exchange.
	// When left empty and an embedded authorization server is configured on the VirtualMCPServer,
	// the controller automatically populates this field with the first configured upstream
	// provider name. Set it explicitly to override that default or to select a specific
	// provider when multiple upstreams are configured.
	// +optional
	SubjectProviderName string `json:"subjectProviderName,omitempty"`
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
	// Must be a valid HTTPS URL (or HTTP for localhost, or HTTP for trusted in-cluster hosts when
	// insecureAllowHTTP is true) without query, fragment, or trailing slash (per RFC 8414).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://[^\s?#]+[^/\s?#]$`
	Issuer string `json:"issuer"`

	// AuthorizationEndpointBaseURL overrides the base URL used for the authorization_endpoint
	// in the OAuth discovery document. When set, the discovery document will advertise
	// `{authorizationEndpointBaseUrl}/oauth/authorize` instead of `{issuer}/oauth/authorize`.
	// All other endpoints (token, registration, JWKS) remain derived from the issuer.
	// This is useful when the browser-facing authorization endpoint needs to be on a
	// different host than the issuer used for backend-to-backend calls.
	// Must be a valid HTTPS URL (or HTTP for localhost, or HTTP for trusted in-cluster hosts
	// when insecureAllowHTTP is true) without query, fragment, or trailing slash.
	// +kubebuilder:validation:Pattern=`^https?://[^\s?#]+[^/\s?#]$`
	// +optional
	AuthorizationEndpointBaseURL string `json:"authorizationEndpointBaseUrl,omitempty"`

	// SigningKeySecretRefs references Kubernetes Secrets containing signing keys for JWT operations.
	// Supports key rotation by allowing multiple keys (oldest keys are used for verification only).
	// If not specified, an ephemeral signing key will be auto-generated (development only -
	// JWTs will be invalid after restart).
	// +kubebuilder:validation:MaxItems=5
	// +listType=atomic
	// +optional
	SigningKeySecretRefs []SecretKeyRef `json:"signingKeySecretRefs,omitempty"`

	// HMACSecretRefs references Kubernetes Secrets containing symmetric secrets for signing
	// authorization codes and refresh tokens (opaque tokens).
	// Current secret must be at least 32 bytes and cryptographically random.
	// Supports secret rotation via multiple entries (first is current, rest are for verification).
	// If not specified, an ephemeral secret will be auto-generated (development only -
	// auth codes and refresh tokens will be invalid after restart).
	// +listType=atomic
	// +optional
	HMACSecretRefs []SecretKeyRef `json:"hmacSecretRefs,omitempty"`

	// TokenLifespans configures the duration that various tokens are valid.
	// If not specified, defaults are applied (access: 1h, refresh: 7d, authCode: 10m).
	// +optional
	TokenLifespans *TokenLifespanConfig `json:"tokenLifespans,omitempty"`

	// UpstreamProviders configures connections to upstream Identity Providers.
	// The embedded auth server delegates authentication to these providers.
	// MCPServer and MCPRemoteProxy support a single upstream; VirtualMCPServer supports multiple.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	UpstreamProviders []UpstreamProviderConfig `json:"upstreamProviders"`

	// PrimaryUpstreamProvider names the upstream IDP whose access token Cedar
	// should read claims from when authorising a request. Must match the name
	// of one of the entries in UpstreamProviders. When empty, the controller
	// auto-selects the first entry of UpstreamProviders.
	//
	// Only meaningful on VirtualMCPServer, where multiple upstream providers
	// can be configured and Cedar needs to pick which token's claims to
	// evaluate. The VirtualMCPServer controller validates this field against
	// UpstreamProviders at admission and rejects unresolvable values.
	//
	// On MCPServer and MCPRemoteProxy this field is structurally present (the
	// EmbeddedAuthServerConfig struct is shared) but has no runtime effect:
	// those CRDs are restricted to a single upstream so there is no choice to
	// make. Setting it on those CRDs is silently ignored.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	PrimaryUpstreamProvider string `json:"primaryUpstreamProvider,omitempty"`

	// Storage configures the storage backend for the embedded auth server.
	// If not specified, defaults to in-memory storage.
	// +optional
	Storage *AuthServerStorageConfig `json:"storage,omitempty"`

	// DisableUpstreamTokenInjection prevents the embedded auth server from injecting
	// upstream IdP tokens into requests forwarded to the backend MCP server.
	// When true, the embedded auth server still handles OAuth flows for clients,
	// but instead of swapping ToolHive JWTs for upstream tokens the proxy STRIPS
	// the client's credential headers (Authorization, Cookie, Proxy-Authorization)
	// after validating the JWT — the backend receives an unauthenticated request.
	// Use headerForward to attach static credentials (e.g. an API key) if the
	// backend needs them. Cannot be combined with token exchange or AWS STS,
	// which would re-add credentials after the strip.
	// This is useful when the backend MCP server does not require authentication
	// (e.g., public documentation servers) but you still want client authentication.
	// +kubebuilder:default=false
	// +optional
	DisableUpstreamTokenInjection bool `json:"disableUpstreamTokenInjection,omitempty"`

	// InsecureAllowHTTP permits an http:// issuer URL for non-localhost hosts.
	// Only set this for in-cluster Kubernetes deployments where traffic between
	// pods traverses a trusted network (e.g. the in-cluster service mesh).
	// Production deployments reachable outside the cluster MUST use https://.
	//
	// On VirtualMCPServer: when false (the default), http:// issuers for non-localhost
	// hosts are rejected at reconcile time with an AuthServerConfigValidated=False condition.
	//
	// On MCPServer and MCPRemoteProxy (via MCPExternalAuthConfig): this field is
	// structurally present but enforcement is deferred to pod startup via Config.Validate();
	// a misconfigured issuer will cause the pod to crash at startup rather than surface
	// as an operator condition.
	// +kubebuilder:default=false
	// +optional
	InsecureAllowHTTP bool `json:"insecureAllowHTTP,omitempty"`

	// BaselineClientScopes is a baseline set of OAuth 2.0 scopes guaranteed to be
	// included in every client registration. The embedded auth server unions these
	// scopes into the registered set returned by RFC 7591 Dynamic Client
	// Registration, so a client that narrows the `scope` field at /oauth/register
	// can still request the baseline scopes at /oauth/authorize. All values must
	// be present in the upstream-derived scopesSupported set; the auth server
	// fails to start if any value is missing.
	//
	// Security: every client registered via /oauth/register will gain the
	// ability to request these scopes at /oauth/authorize, regardless of what
	// the client itself requested. Keep the baseline narrow (typically
	// "openid" and "offline_access"). Adding a privileged scope here — e.g.
	// "admin:read" — would grant it to every DCR-registered client, including
	// public clients like Claude Code, Cursor, and VS Code.
	// When cimd.enabled is true, every dynamically resolved CIMD client will
	// also gain the ability to request these scopes, including third-party
	// clients resolved from arbitrary HTTPS URLs.
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:Pattern=`^[\x21\x23-\x5B\x5D-\x7E]+$`
	// +listType=atomic
	// +optional
	BaselineClientScopes []string `json:"baselineClientScopes,omitempty"`

	// CIMD configures Client ID Metadata Document support. When omitted, CIMD is disabled.
	// +optional
	CIMD *EmbeddedAuthServerCIMDConfig `json:"cimd,omitempty"`
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

// EmbeddedAuthServerCIMDConfig configures Client ID Metadata Document (CIMD) support
// on the embedded authorization server. When enabled, the AS accepts HTTPS URLs as
// client_id values and resolves them via the CIMD protocol, allowing clients such as
// VS Code to authenticate without prior Dynamic Client Registration.
type EmbeddedAuthServerCIMDConfig struct {
	// Enabled activates CIMD client lookup. When false (the default), the AS only
	// accepts client_id values that were registered via DCR.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// CacheMaxSize is the maximum number of CIMD documents held in the LRU cache.
	// Defaults to 256 when Enabled is true and this field is omitted.
	// +kubebuilder:validation:Minimum=1
	// +optional
	CacheMaxSize int `json:"cacheMaxSize,omitempty"`

	// CacheFallbackTTL is the fixed TTL applied to every cached CIMD document.
	// Cache-Control header parsing is not yet implemented; all entries use this value.
	// Format: Go duration string (e.g. "5m", "10m", "1h").
	// Defaults to 5 minutes when Enabled is true and this field is omitted.
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	// +optional
	CacheFallbackTTL string `json:"cacheFallbackTtl,omitempty"`
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
//
// Exactly one of OIDCConfig or OAuth2Config must be set and must match the
// declared Type: oidc-typed providers set OIDCConfig, oauth2-typed providers
// set OAuth2Config. The CEL rule below enforces the pairing at admission; the
// matching Go-level check in validateUpstreamProvider provides defense-in-depth
// for stored objects.
//
// The rule is structured as a chain of equality checks ending in an explicit
// `false`, so adding a new UpstreamProviderType value without extending this
// rule fails admission instead of silently demanding the OAuth2 shape. When
// adding a new type, extend both this rule and validateUpstreamProvider.
//
// +kubebuilder:validation:XValidation:rule="self.type == 'oidc' ? (has(self.oidcConfig) && !has(self.oauth2Config)) : self.type == 'oauth2' ? (has(self.oauth2Config) && !has(self.oidcConfig)) : false",message="type must be 'oidc' or 'oauth2'; oidcConfig must be set when type is 'oidc' and oauth2Config must be set when type is 'oauth2' (and the other must not be set)"
//
//nolint:lll // CEL validation rules exceed line length limit
type UpstreamProviderConfig struct {
	// Name uniquely identifies this upstream provider.
	// Used for routing decisions and session binding in multi-upstream scenarios.
	// Must be lowercase alphanumeric with hyphens (DNS-label-like).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
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

	// ClientID is the OAuth 2.0 client identifier registered with the upstream IdP.
	// +kubebuilder:validation:Required
	ClientID string `json:"clientId"`

	// ClientSecretRef references a Kubernetes Secret containing the OAuth 2.0 client secret.
	// Optional for public clients using PKCE instead of client secret.
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// RedirectURI is the callback URL where the upstream IdP will redirect after authentication.
	// When not specified, defaults to `{resourceUrl}/oauth/callback` where `resourceUrl` is the
	// URL associated with the resource (e.g., MCPServer or vMCP) using this config.
	// +optional
	RedirectURI string `json:"redirectUri,omitempty"`

	// Scopes are the OAuth scopes to request from the upstream IdP.
	// If not specified, defaults to ["openid", "offline_access"].
	// When using additionalAuthorizationParams with provider-specific refresh token
	// mechanisms (e.g., Google's access_type=offline), set explicit scopes to avoid
	// sending both offline_access and the provider-specific parameter.
	// +listType=atomic
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// UserInfoOverride allows customizing UserInfo fetching behavior for OIDC providers.
	// By default, the UserInfo endpoint is discovered automatically via OIDC discovery.
	// Use this to override the endpoint URL, HTTP method, or field mappings for providers
	// that return non-standard claim names in their UserInfo response.
	// +optional
	UserInfoOverride *UserInfoConfig `json:"userInfoOverride,omitempty"`

	// AdditionalAuthorizationParams are extra query parameters to include in
	// authorization requests sent to the upstream provider.
	// This is useful for providers that require custom parameters, such as
	// Google's access_type=offline for obtaining refresh tokens.
	// Note: when using access_type=offline, also set explicit scopes to avoid
	// the default offline_access scope being sent alongside it.
	// Framework-managed parameters (response_type, client_id, redirect_uri,
	// scope, state, code_challenge, code_challenge_method, nonce) are not allowed.
	// +kubebuilder:validation:MaxProperties=16
	// +optional
	AdditionalAuthorizationParams map[string]string `json:"additionalAuthorizationParams,omitempty"`

	// SubjectClaim names the validated ID-token claim to use as the upstream
	// subject. Defaults to "sub" when empty. Set it for IdPs where "sub" isn't
	// stable per user — e.g. Entra/Azure AD, whose "sub" rotates per application
	// and whose stable identifier is "oid".
	//
	// The value is looked up verbatim as a top-level claim name, so it is
	// constrained to a claim-name shape: it must start with a letter or
	// underscore and contain only letters, digits, and underscores. This rejects
	// dotted, colon-namespaced, or whitespace-containing values at admission
	// rather than letting a typo silently miss the claim at login, and keeps the
	// field aligned with the directory service's per-issuer bindingClaim.
	//
	// Changing this on a live deployment re-keys existing users (the value
	// resolves to the internal user ID), so treat it as immutable once users
	// exist.
	//
	// Per-IdP notes:
	//   - Entra/Azure AD: use "oid"; it is only emitted when the upstream scopes
	//     include "profile". "oid" is unique within a single tenant — multi-tenant
	//     apps need oid+tid, which this single-claim field cannot express.
	//   - Okta: the org auth server already puts the stable id in "sub" (default
	//     works). A custom auth server's "sub" is the mutable login/email and the
	//     stable "uid" lives only in the access token, not the ID token — map a
	//     custom ID-token claim and point subjectClaim at it.
	// The pattern matches the claim-name shape and allows empty (defaults to
	// "sub"). Using Pattern rather than a CEL XValidation rule keeps this off the
	// CRD's CEL cost budget — a single-field format check via CEL is rejected by
	// the apiserver as too expensive once multiplied across the upstreams list.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^([a-zA-Z_][a-zA-Z0-9_]*)?$`
	SubjectClaim string `json:"subjectClaim,omitempty"`
}

// OAuth2UpstreamConfig contains configuration for pure OAuth 2.0 providers.
// OAuth 2.0 providers require explicit endpoint configuration.
//
// Exactly one of ClientID or DCRConfig must be set: ClientID is used when the
// client is pre-provisioned out of band, DCRConfig enables RFC 7591 Dynamic
// Client Registration at runtime.
//
// ClientSecretRef is mutually exclusive with DCRConfig: when DCRConfig is set,
// the client_secret is obtained from the registration response (RFC 7591
// §3.2.1) and any static ClientSecretRef would be either dead config or a
// competing source of truth. The XValidation rule below rejects the
// combination at admission; ValidateOAuth2DCRConfig is the matching
// reconcile-time backstop.
//
// Layered XOR behavior: the ClientID/DCRConfig rule treats `clientId: ""` as
// absent (size>0) but treats `dcrConfig: {}` as present (has() is true for an
// empty object). For input `{ clientId: "", dcrConfig: {} }` the outer rule
// passes and the inner DCRUpstreamConfig XOR fires with "exactly one of
// discoveryUrl or registrationEndpoint must be set". This is intentional —
// adding a non-empty subfield check (e.g.,
// `has(self.dcrConfig.discoveryUrl) || has(self.dcrConfig.registrationEndpoint)`)
// would inflate CEL cost on an already-budget-bound rule, and the inner
// message is still actionable.
//
// +kubebuilder:validation:XValidation:rule="(has(self.clientId) && size(self.clientId) > 0) ? !has(self.dcrConfig) : has(self.dcrConfig)",message="exactly one of clientId or dcrConfig must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.dcrConfig) && has(self.clientSecretRef))",message="clientSecretRef must not be set when dcrConfig is set; the client_secret is obtained at runtime via Dynamic Client Registration"
//
//nolint:lll // CEL validation rules exceed line length limit
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
	// When omitted and IdentityFromToken is also unset, the embedded auth server runs in
	// synthesis mode for this upstream: a non-PII subject derived from the access token, no
	// Name/Email. Use this shape for upstreams with no userinfo surface and no identity in
	// the token response (e.g., MCP authorization servers per the MCP spec). When
	// IdentityFromToken is set instead, identity is resolved from the token response body
	// (e.g., Snowflake's "username" field, Slack's "authed_user.id"); the userinfo HTTP call
	// is skipped entirely.
	// +optional
	UserInfo *UserInfoConfig `json:"userInfo,omitempty"`

	// ClientID is the OAuth 2.0 client identifier registered with the upstream IDP.
	// Mutually exclusive with DCRConfig: when DCRConfig is set, ClientID is obtained
	// at runtime via RFC 7591 Dynamic Client Registration and must be left empty.
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ClientSecretRef references a Kubernetes Secret containing the OAuth 2.0 client secret.
	// Optional for public clients using PKCE instead of client secret.
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// RedirectURI is the callback URL where the upstream IdP will redirect after authentication.
	// When not specified, defaults to `{resourceUrl}/oauth/callback` where `resourceUrl` is the
	// URL associated with the resource (e.g., MCPServer or vMCP) using this config.
	// +optional
	RedirectURI string `json:"redirectUri,omitempty"`

	// Scopes are the OAuth scopes to request from the upstream IdP.
	// +listType=atomic
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// TokenResponseMapping configures custom field extraction from non-standard token responses.
	// Some OAuth providers (e.g., GovSlack) nest token fields under non-standard paths
	// instead of returning them at the top level. When set, ToolHive performs the token
	// exchange HTTP call directly and extracts fields using the configured dot-notation paths.
	// If nil, standard OAuth 2.0 token response parsing is used.
	// For extracting user identity from the token response, see IdentityFromToken.
	// +optional
	TokenResponseMapping *TokenResponseMapping `json:"tokenResponseMapping,omitempty"`

	// IdentityFromToken extracts user identity (subject, name, email) directly
	// from the OAuth2 token-endpoint response body using gjson dot-notation paths.
	// When set, the embedded auth server skips the userinfo HTTP call entirely
	// and resolves identity from the token response. See IdentityFromTokenConfig
	// for trust-model and uniqueness considerations.
	// +optional
	IdentityFromToken *IdentityFromTokenConfig `json:"identityFromToken,omitempty"`

	// AdditionalAuthorizationParams are extra query parameters to include in
	// authorization requests sent to the upstream provider.
	// This is useful for providers that require custom parameters, such as
	// Google's access_type=offline for obtaining refresh tokens.
	// Framework-managed parameters (response_type, client_id, redirect_uri,
	// scope, state, code_challenge, code_challenge_method, nonce) are not allowed.
	// +kubebuilder:validation:MaxProperties=16
	// +optional
	AdditionalAuthorizationParams map[string]string `json:"additionalAuthorizationParams,omitempty"`

	// DCRConfig enables RFC 7591 Dynamic Client Registration against the upstream
	// authorization server. When set, the client credentials are obtained at
	// runtime rather than being pre-provisioned, and ClientID must be left empty.
	// Mutually exclusive with ClientID.
	// +optional
	DCRConfig *DCRUpstreamConfig `json:"dcrConfig,omitempty"`
}

// DCRUpstreamConfig configures RFC 7591 Dynamic Client Registration for an
// OAuth 2.0 upstream. When present on an OAuth2 upstream, the authserver
// performs registration at runtime to obtain client credentials, replacing
// the need to pre-provision a ClientID.
//
// Exactly one of DiscoveryURL or RegistrationEndpoint must be set. DiscoveryURL
// points at an RFC 8414 / OIDC Discovery document from which the registration
// endpoint is resolved; RegistrationEndpoint is used directly when the upstream
// does not publish discovery metadata.
//
// The XOR rule uses has() alone (not has() + size() > 0) to keep the rule's
// estimated CEL cost under the apiserver's per-rule static budget. With
// `omitempty` on both fields, an unset field is absent on the wire and has()
// returns false; the explicit-empty-string edge case is rejected at reconcile
// time by ValidateOAuth2DCRConfig.
//
// +kubebuilder:validation:XValidation:rule="has(self.discoveryUrl) != has(self.registrationEndpoint)",message="exactly one of discoveryUrl or registrationEndpoint must be set"
//
//nolint:lll // CEL validation rules exceed line length limit
type DCRUpstreamConfig struct {
	// DiscoveryURL is the RFC 8414 / OIDC Discovery document URL. The resolver
	// issues a single GET against this URL (no well-known-path fallback) and
	// reads registration_endpoint, authorization_endpoint, token_endpoint,
	// token_endpoint_auth_methods_supported, and scopes_supported from the
	// response.
	// Mutually exclusive with RegistrationEndpoint.
	// HTTPS is required because the registration endpoint resolved from this
	// document carries the initial access token and the issued client_secret
	// (RFC 7591 §3, RFC 8414 §3). MaxLength is a defensive size cap (etcd
	// object budget, regex evaluation cost) and matches the conventional URL
	// length cap.
	// +optional
	// +kubebuilder:validation:Pattern=`^https://[^\s?#]+[^/\s?#]$`
	// +kubebuilder:validation:MaxLength=2048
	DiscoveryURL string `json:"discoveryUrl,omitempty"`

	// RegistrationEndpoint is the RFC 7591 registration endpoint URL used
	// directly, bypassing discovery. When using this field, the caller is
	// expected to also supply AuthorizationEndpoint, TokenEndpoint, and an
	// explicit Scopes list on the parent OAuth2UpstreamConfig.
	// Mutually exclusive with DiscoveryURL.
	// HTTPS is required because the registration endpoint carries the initial
	// access token and the issued client_secret (RFC 7591 §3, RFC 8414 §3).
	// MaxLength is a defensive size cap (etcd object budget, regex evaluation
	// cost) and matches the conventional URL length cap.
	// +optional
	// +kubebuilder:validation:Pattern=`^https://[^\s?#]+[^/\s?#]$`
	// +kubebuilder:validation:MaxLength=2048
	RegistrationEndpoint string `json:"registrationEndpoint,omitempty"`

	// InitialAccessTokenRef is an optional reference to a Kubernetes Secret
	// carrying an RFC 7591 §3 initial access token. When set, the resolver
	// presents the token value as a Bearer credential on the registration
	// request. Mirrors the ClientSecretRef pattern.
	// +optional
	InitialAccessTokenRef *SecretKeyRef `json:"initialAccessTokenRef,omitempty"`

	// SoftwareID is the RFC 7591 "software_id" registration metadata value,
	// identifying the client software independent of any particular
	// registration instance. Typically a UUID or short identifier.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	SoftwareID string `json:"softwareId,omitempty"`

	// SoftwareStatement is the RFC 7591 "software_statement" JWT asserting
	// metadata about the client software, signed by a party the authorization
	// server trusts.
	//
	// Stored inline on the CR. The JWT is signed but not encrypted, so its
	// contents are visible to anyone with get/list/watch on this resource and
	// appear in etcd backups in plaintext. Treat the value as non-confidential
	// (signed attestation, not a secret). Operators that rotate software
	// statements like bearer credentials should keep them at the authorization
	// server side and rely on the registration endpoint's initial access
	// token (see InitialAccessTokenRef) instead of placing them on the CR.
	//
	// Bounded to 16384 characters as a defensive size cap (etcd object
	// budget, regex evaluation cost). Real-world signed statements with
	// embedded x5c certificate chains, JWKS keys, or OIDC-Federation
	// trust-framework metadata routinely exceed 4 KB.
	// +optional
	// +kubebuilder:validation:MaxLength=16384
	SoftwareStatement string `json:"softwareStatement,omitempty"`
}

// IdentityFromTokenConfig extracts user identity (subject, name, email) directly from the
// OAuth2 token-endpoint response body using gjson dot-notation paths. When configured on an
// OAuth2UpstreamConfig, the embedded auth server skips the userinfo HTTP call entirely and
// resolves identity from the token response.
//
// Paths use gjson dot-notation, where each segment is a JSON object key. For example,
// "username" extracts a top-level field, and "authed_user.id" extracts a nested field.
//
// Trust-model warning: Identity claims extracted via this block are not cryptographically
// verified — they are trusted only via the TLS connection to the token endpoint. Prefer
// OIDC + ID token validation when verifiable claims are required.
//
// Subject uniqueness is scoped to the upstream provider entry. To keep identity namespaces
// separate across multiple instances of the same provider (e.g., two Snowflake accounts),
// use distinct upstream provider entries.
type IdentityFromTokenConfig struct {
	// SubjectPath is the dot-notation path to the subject (user ID) field in the token response.
	// Warning: claims read from the token response are trusted only via TLS, not
	// cryptographically verified; prefer OIDC ID tokens when verifiable claims are required.
	// Example: "authed_user.id" for Slack (top-level token-response field). For providers
	// whose token response embeds the access token as a JWT (e.g. Snowflake), use the
	// "@upstreamjwt" modifier to decode the payload, e.g. "access_token|@upstreamjwt|sub".
	// The "@upstreamjwt" modifier performs no signature verification either.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	SubjectPath string `json:"subjectPath"`

	// NamePath is the dot-notation path to the display name field in the token response.
	// If not specified or if the path does not resolve to a string, the display name is omitted.
	// Omit the field entirely rather than setting it to an empty string.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	NamePath string `json:"namePath,omitempty"`

	// EmailPath is the dot-notation path to the email address field in the token response.
	// If not specified or if the path does not resolve to a string, the email is omitted.
	// Omit the field entirely rather than setting it to an empty string.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	EmailPath string `json:"emailPath,omitempty"`
}

// TokenResponseMapping maps non-standard token response fields to standard OAuth 2.0 fields
// using dot-notation JSON paths. This supports upstream providers like GovSlack that nest
// the access token under paths like "authed_user.access_token".
//
// For extracting user identity from the token response, see IdentityFromToken.
type TokenResponseMapping struct {
	// AccessTokenPath is the dot-notation path to the access token in the response.
	// Example: "authed_user.access_token"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	AccessTokenPath string `json:"accessTokenPath"`

	// ScopePath is the dot-notation path to the scope string in the response.
	// If not specified, defaults to "scope".
	// +optional
	ScopePath string `json:"scopePath,omitempty"`

	// RefreshTokenPath is the dot-notation path to the refresh token in the response.
	// If not specified, defaults to "refresh_token".
	// +optional
	RefreshTokenPath string `json:"refreshTokenPath,omitempty"`

	// ExpiresInPath is the dot-notation path to the expires_in value (in seconds).
	// If not specified, defaults to "expires_in".
	// +optional
	ExpiresInPath string `json:"expiresInPath,omitempty"`
}

// UserInfoConfig contains configuration for fetching user information from an upstream provider.
// This supports both standard OIDC UserInfo endpoints and custom provider-specific endpoints
// like GitHub's /user API. For providers that do not expose a usable userinfo endpoint but
// include identity in the OAuth2 token response, use IdentityFromToken on OAuth2UpstreamConfig
// instead.
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
	// +listType=atomic
	// +optional
	SubjectFields []string `json:"subjectFields,omitempty"`

	// NameFields is an ordered list of field names to try for the display name.
	// The first non-empty value found will be used.
	// Default: ["name"]
	// +listType=atomic
	// +optional
	NameFields []string `json:"nameFields,omitempty"`

	// EmailFields is an ordered list of field names to try for the email address.
	// The first non-empty value found will be used.
	// Default: ["email"]
	// +listType=atomic
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
// Exactly one of addr or sentinelConfig must be set. Set clusterMode to true when
// addr points to a Redis Cluster discovery endpoint (GCP Memorystore Cluster,
// AWS ElastiCache cluster mode enabled).
//
// +kubebuilder:validation:XValidation:rule="(has(self.addr) && self.addr.size() > 0) != has(self.sentinelConfig)",message="exactly one of addr or sentinelConfig must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.clusterMode) && self.clusterMode) || (has(self.addr) && self.addr.size() > 0)",message="clusterMode requires addr to be set"
//
//nolint:lll // CEL validation rules exceed line length limit
type RedisStorageConfig struct {
	// Addr is the Redis server address (host:port). Required for standalone and cluster modes.
	// Use for managed Redis services that expose a single endpoint (GCP Memorystore basic tier,
	// AWS ElastiCache without cluster mode, or cluster-mode services when clusterMode is true).
	// Mutually exclusive with sentinelConfig.
	// +optional
	Addr string `json:"addr,omitempty"`

	// ClusterMode enables the Redis Cluster protocol. Set to true when addr points to a
	// Redis Cluster discovery endpoint (e.g., GCP Memorystore Cluster, AWS ElastiCache
	// cluster mode enabled). Requires addr to be set.
	// +optional
	ClusterMode bool `json:"clusterMode,omitempty"`

	// SentinelConfig holds Redis Sentinel configuration.
	// Use for self-managed Redis with Sentinel-based HA. Mutually exclusive with addr.
	// +optional
	SentinelConfig *RedisSentinelConfig `json:"sentinelConfig,omitempty"`

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

	// TLS configures TLS for connections to the Redis/Valkey master or cluster nodes.
	// Presence of this field enables TLS. Omit to use plaintext.
	// +optional
	TLS *RedisTLSConfig `json:"tls,omitempty"`

	// SentinelTLS configures TLS for connections to Sentinel instances.
	// Only applies when sentinelConfig is set. Presence of this field enables TLS.
	// +optional
	SentinelTLS *RedisTLSConfig `json:"sentinelTls,omitempty"`
}

// RedisSentinelConfig configures Redis Sentinel connection.
type RedisSentinelConfig struct {
	// MasterName is the name of the Redis master monitored by Sentinel.
	// +kubebuilder:validation:Required
	MasterName string `json:"masterName"`

	// SentinelAddrs is a list of Sentinel host:port addresses.
	// Mutually exclusive with SentinelService.
	// +listType=atomic
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

// RedisTLSConfig configures TLS for Redis connections.
// Presence of this struct on a connection type enables TLS for that connection.
type RedisTLSConfig struct {
	// InsecureSkipVerify skips TLS certificate verification.
	// Use when connecting to services with self-signed certificates.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// CACertSecretRef references a Secret containing a PEM-encoded CA certificate
	// for verifying the server. When not specified, system root CAs are used.
	// +optional
	CACertSecretRef *SecretKeyRef `json:"caCertSecretRef,omitempty"`
}

// RedisACLUserConfig configures Redis ACL user authentication.
type RedisACLUserConfig struct {
	// UsernameSecretRef references a Secret containing the Redis ACL username.
	// When omitted, connections use legacy password-only AUTH. Omit for managed
	// Redis tiers that do not support ACL users (e.g. GCP Memorystore Basic/Standard
	// HA, Azure Cache for Redis). Set for services that support ACL users (e.g. AWS
	// ElastiCache non-cluster with Redis 6+ RBAC).
	// +optional
	UsernameSecretRef *SecretKeyRef `json:"usernameSecretRef,omitempty"`

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
	// +listType=atomic
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

	// SubjectProviderName is the name of the upstream provider whose access token
	// is used as the web identity token for STS AssumeRoleWithWebIdentity.
	// This field is used exclusively by VirtualMCPServer, where there is no
	// upstream swap middleware to replace the bearer token before the strategy runs.
	// When left empty and an embedded authorization server is configured on the
	// VirtualMCPServer, the controller automatically populates this field with
	// the first configured upstream provider name. Set it explicitly to override
	// that default or to select a specific provider when multiple upstreams are
	// configured.
	// When no embedded auth server is present, the bearer token from the incoming
	// request's Authorization header is used instead.
	// +optional
	SubjectProviderName string `json:"subjectProviderName,omitempty"`
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

// UpstreamInjectSpec holds configuration for upstream token injection.
// This strategy injects an upstream IdP access token obtained by the embedded
// authorization server into backend requests as the Authorization: Bearer header.
type UpstreamInjectSpec struct {
	// ProviderName is the name of the upstream IdP provider whose access token
	// should be injected as the Authorization: Bearer header.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ProviderName string `json:"providerName"`
}

// Condition types specific to MCPExternalAuthConfig and the inline embedded
// auth server config it shares with VirtualMCPServer.
const (
	// ConditionTypeIdentitySynthesized is an advisory set to True when at
	// least one OAuth2 upstream has no real-identity source configured
	// (neither userInfo nor identityFromToken). The embedded auth server
	// then synthesizes its subject from the access token, with no
	// Name/Email claims. Surfaces on resources that own the upstream
	// declaration so a missing identity source is visible in
	// `kubectl describe` instead of only in proxyrunner logs.
	ConditionTypeIdentitySynthesized = "IdentitySynthesized"
)

// Condition reasons for ConditionTypeIdentitySynthesized.
const (
	// ConditionReasonIdentitySynthesizedActive: one or more OAuth2 upstreams
	// have neither userInfo nor identityFromToken configured. The condition
	// message names the affected upstream(s).
	ConditionReasonIdentitySynthesizedActive = "OAuth2UpstreamWithoutUserInfo"

	// ConditionReasonIdentitySynthesizedInactive: every OAuth2 upstream has
	// a real-identity source (userInfo or identityFromToken); real identity
	// is being resolved.
	ConditionReasonIdentitySynthesizedInactive = "AllUpstreamsHaveUserInfo"
)

// Condition reasons for ConditionTypeValid on OBO-typed configs. These
// literals are part of the user-facing contract — external consumers and
// downstream tooling pattern-match on them.
const (
	// ConditionReasonEnterpriseRequired: an obo-typed MCPExternalAuthConfig
	// requires an enterprise build that has registered an OBO handler via
	// controllerutil.RegisterOBOHandler. Upstream-only builds surface this
	// reason for every obo-typed config.
	ConditionReasonEnterpriseRequired = "EnterpriseRequired"

	// ConditionReasonInvalidConfig: an obo-typed MCPExternalAuthConfig is
	// well-formed at the CRD level but fails the registered OBO handler's
	// Validate() with an error other than the enterprise-required sentinel.
	// Used by out-of-tree handlers; unreachable in upstream-only builds.
	ConditionReasonInvalidConfig = "InvalidConfig"
)

// XAASpec holds configuration for the XAA (Cross-Application Access) auth strategy.
// XAA implements draft-ietf-oauth-identity-assertion-authz-grant (ID-JAG) — a
// two-step token exchange to obtain access tokens for target services:
//   - IdP exchange (RFC 8693): Exchange the user's ID token at their IdP for an ID-JAG JWT
//   - Target grant (RFC 7523): Exchange the ID-JAG at the target app's AS for an access token
type XAASpec struct {
	// IDPTokenURL is the IdP token endpoint for IdP exchange (RFC 8693).
	// Must be a valid HTTPS URL.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https://.*$`
	IDPTokenURL string `json:"idpTokenUrl"`

	// IDPClientID is the OAuth client ID at the IdP for IdP exchange.
	// +optional
	IDPClientID string `json:"idpClientId,omitempty"`

	// IDPClientSecretRef references a Kubernetes Secret containing the IdP client secret.
	// +optional
	IDPClientSecretRef *SecretKeyRef `json:"idpClientSecretRef,omitempty"`

	// TargetTokenURL is the target AS token endpoint for target grant (RFC 7523).
	// +kubebuilder:validation:Required
	TargetTokenURL string `json:"targetTokenUrl"`

	// InsecureTargetTokenURL allows plain HTTP for TargetTokenURL.
	// WARNING: this is insecure and must only be set for in-cluster or
	// development/testing endpoints — never in production.
	// +optional
	InsecureTargetTokenURL bool `json:"insecureTargetTokenUrl,omitempty"`

	// TargetClientID is the OAuth client ID at the target AS for target grant.
	// ID-JAG draft §9.1 RECOMMENDS confidential clients for target grant; most
	// conformant target authorization servers will reject an unauthenticated
	// JWT-bearer grant per the §4.4.1 client_id continuity requirement.
	// +optional
	TargetClientID string `json:"targetClientId,omitempty"`

	// TargetClientSecretRef references a Kubernetes Secret for the target AS client secret.
	// +optional
	TargetClientSecretRef *SecretKeyRef `json:"targetClientSecretRef,omitempty"`

	// TargetAudience is the resource AS URL for the ID-JAG audience claim.
	// +kubebuilder:validation:Required
	TargetAudience string `json:"targetAudience"`

	// TargetResource is the RFC 8707 resource indicator sent as the `resource`
	// parameter in IdP exchange (RFC 8693, draft §4.3, OPTIONAL). It
	// identifies the target resource server — not the access-token audience, which
	// is governed by TargetAudience. For MCP backends, set to the MCP server URL.
	// Some authorization servers (e.g. Okta's early ID-JAG implementation) require
	// this parameter in practice despite the draft marking it optional — set it
	// when your IdP needs it.
	// +optional
	TargetResource string `json:"targetResource,omitempty"`

	// Scopes are the requested scopes for the XAA exchange (IdP exchange and target grant).
	// +listType=atomic
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// SubjectProviderName selects which upstream provider's ID token to use.
	// When left empty and an embedded authorization server is configured,
	// the controller automatically populates this field with the first configured
	// upstream provider name.
	// +optional
	SubjectProviderName string `json:"subjectProviderName,omitempty"`

	// SubjectTokenType is the token-type URN of the upstream subject token
	// used in IdP exchange. Defaults to "urn:ietf:params:oauth:token-type:id_token"
	// when empty.
	// +kubebuilder:validation:Enum="urn:ietf:params:oauth:token-type:id_token"
	// +optional
	SubjectTokenType string `json:"subjectTokenType,omitempty"`
}

// MCPExternalAuthConfigStatus defines the observed state of MCPExternalAuthConfig
type MCPExternalAuthConfigStatus struct {
	// Conditions represent the latest available observations of the MCPExternalAuthConfig's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed for this MCPExternalAuthConfig.
	// It corresponds to the MCPExternalAuthConfig's generation, which is updated on mutation by the API Server.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the current configuration for change detection
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ReferenceCount is the number of workloads referencing this config.
	// +optional
	ReferenceCount int32 `json:"referenceCount,omitempty"`

	// ReferencingWorkloads is a list of workload resources that reference this MCPExternalAuthConfig.
	// Each entry identifies the workload by kind and name. The map key is the
	// (kind, name) pair so two workloads of different kinds that share a name
	// (e.g., an MCPServer "foo" and a VirtualMCPServer "foo") are distinct
	// entries rather than colliding under merge-patch semantics.
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	// +optional
	ReferencingWorkloads []WorkloadReference `json:"referencingWorkloads,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
// +kubebuilder:resource:shortName=extauth;mcpextauth,categories=toolhive
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
// +kubebuilder:printcolumn:name="References",type=integer,JSONPath=`.status.referenceCount`
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
// Note: These validations provide defense-in-depth alongside the
// +kubebuilder:validation:XValidation markers on MCPExternalAuthConfigSpec.
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
	case ExternalAuthTypeUpstreamInject:
		if r.Spec.UpstreamInject == nil || r.Spec.UpstreamInject.ProviderName == "" {
			return fmt.Errorf("upstreamInject requires a non-empty providerName")
		}
		return nil
	case ExternalAuthTypeXAA:
		if r.Spec.XAA == nil {
			return fmt.Errorf("xaa requires configuration")
		}
		return nil
	case ExternalAuthTypeTokenExchange,
		ExternalAuthTypeHeaderInjection,
		ExternalAuthTypeBearerToken,
		ExternalAuthTypeUnauthenticated:
		// No complex validation needed for these types
		return nil
	case ExternalAuthTypeOBO:
		// Structural validation (the OBO field is set iff Type is "obo")
		// has already run via r.validateTypeConfigConsistency() at the top
		// of this method, so this arm is reached only when the structural
		// invariant holds — and the matching CEL rule on the spec catches
		// it at admission time. OBOConfig carries no required field and no
		// cross-field CEL rule: spec.obo shipped as an empty placeholder in an
		// earlier release, so a required field or a rule rejecting {} would be a
		// backward-incompatible narrowing. The kubebuilder markers check only
		// per-field shape (tenantId GUID/domain, authority URL,
		// subjectTokenProviderName, scopes/cacheSkew bounds) at admission, and
		// only for values that are present. All presence and combination
		// requirements (a tenant, a client-auth credential, at least one of
		// audience/scopes) and protocol-level checks are owned by the registered
		// handler, not the upstream type. That handler runs at reconcile
		// time via the controllerutil.OBOValidate function-pointer hook:
		// upstream-only builds return obo.ErrEnterpriseRequired, which the
		// reconciler maps to status.conditions[Valid] = False / Reason:
		// EnterpriseRequired. Out-of-tree builds that register a handler via
		// controllerutil.RegisterOBOHandler short-circuit the sentinel and
		// run their own protocol-level checks. Splitting the tiers this way
		// keeps the upstream CRD schema stable across builds.
		return nil
	default:
		// Unknown type - should be caught by enum validation, but handle defensively
		return fmt.Errorf("unsupported auth type: %s", r.Spec.Type)
	}
}

// typeConfigEntry maps an ExternalAuthType to its corresponding config field presence check.
type typeConfigEntry struct {
	authType  ExternalAuthType
	fieldName string
	isSet     bool
}

// validateTypeConfigConsistency validates that the correct config is set for the selected type.
// This mirrors the CEL validation rules but provides defense-in-depth for stored objects.
//
// Each ExternalAuthType with a config sub-field is one row in the typeConfigEntry
// table, checked by a uniform biconditional in the loop below ("config set iff
// type matches"). OBO is checked separately because its handler is registered
// out-of-tree; the unauthenticated guard then asserts no config is set for the
// unauthenticated type. Each row still maps one-to-one onto the corresponding
// CEL XValidation rule on MCPExternalAuthConfigSpec, so a reviewer can audit the
// structural-validation contract by comparing the table against the markers.
// See issue #5329 for the broader discussion.
func (r *MCPExternalAuthConfig) validateTypeConfigConsistency() error {
	entries := []typeConfigEntry{
		{ExternalAuthTypeTokenExchange, "tokenExchange", r.Spec.TokenExchange != nil},
		{ExternalAuthTypeHeaderInjection, "headerInjection", r.Spec.HeaderInjection != nil},
		{ExternalAuthTypeBearerToken, "bearerToken", r.Spec.BearerToken != nil},
		{ExternalAuthTypeEmbeddedAuthServer, "embeddedAuthServer", r.Spec.EmbeddedAuthServer != nil},
		{ExternalAuthTypeAWSSts, "awsSts", r.Spec.AWSSts != nil},
		{ExternalAuthTypeUpstreamInject, "upstreamInject", r.Spec.UpstreamInject != nil},
		{ExternalAuthTypeXAA, "xaa", r.Spec.XAA != nil},
	}
	if (r.Spec.OBO == nil) == (r.Spec.Type == ExternalAuthTypeOBO) {
		return fmt.Errorf("obo configuration must be set if and only if type is 'obo'")
	}

	for _, e := range entries {
		wantSet := r.Spec.Type == e.authType
		if e.isSet != wantSet {
			return fmt.Errorf("%s configuration must be set if and only if type is '%s'", e.fieldName, e.authType)
		}
	}

	// Redundant with the per-type biconditionals above — each fires first for
	// Type=Unauthenticated with any non-nil field — but retained as a single
	// readable invariant so a contributor adding a new ExternalAuthType extends
	// the "no configuration must be set" check here too.
	if r.Spec.Type == ExternalAuthTypeUnauthenticated {
		if r.Spec.OBO != nil {
			return fmt.Errorf("no configuration must be set when type is 'unauthenticated'")
		}
		for _, e := range entries {
			if e.isSet {
				return fmt.Errorf("no configuration must be set when type is 'unauthenticated'")
			}
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
	// Note: multi-upstream is accepted at the CRD level. Consumer controllers
	// (MCPServer, MCPRemoteProxy) enforce single-upstream restrictions;
	// VirtualMCPServer allows multiple upstreams.

	seen := make(map[string]bool, len(cfg.UpstreamProviders))
	for i, provider := range cfg.UpstreamProviders {
		if seen[provider.Name] {
			return fmt.Errorf("upstreamProviders[%d]: duplicate name %q", i, provider.Name)
		}
		seen[provider.Name] = true

		if err := r.validateUpstreamProvider(i, &provider); err != nil {
			return err
		}
	}

	return nil
}

// validateUpstreamProvider validates a single upstream provider configuration.
// The discriminator check mirrors the combined CEL XValidation rule on
// UpstreamProviderConfig: a single boolean test produces a single message that
// matches what admission emits, so reconcile-time and admission-time errors
// stay aligned.
func (*MCPExternalAuthConfig) validateUpstreamProvider(index int, provider *UpstreamProviderConfig) error {
	prefix := fmt.Sprintf("upstreamProviders[%d]", index)

	typeOK := provider.Type == UpstreamProviderTypeOIDC || provider.Type == UpstreamProviderTypeOAuth2
	configOK := (provider.Type == UpstreamProviderTypeOIDC && provider.OIDCConfig != nil && provider.OAuth2Config == nil) ||
		(provider.Type == UpstreamProviderTypeOAuth2 && provider.OAuth2Config != nil && provider.OIDCConfig == nil)
	if !typeOK || !configOK {
		return fmt.Errorf("%s: type must be 'oidc' or 'oauth2'; oidcConfig must be set when type is 'oidc' "+
			"and oauth2Config must be set when type is 'oauth2' (and the other must not be set)", prefix)
	}

	// Validate OAuth2-specific constraints (defense-in-depth with CEL).
	// The discriminator above guarantees OAuth2Config != nil when type is oauth2.
	if provider.Type == UpstreamProviderTypeOAuth2 {
		if err := ValidateOAuth2DCRConfig(provider.OAuth2Config); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
		if err := validateOAuth2UpstreamConfig(provider.OAuth2Config); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
	}

	// Validate additionalAuthorizationParams does not contain reserved keys
	return ValidateAdditionalAuthorizationParams(prefix, provider.AdditionalAuthorizationParams())
}

// Length caps for DCR-related string fields. Mirror the
// +kubebuilder:validation:MaxLength markers on DCRUpstreamConfig so that
// ValidateOAuth2DCRConfig is a true reconcile-time backstop for length
// constraints, not just for the XOR rules.
const (
	// MaxDCRURLLength matches the MaxLength marker on
	// DCRUpstreamConfig.DiscoveryURL and DCRUpstreamConfig.RegistrationEndpoint.
	MaxDCRURLLength = 2048

	// MaxSoftwareStatementLength matches the MaxLength marker on
	// DCRUpstreamConfig.SoftwareStatement.
	MaxSoftwareStatementLength = 16384
)

// ValidateOAuth2DCRConfig enforces the mutual exclusivity between ClientID and
// DCRConfig, between ClientSecretRef and DCRConfig, and (when DCRConfig is
// present) between DiscoveryURL and RegistrationEndpoint. It also enforces the
// MaxLength caps declared on DCRUpstreamConfig so reconcile-time matches
// admission-time. These rules mirror the CEL validation on OAuth2UpstreamConfig
// and DCRUpstreamConfig and provide defense-in-depth for stored objects (e.g.,
// objects stored before CEL rules were added or validated through code paths
// that bypass admission).
//
// Errors are scoped to "oauth2Config[.dcrConfig[.field]]" so callers can wrap
// with their own outer scope (e.g. "upstreamProviders[i]: %w" or
// "upstream %q: %w") without duplicating the field name.
//
// Exported so the controllerutil conversion layer can reuse the same
// invariants when building runtime configs, rejecting malformed objects at
// reconcile time rather than waiting until the authserver process parses them.
func ValidateOAuth2DCRConfig(cfg *OAuth2UpstreamConfig) error {
	hasClientID := cfg.ClientID != ""
	hasDCR := cfg.DCRConfig != nil

	if hasClientID == hasDCR {
		return fmt.Errorf("oauth2Config: exactly one of clientId or dcrConfig must be set")
	}

	if !hasDCR {
		return nil
	}

	if cfg.ClientSecretRef != nil {
		return fmt.Errorf(
			"oauth2Config: clientSecretRef must not be set when dcrConfig is set; " +
				"the client_secret is obtained at runtime via Dynamic Client Registration")
	}

	hasDiscoveryURL := cfg.DCRConfig.DiscoveryURL != ""
	hasRegistrationEndpoint := cfg.DCRConfig.RegistrationEndpoint != ""
	if hasDiscoveryURL == hasRegistrationEndpoint {
		return fmt.Errorf("oauth2Config.dcrConfig: exactly one of discoveryUrl or registrationEndpoint must be set")
	}

	if l := len(cfg.DCRConfig.DiscoveryURL); l > MaxDCRURLLength {
		return fmt.Errorf("oauth2Config.dcrConfig.discoveryUrl: length %d exceeds maximum %d", l, MaxDCRURLLength)
	}
	if l := len(cfg.DCRConfig.RegistrationEndpoint); l > MaxDCRURLLength {
		return fmt.Errorf("oauth2Config.dcrConfig.registrationEndpoint: length %d exceeds maximum %d", l, MaxDCRURLLength)
	}
	if l := len(cfg.DCRConfig.SoftwareStatement); l > MaxSoftwareStatementLength {
		return fmt.Errorf(
			"oauth2Config.dcrConfig.softwareStatement: length %d exceeds maximum %d",
			l, MaxSoftwareStatementLength)
	}
	return nil
}

// validateOAuth2UpstreamConfig validates OAuth2-specific upstream provider configuration
// beyond the DCR / ClientID constraints handled by ValidateOAuth2DCRConfig. Errors are
// scoped to "oauth2Config[.field]" so callers can wrap with their own outer scope (e.g.
// "upstreamProviders[i]: %w") without duplicating the field name, matching the contract
// of ValidateOAuth2DCRConfig.
func validateOAuth2UpstreamConfig(cfg *OAuth2UpstreamConfig) error {
	if cfg.IdentityFromToken != nil && cfg.IdentityFromToken.SubjectPath == "" {
		return fmt.Errorf("oauth2Config.identityFromToken.subjectPath must not be empty when identityFromToken is set")
	}
	return nil
}

// AdditionalAuthorizationParams returns the additional authorization parameters
// from whichever upstream config is set, or nil if none.
func (p *UpstreamProviderConfig) AdditionalAuthorizationParams() map[string]string {
	if p.OIDCConfig != nil {
		return p.OIDCConfig.AdditionalAuthorizationParams
	}
	if p.OAuth2Config != nil {
		return p.OAuth2Config.AdditionalAuthorizationParams
	}
	return nil
}

// SyntheticIdentityUpstreams returns the names of OAuth2 upstreams running
// in synthesis mode (neither userInfo nor identityFromToken configured),
// sorted lexically for deterministic condition messages. OIDC upstreams are
// skipped — they always have an ID-token-derived subject. Upstreams with
// identityFromToken are also skipped — the subject is extracted from the
// token response, not synthesized. Source of truth for the
// ConditionTypeIdentitySynthesized advisory.
func (c *EmbeddedAuthServerConfig) SyntheticIdentityUpstreams() []string {
	if c == nil {
		return nil
	}
	var names []string
	for i := range c.UpstreamProviders {
		p := &c.UpstreamProviders[i]
		if p.Type != UpstreamProviderTypeOAuth2 || p.OAuth2Config == nil {
			continue
		}
		if p.OAuth2Config.UserInfo == nil && p.OAuth2Config.IdentityFromToken == nil {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}

// ValidateAdditionalAuthorizationParams checks that no reserved OAuth2 parameters
// are present in the additional authorization params map.
func ValidateAdditionalAuthorizationParams(prefix string, params map[string]string) error {
	if err := oauthparams.Validate(params); err != nil {
		return fmt.Errorf("%s.additionalAuthorizationParams: %w", prefix, err)
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
