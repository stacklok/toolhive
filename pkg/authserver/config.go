// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authserver provides configuration and validation for the OAuth authorization server.
package authserver

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/networking"
)

// CurrentSchemaVersion is the current version of the authserver RunConfig schema.
const CurrentSchemaVersion = "v0.1.0"

// RunConfig is the serializable configuration for the embedded auth server.
// It contains no secrets - only file paths and environment variable names
// that will be resolved at runtime.
//
// This follows the same pattern as pkg/runner.RunConfig - it's serializable,
// versioned, and portable. Secrets are referenced by file path or environment
// variable name, never embedded directly.
type RunConfig struct {
	// SchemaVersion is the version of the RunConfig schema.
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`

	// Issuer is the issuer identifier for this authorization server.
	// This will be included in the "iss" claim of issued tokens.
	// Must be a valid HTTPS URL (or HTTP for localhost) without query, fragment, or trailing slash.
	Issuer string `json:"issuer" yaml:"issuer"`

	// AuthorizationEndpointBaseURL overrides the base URL used for the authorization_endpoint
	// in the OAuth discovery document. When set, the discovery document will advertise
	// `{authorization_endpoint_base_url}/oauth/authorize` instead of `{issuer}/oauth/authorize`.
	// All other endpoints remain derived from the issuer.
	//nolint:lll // field tags require full JSON+YAML names
	AuthorizationEndpointBaseURL string `json:"authorization_endpoint_base_url,omitempty" yaml:"authorization_endpoint_base_url,omitempty"`

	// SigningKeyConfig configures the signing key provider for JWT operations.
	// If nil or empty, an ephemeral signing key will be auto-generated (development only).
	SigningKeyConfig *SigningKeyRunConfig `json:"signing_key_config,omitempty" yaml:"signing_key_config,omitempty"`

	// HMACSecretFiles contains file paths to HMAC secrets for signing authorization codes
	// and refresh tokens (opaque tokens).
	// First file is the current secret (must be at least 32 bytes), subsequent files
	// are for rotation/verification of existing tokens.
	// If empty, an ephemeral secret will be auto-generated (development only).
	HMACSecretFiles []string `json:"hmac_secret_files,omitempty" yaml:"hmac_secret_files,omitempty"`

	// TokenLifespans configures the duration that various tokens are valid.
	// If nil, defaults are applied (access: 1h, refresh: 7d, authCode: 10m).
	TokenLifespans *TokenLifespanRunConfig `json:"token_lifespans,omitempty" yaml:"token_lifespans,omitempty"`

	// Upstreams configures connections to upstream Identity Providers.
	// At least one upstream is required - the server delegates authentication to these providers.
	// Multiple upstreams are supported for sequential authorization chains.
	Upstreams []UpstreamRunConfig `json:"upstreams" yaml:"upstreams"`

	// ScopesSupported lists the OAuth 2.0 scope values advertised in discovery documents.
	// If empty, defaults to registration.DefaultScopes (["openid", "profile", "email", "offline_access"]).
	ScopesSupported []string `json:"scopes_supported,omitempty" yaml:"scopes_supported,omitempty"`

	// AllowedAudiences is the list of valid resource URIs that tokens can be issued for.
	// Per RFC 8707, the "resource" parameter in authorization and token requests is
	// validated against this list. Required for MCP compliance.
	AllowedAudiences []string `json:"allowed_audiences" yaml:"allowed_audiences"`

	// Storage configures the storage backend for the auth server.
	// If nil, defaults to in-memory storage.
	Storage *storage.RunConfig `json:"storage,omitempty" yaml:"storage,omitempty"`
}

// SigningKeyRunConfig configures where to load signing keys from.
// Keys are loaded from PEM-encoded files on disk (typically mounted from secrets).
type SigningKeyRunConfig struct {
	// KeyDir is the directory containing PEM-encoded private key files.
	// All key filenames are relative to this directory.
	// In Kubernetes, this is typically a mounted Secret volume.
	KeyDir string `json:"key_dir,omitempty" yaml:"key_dir,omitempty"`

	// SigningKeyFile is the filename of the primary signing key (relative to KeyDir).
	// This key is used for signing new tokens.
	SigningKeyFile string `json:"signing_key_file,omitempty" yaml:"signing_key_file,omitempty"`

	// FallbackKeyFiles are filenames of additional keys for verification (relative to KeyDir).
	// These keys are included in the JWKS endpoint for token verification but are NOT
	// used for signing new tokens. Useful for key rotation.
	FallbackKeyFiles []string `json:"fallback_key_files,omitempty" yaml:"fallback_key_files,omitempty"`
}

// TokenLifespanRunConfig holds token lifetime configuration.
// All durations are specified as Go duration strings (e.g., "1h", "30m", "168h").
type TokenLifespanRunConfig struct {
	// AccessTokenLifespan is the duration that access tokens are valid.
	// If empty, defaults to 1 hour.
	AccessTokenLifespan string `json:"access_token_lifespan,omitempty" yaml:"access_token_lifespan,omitempty"`

	// RefreshTokenLifespan is the duration that refresh tokens are valid.
	// If empty, defaults to 7 days (168h).
	RefreshTokenLifespan string `json:"refresh_token_lifespan,omitempty" yaml:"refresh_token_lifespan,omitempty"`

	// AuthCodeLifespan is the duration that authorization codes are valid.
	// If empty, defaults to 10 minutes.
	AuthCodeLifespan string `json:"auth_code_lifespan,omitempty" yaml:"auth_code_lifespan,omitempty"`
}

// UpstreamProviderType identifies the type of upstream Identity Provider.
type UpstreamProviderType string

const (
	// UpstreamProviderTypeOIDC is for OIDC providers with discovery support.
	UpstreamProviderTypeOIDC UpstreamProviderType = "oidc"

	// UpstreamProviderTypeOAuth2 is for pure OAuth 2.0 providers with explicit endpoints.
	UpstreamProviderTypeOAuth2 UpstreamProviderType = "oauth2"
)

// DefaultUpstreamName is the name assigned to a single unnamed upstream.
const DefaultUpstreamName = "default"

// ResolveUpstreamName returns the canonical name for an upstream.
// An empty name is resolved to DefaultUpstreamName ("default").
func ResolveUpstreamName(name string) string {
	if name == "" {
		return DefaultUpstreamName
	}
	return name
}

// upstreamNameRegex validates upstream provider names.
// Names must be DNS-label-like to prevent delimiter injection in storage keys.
var upstreamNameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// UpstreamRunConfig configures an upstream identity provider.
type UpstreamRunConfig struct {
	// Name uniquely identifies this upstream.
	// Used for routing decisions and session binding in multi-upstream scenarios.
	// If empty when only one upstream is configured, defaults to "default".
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Type specifies the provider type: "oidc" or "oauth2".
	Type UpstreamProviderType `json:"type" yaml:"type"`

	// OIDCConfig contains OIDC-specific configuration.
	// Required when Type is "oidc", must be nil when Type is "oauth2".
	OIDCConfig *OIDCUpstreamRunConfig `json:"oidc_config,omitempty" yaml:"oidc_config,omitempty"`

	// OAuth2Config contains OAuth 2.0-specific configuration.
	// Required when Type is "oauth2", must be nil when Type is "oidc".
	OAuth2Config *OAuth2UpstreamRunConfig `json:"oauth2_config,omitempty" yaml:"oauth2_config,omitempty"`
}

// OIDCUpstreamRunConfig contains OIDC provider configuration.
// OIDC providers support automatic endpoint discovery via the issuer URL.
type OIDCUpstreamRunConfig struct {
	// IssuerURL is the OIDC issuer URL for automatic endpoint discovery.
	// Must be a valid HTTPS URL.
	IssuerURL string `json:"issuer_url" yaml:"issuer_url"`

	// ClientID is the OAuth 2.0 client identifier registered with the upstream IDP.
	ClientID string `json:"client_id" yaml:"client_id"`

	// ClientSecretFile is the path to a file containing the OAuth 2.0 client secret.
	// Mutually exclusive with ClientSecretEnvVar. Optional for public clients using PKCE.
	ClientSecretFile string `json:"client_secret_file,omitempty" yaml:"client_secret_file,omitempty"`

	// ClientSecretEnvVar is the name of an environment variable containing the client secret.
	// Mutually exclusive with ClientSecretFile. Optional for public clients using PKCE.
	ClientSecretEnvVar string `json:"client_secret_env_var,omitempty" yaml:"client_secret_env_var,omitempty"`

	// RedirectURI is the callback URL where the upstream IDP will redirect after authentication.
	// When not specified, defaults to `{issuer}/oauth/callback`.
	RedirectURI string `json:"redirect_uri,omitempty" yaml:"redirect_uri,omitempty"`

	// Scopes are the OAuth scopes to request from the upstream IDP.
	// If not specified, defaults to ["openid", "offline_access"].
	// When using AdditionalAuthorizationParams with provider-specific refresh
	// token mechanisms (e.g., Google's access_type=offline), set explicit scopes
	// to avoid sending both offline_access and the provider-specific parameter.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// UserInfoOverride allows customizing UserInfo fetching behavior for OIDC providers.
	// By default, the UserInfo endpoint is discovered automatically via OIDC discovery.
	UserInfoOverride *UserInfoRunConfig `json:"userinfo_override,omitempty" yaml:"userinfo_override,omitempty"`

	// AdditionalAuthorizationParams are extra query parameters to include in
	// authorization requests. Useful for provider-specific parameters like
	// Google's access_type=offline.
	//nolint:lll // field tags require full JSON+YAML names
	AdditionalAuthorizationParams map[string]string `json:"additional_authorization_params,omitempty" yaml:"additional_authorization_params,omitempty"`
}

// OAuth2UpstreamRunConfig contains configuration for pure OAuth 2.0 providers.
// OAuth 2.0 providers require explicit endpoint configuration.
type OAuth2UpstreamRunConfig struct {
	// AuthorizationEndpoint is the URL for the OAuth authorization endpoint.
	AuthorizationEndpoint string `json:"authorization_endpoint" yaml:"authorization_endpoint"`

	// TokenEndpoint is the URL for the OAuth token endpoint.
	TokenEndpoint string `json:"token_endpoint" yaml:"token_endpoint"`

	// ClientID is the OAuth 2.0 client identifier registered with the upstream IDP.
	// Mutually exclusive with DCRConfig: when DCRConfig is set, ClientID is obtained
	// at runtime via RFC 7591 Dynamic Client Registration and must be left empty.
	ClientID string `json:"client_id" yaml:"client_id"`

	// ClientSecretFile is the path to a file containing the OAuth 2.0 client secret.
	// Mutually exclusive with ClientSecretEnvVar. Optional for public clients using PKCE.
	ClientSecretFile string `json:"client_secret_file,omitempty" yaml:"client_secret_file,omitempty"`

	// ClientSecretEnvVar is the name of an environment variable containing the client secret.
	// Mutually exclusive with ClientSecretFile. Optional for public clients using PKCE.
	ClientSecretEnvVar string `json:"client_secret_env_var,omitempty" yaml:"client_secret_env_var,omitempty"`

	// RedirectURI is the callback URL where the upstream IDP will redirect after authentication.
	// When not specified, defaults to `{issuer}/oauth/callback`.
	RedirectURI string `json:"redirect_uri,omitempty" yaml:"redirect_uri,omitempty"`

	// Scopes are the OAuth scopes to request from the upstream IDP.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// UserInfo contains configuration for fetching user information (required for OAuth2).
	UserInfo *UserInfoRunConfig `json:"userinfo" yaml:"userinfo"`

	// TokenResponseMapping configures custom field extraction from non-standard token responses.
	// When set, the token exchange bypasses golang.org/x/oauth2 and extracts fields using
	// the configured dot-notation paths.
	//nolint:lll // field tags require full JSON+YAML names
	TokenResponseMapping *TokenResponseMappingRunConfig `json:"token_response_mapping,omitempty" yaml:"token_response_mapping,omitempty"`

	// AdditionalAuthorizationParams are extra query parameters to include in
	// authorization requests. Useful for provider-specific parameters like
	// Google's access_type=offline.
	//nolint:lll // field tags require full JSON+YAML names
	AdditionalAuthorizationParams map[string]string `json:"additional_authorization_params,omitempty" yaml:"additional_authorization_params,omitempty"`

	// DCRConfig enables RFC 7591 Dynamic Client Registration against the
	// upstream authorization server. When set, the client credentials are
	// obtained at runtime rather than being pre-provisioned via ClientID /
	// ClientSecretFile / ClientSecretEnvVar, and ClientID must be left empty.
	// Mutually exclusive with ClientID.
	DCRConfig *DCRUpstreamConfig `json:"dcr_config,omitempty" yaml:"dcr_config,omitempty"`
}

// DCRUpstreamConfig configures RFC 7591 Dynamic Client Registration for an
// upstream authorization server. When present on an OAuth2 upstream, the
// authserver performs registration at runtime to obtain client credentials,
// replacing the need to pre-provision a ClientID.
//
// Exactly one of DiscoveryURL or RegistrationEndpoint must be set. DiscoveryURL
// points at RFC 8414 / OIDC Discovery metadata from which the registration
// endpoint is resolved; RegistrationEndpoint is used directly when the upstream
// does not publish discovery metadata.
type DCRUpstreamConfig struct {
	// DiscoveryURL is the RFC 8414 / OIDC Discovery URL from which the
	// registration_endpoint is resolved at runtime. Mutually exclusive with
	// RegistrationEndpoint.
	DiscoveryURL string `json:"discovery_url,omitempty" yaml:"discovery_url,omitempty"`

	// RegistrationEndpoint is the RFC 7591 registration endpoint URL used
	// directly, bypassing discovery. Mutually exclusive with DiscoveryURL.
	RegistrationEndpoint string `json:"registration_endpoint,omitempty" yaml:"registration_endpoint,omitempty"`

	// InitialAccessTokenFile is the path to a file containing the RFC 7591
	// initial access token presented to the registration endpoint. Mutually
	// exclusive with InitialAccessTokenEnvVar. Both may be omitted for open
	// registration endpoints.
	//nolint:lll // field tags require full JSON+YAML names
	InitialAccessTokenFile string `json:"initial_access_token_file,omitempty" yaml:"initial_access_token_file,omitempty"`

	// InitialAccessTokenEnvVar is the name of an environment variable
	// containing the RFC 7591 initial access token. Mutually exclusive with
	// InitialAccessTokenFile.
	//nolint:lll // field tags require full JSON+YAML names
	InitialAccessTokenEnvVar string `json:"initial_access_token_env_var,omitempty" yaml:"initial_access_token_env_var,omitempty"`

	// SoftwareID is the RFC 7591 "software_id" registration metadata value,
	// identifying the client software independent of any particular
	// registration instance.
	SoftwareID string `json:"software_id,omitempty" yaml:"software_id,omitempty"`

	// SoftwareStatement is the RFC 7591 "software_statement" JWT asserting
	// metadata about the client software, signed by a party the authorization
	// server trusts.
	SoftwareStatement string `json:"software_statement,omitempty" yaml:"software_statement,omitempty"`
}

// TokenResponseMappingRunConfig maps non-standard token response fields to standard fields.
// Paths support dot-notation for nested JSON fields (e.g., "authed_user.access_token").
type TokenResponseMappingRunConfig struct {
	// AccessTokenPath is the dot-notation path to the access token (required).
	AccessTokenPath string `json:"access_token_path" yaml:"access_token_path"`

	// ScopePath is the dot-notation path to the scope. Defaults to "scope".
	ScopePath string `json:"scope_path,omitempty" yaml:"scope_path,omitempty"`

	// RefreshTokenPath is the dot-notation path to the refresh token. Defaults to "refresh_token".
	RefreshTokenPath string `json:"refresh_token_path,omitempty" yaml:"refresh_token_path,omitempty"`

	// ExpiresInPath is the dot-notation path to the expires_in value. Defaults to "expires_in".
	ExpiresInPath string `json:"expires_in_path,omitempty" yaml:"expires_in_path,omitempty"`
}

// UserInfoRunConfig contains UserInfo endpoint configuration.
// This supports both standard OIDC UserInfo endpoints and custom provider-specific endpoints.
type UserInfoRunConfig struct {
	// EndpointURL is the URL of the userinfo endpoint.
	EndpointURL string `json:"endpoint_url" yaml:"endpoint_url"`

	// HTTPMethod is the HTTP method to use for the userinfo request.
	// If not specified, defaults to GET.
	HTTPMethod string `json:"http_method,omitempty" yaml:"http_method,omitempty"`

	// AdditionalHeaders contains extra headers to include in the userinfo request.
	// Useful for providers that require specific headers (e.g., GitHub's Accept header).
	AdditionalHeaders map[string]string `json:"additional_headers,omitempty" yaml:"additional_headers,omitempty"`

	// FieldMapping contains custom field mapping configuration for non-standard providers.
	// If nil, standard OIDC field names are used ("sub", "name", "email").
	FieldMapping *UserInfoFieldMappingRunConfig `json:"field_mapping,omitempty" yaml:"field_mapping,omitempty"`
}

// UserInfoFieldMappingRunConfig maps provider-specific field names to standard UserInfo fields.
// This allows adapting non-standard provider responses to the canonical UserInfo structure.
type UserInfoFieldMappingRunConfig struct {
	// SubjectFields is an ordered list of field names to try for the user ID.
	// The first non-empty value found will be used.
	// Default: ["sub"]
	SubjectFields []string `json:"subject_fields,omitempty" yaml:"subject_fields,omitempty"`

	// NameFields is an ordered list of field names to try for the display name.
	// The first non-empty value found will be used.
	// Default: ["name"]
	NameFields []string `json:"name_fields,omitempty" yaml:"name_fields,omitempty"`

	// EmailFields is an ordered list of field names to try for the email address.
	// The first non-empty value found will be used.
	// Default: ["email"]
	EmailFields []string `json:"email_fields,omitempty" yaml:"email_fields,omitempty"`
}

// UpstreamConfig wraps an upstream IDP configuration with identifying metadata.
// It supports both OIDC providers (with discovery) and pure OAuth 2.0 providers.
type UpstreamConfig struct {
	// Name uniquely identifies this upstream.
	// Used for routing decisions and session binding in multi-upstream scenarios.
	// If empty when only one upstream is configured, defaults to "default".
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Type specifies the provider type: "oidc" or "oauth2".
	Type UpstreamProviderType `json:"type" yaml:"type"`

	// OAuth2Config contains OAuth 2.0 provider configuration.
	// Used when Type is "oauth2". Must be nil when Type is "oidc".
	OAuth2Config *upstream.OAuth2Config `json:"oauth2_config,omitempty" yaml:"oauth2_config,omitempty"`

	// OIDCConfig contains OIDC provider configuration (uses discovery).
	// Used when Type is "oidc". Must be nil when Type is "oauth2".
	OIDCConfig *upstream.OIDCConfig `json:"oidc_config,omitempty" yaml:"oidc_config,omitempty"`
}

// Config is the pure configuration for the OAuth authorization server.
// All values must be fully resolved (no file paths, no env vars).
// This is the interface that consumers should use to configure the server.
type Config struct {
	// Issuer is the issuer identifier for this authorization server.
	// This will be included in the "iss" claim of issued tokens.
	Issuer string

	// AuthorizationEndpointBaseURL overrides the base URL used for the authorization_endpoint
	// in the OAuth discovery document. When empty, defaults to Issuer.
	AuthorizationEndpointBaseURL string

	// KeyProvider provides signing keys for JWT operations.
	// Supports key rotation by returning multiple public keys for JWKS.
	// If nil, an ephemeral key will be auto-generated (development only).
	//
	// Production: Use keys.NewFileProvider() or keys.NewProviderFromConfig()
	// Testing: Use a mock or keys.NewGeneratingProvider()
	KeyProvider keys.KeyProvider

	// HMACSecrets contains the symmetric secrets used for signing authorization codes
	// and refresh tokens (opaque tokens). Unlike the asymmetric SigningKey which
	// signs JWTs for distributed verification, these secrets are used internally
	// by the authorization server only.
	// Current secret must be at least 32 bytes and cryptographically random.
	// Must be consistent across all replicas in multi-instance deployments.
	// Supports secret rotation via the Rotated field.
	HMACSecrets *servercrypto.HMACSecrets

	// AccessTokenLifespan is the duration that access tokens are valid.
	// If zero, defaults to 1 hour.
	AccessTokenLifespan time.Duration

	// RefreshTokenLifespan is the duration that refresh tokens are valid.
	// If zero, defaults to 7 days.
	RefreshTokenLifespan time.Duration

	// AuthCodeLifespan is the duration that authorization codes are valid.
	// If zero, defaults to 10 minutes.
	AuthCodeLifespan time.Duration

	// Upstreams contains configurations for connecting to upstream IDPs.
	// At least one upstream is required - the server delegates authentication to the upstream IDP.
	// Multiple upstreams form a sequential authorization chain.
	Upstreams []UpstreamConfig

	// ScopesSupported lists the OAuth 2.0 scope values advertised in discovery documents.
	// If nil or empty, defaults to registration.DefaultScopes (["openid", "profile", "email", "offline_access"]).
	// This is advertised in /.well-known/openid-configuration and
	// /.well-known/oauth-authorization-server discovery endpoints.
	ScopesSupported []string

	// AllowedAudiences is the list of valid resource URIs that tokens can be issued for.
	// Per RFC 8707, the "resource" parameter in authorization and token requests is
	// validated against this list. MCP clients are required to include the resource
	// parameter, so this should be configured with the canonical URIs of all MCP servers
	// this authorization server issues tokens for.
	//
	// Security: An empty list means NO audiences are permitted (secure default).
	// When empty, any request with a "resource" parameter will be rejected with
	// "invalid_target". Configure this for proper MCP specification compliance.
	AllowedAudiences []string
}

// Validate checks that the Config is valid.
func (c *Config) Validate() error {
	slog.Debug("validating authserver config", "issuer", c.Issuer)

	if err := validateIssuerURL(c.Issuer); err != nil {
		return fmt.Errorf("issuer: %w", err)
	}

	if c.AuthorizationEndpointBaseURL != "" {
		if err := validateIssuerURL(c.AuthorizationEndpointBaseURL); err != nil {
			return fmt.Errorf("authorization_endpoint_base_url: %w", err)
		}
	}

	// KeyProvider is optional - if nil, applyDefaults() will create a GeneratingProvider

	if c.HMACSecrets == nil {
		return fmt.Errorf("HMAC secrets are required")
	}
	if len(c.HMACSecrets.Current) < servercrypto.MinSecretLength {
		return fmt.Errorf("HMAC secret must be at least %d bytes", servercrypto.MinSecretLength)
	}

	if err := c.validateUpstreams(); err != nil {
		return err
	}

	// AllowedAudiences is required for MCP compliance.
	// Per MCP specification, clients MUST include the "resource" parameter (RFC 8707),
	// which requires the server to have configured allowed audiences to validate against.
	if len(c.AllowedAudiences) == 0 {
		return fmt.Errorf("at least one allowed audience is required for MCP compliance (RFC 8707 resource parameter validation)")
	}

	slog.Debug("authserver config validation passed",
		"issuer", c.Issuer,
		"upstream_count", len(c.Upstreams),
	)
	return nil
}

// validateUpstreams validates the upstream configurations.
func (c *Config) validateUpstreams() error {
	if len(c.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream is required")
	}
	// Track names for uniqueness checking
	seenNames := make(map[string]bool)

	for i := range c.Upstreams {
		up := &c.Upstreams[i]

		if err := c.validateUpstreamName(i, up); err != nil {
			return err
		}

		// Check for duplicate names
		if seenNames[up.Name] {
			return fmt.Errorf("duplicate upstream name: %q", up.Name)
		}
		seenNames[up.Name] = true

		if err := validateUpstreamType(up); err != nil {
			return err
		}
	}

	return nil
}

// validateUpstreamName validates and defaults the upstream name.
// For single upstream, empty names default to "default".
// For multi-upstream, explicit non-"default" names are required.
func (c *Config) validateUpstreamName(i int, up *UpstreamConfig) error {
	if len(c.Upstreams) == 1 {
		if up.Name == "" {
			up.Name = DefaultUpstreamName
		}
	} else {
		if up.Name == "" {
			return fmt.Errorf(
				"upstream[%d]: name must be explicitly set when multiple upstreams are configured", i)
		}
		if up.Name == DefaultUpstreamName {
			return fmt.Errorf(
				"upstream[%d]: name %q is reserved for single-upstream configs; use a descriptive name",
				i, up.Name)
		}
	}

	// Validate name format (DNS-label-like) to prevent storage key injection
	if !upstreamNameRegex.MatchString(up.Name) {
		return fmt.Errorf(
			"upstream[%d]: name %q must match %s (lowercase alphanumeric and hyphens)",
			i, up.Name, upstreamNameRegex.String())
	}

	return nil
}

// validateUpstreamType validates the provider type and its type-specific config.
func validateUpstreamType(up *UpstreamConfig) error {
	switch up.Type {
	case UpstreamProviderTypeOIDC:
		if up.OIDCConfig == nil {
			return fmt.Errorf("upstream %q: oidc_config is required for OIDC provider", up.Name)
		}
		if up.OAuth2Config != nil {
			return fmt.Errorf("upstream %q: oauth2_config must not be set when type is %q", up.Name, up.Type)
		}
		if err := up.OIDCConfig.Validate(); err != nil {
			return fmt.Errorf("upstream %q: %w", up.Name, err)
		}
	case UpstreamProviderTypeOAuth2:
		if up.OAuth2Config == nil {
			return fmt.Errorf("upstream %q: oauth2_config is required for OAuth2 provider", up.Name)
		}
		if up.OIDCConfig != nil {
			return fmt.Errorf("upstream %q: oidc_config must not be set when type is %q", up.Name, up.Type)
		}
		if err := up.OAuth2Config.Validate(); err != nil {
			return fmt.Errorf("upstream %q: %w", up.Name, err)
		}
	default:
		return fmt.Errorf("upstream %q: unsupported provider type: %q", up.Name, up.Type)
	}
	return nil
}

// applyDefaults applies default values to the config where not set.
func (c *Config) applyDefaults() error {
	slog.Debug("applying default values to authserver config")

	if c.AccessTokenLifespan == 0 {
		c.AccessTokenLifespan = time.Hour
		slog.Debug("applied default access token lifespan", "duration", c.AccessTokenLifespan)
	}
	if c.RefreshTokenLifespan == 0 {
		c.RefreshTokenLifespan = 24 * time.Hour * 7 // 7 days
		slog.Debug("applied default refresh token lifespan", "duration", c.RefreshTokenLifespan)
	}
	if c.AuthCodeLifespan == 0 {
		c.AuthCodeLifespan = 10 * time.Minute
		slog.Debug("applied default auth code lifespan", "duration", c.AuthCodeLifespan)
	}
	if c.HMACSecrets == nil {
		secret := make([]byte, servercrypto.MinSecretLength)
		if _, err := rand.Read(secret); err != nil {
			return fmt.Errorf("failed to generate HMAC secret: %w", err)
		}
		c.HMACSecrets = &servercrypto.HMACSecrets{Current: secret}
		slog.Warn("no HMAC secrets configured, generating ephemeral secret",
			"warning", "auth codes and refresh tokens will be invalid after restart")
	}
	if c.KeyProvider == nil {
		c.KeyProvider = keys.NewGeneratingProvider(keys.DefaultAlgorithm)
		slog.Warn("no key provider configured, using ephemeral signing key",
			"warning", "JWTs will be invalid after restart")
	}
	if len(c.ScopesSupported) == 0 {
		c.ScopesSupported = registration.DefaultScopes
		slog.Debug("applied default scopes_supported", "scopes", c.ScopesSupported)
	}
	return nil
}

// validateIssuerURL validates that the issuer is a valid URL.
// Per OIDC Core Section 3.1.2.1 and RFC 8414 Section 2, the issuer
// MUST use the "https" scheme, except for localhost during development.
func validateIssuerURL(issuer string) error {
	if issuer == "" {
		return fmt.Errorf("issuer is required")
	}

	parsed, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsed.Scheme == "" {
		return fmt.Errorf("scheme is required")
	}

	if parsed.Host == "" {
		return fmt.Errorf("host is required")
	}

	// Per RFC 8414 Section 2, the issuer identifier has no query or fragment components
	if parsed.RawQuery != "" {
		return fmt.Errorf("must not contain query component")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("must not contain fragment component")
	}

	// HTTPS is required unless it's a loopback address (for development)
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" {
			return fmt.Errorf("scheme must be https (or http for localhost)")
		}
		if !networking.IsLocalhost(parsed.Host) {
			return fmt.Errorf("http scheme is only allowed for localhost, use https for %s", parsed.Hostname())
		}
	}

	// Issuer must not have trailing slash per OIDC spec
	if strings.HasSuffix(issuer, "/") {
		return fmt.Errorf("must not have trailing slash")
	}

	return nil
}

// Validate checks that the OAuth2UpstreamRunConfig is internally consistent.
// It enforces the mutual exclusivity of ClientID and DCRConfig: exactly one must
// be set. A ClientID is required for pre-provisioned clients; a DCRConfig is
// required when client credentials are obtained at runtime via RFC 7591
// Dynamic Client Registration. When DCRConfig is present, its own validity is
// also checked via DCRUpstreamConfig.Validate.
//
// Validate intentionally does not verify fields handled by the shared
// CommonOAuthConfig or upstream.OAuth2Config validators — it only covers the
// run-config surface area unique to OAuth2UpstreamRunConfig.
func (c *OAuth2UpstreamRunConfig) Validate() error {
	hasClientID := c.ClientID != ""
	hasDCR := c.DCRConfig != nil
	switch {
	case !hasClientID && !hasDCR:
		return fmt.Errorf("oauth2 upstream: either client_id or dcr_config is required")
	case hasClientID && hasDCR:
		return fmt.Errorf("oauth2 upstream: client_id and dcr_config are mutually exclusive")
	}

	if hasDCR {
		if err := c.DCRConfig.Validate(); err != nil {
			return fmt.Errorf("oauth2 upstream: invalid dcr_config: %w", err)
		}
	}

	return nil
}

// Validate checks that the DCRUpstreamConfig specifies exactly one of
// DiscoveryURL or RegistrationEndpoint.
//
// DiscoveryURL triggers runtime resolution of the registration endpoint via
// RFC 8414 / OIDC Discovery; RegistrationEndpoint bypasses discovery for
// providers that do not publish metadata. Requiring exactly one prevents
// ambiguity about which URL the authserver should contact for registration.
func (c *DCRUpstreamConfig) Validate() error {
	hasDiscovery := c.DiscoveryURL != ""
	hasRegistration := c.RegistrationEndpoint != ""
	switch {
	case !hasDiscovery && !hasRegistration:
		return fmt.Errorf("dcr_config: either discovery_url or registration_endpoint is required")
	case hasDiscovery && hasRegistration:
		return fmt.Errorf("dcr_config: discovery_url and registration_endpoint are mutually exclusive")
	}
	return nil
}
