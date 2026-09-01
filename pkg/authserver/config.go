// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authserver provides configuration and validation for the OAuth authorization server.
package authserver

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
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

	// DelegationTokenLifespan is the maximum lifetime for delegated tokens issued
	// via RFC 8693 token exchange. Specified as a Go duration string (e.g., "15m").
	// If empty, defaults to 15 minutes.
	DelegationTokenLifespan string `json:"delegation_token_lifespan,omitempty" yaml:"delegation_token_lifespan,omitempty"`

	// Upstreams configures connections to upstream Identity Providers for
	// interactive authorization. It may be empty only when DelegateClients or a
	// TrustedIssuer with JWTBearerGrant enables token-only operation.
	// Multiple upstreams are supported for sequential authorization chains.
	Upstreams []UpstreamRunConfig `json:"upstreams" yaml:"upstreams"`

	// ScopesSupported lists the OAuth 2.0 scope values advertised in discovery documents.
	// If empty, defaults to registration.DefaultScopes (["openid", "profile", "email", "offline_access"]).
	ScopesSupported []string `json:"scopes_supported,omitempty" yaml:"scopes_supported,omitempty"`

	// BaselineClientScopes is a baseline set of OAuth 2.0 scopes unioned into every
	// DCR registration. All values must appear in ScopesSupported; the auth server
	// rejects this RunConfig at startup otherwise. Empty means current behavior is
	// preserved (registered scope = client-requested, or the intersection of
	// DefaultScopes with ScopesSupported if the client requested none).
	// When ScopesSupported is empty, the subset check uses registration.DefaultScopes
	// (the same set applyDefaults would substitute at startup) — so
	// BaselineClientScopes containing standard OIDC scopes works without enumerating
	// ScopesSupported explicitly.
	//nolint:lll // field tags require full JSON+YAML names
	BaselineClientScopes []string `json:"baseline_client_scopes,omitempty" yaml:"baseline_client_scopes,omitempty"`

	// AllowedAudiences is the list of valid resource URIs that tokens can be issued for.
	// Per RFC 8707, the "resource" parameter in authorization and token requests is
	// validated against this list. Required for MCP compliance.
	AllowedAudiences []string `json:"allowed_audiences" yaml:"allowed_audiences"`

	// Storage configures the storage backend for the auth server.
	// If nil, defaults to in-memory storage.
	Storage *storage.RunConfig `json:"storage,omitempty" yaml:"storage,omitempty"`

	// DisableUpstreamTokenInjection prevents the upstream swap middleware from being added.
	// When true, the embedded auth server handles OAuth flows for clients, but instead of
	// injecting upstream IdP tokens the proxy strips the client's credential headers
	// (Authorization, Cookie, Proxy-Authorization) after the JWT is validated — the
	// backend receives an unauthenticated request. Incompatible with token exchange
	// and AWS STS, which would re-add credentials after the strip.
	//nolint:lll // field tags require full JSON+YAML names
	DisableUpstreamTokenInjection bool `json:"disable_upstream_token_injection,omitempty" yaml:"disable_upstream_token_injection,omitempty"`

	// CIMD controls client_id metadata document support. When enabled, the
	// embedded authorization server accepts HTTPS URLs as client_id values
	// and resolves them via the CIMD protocol instead of requiring DCR.
	CIMD *CIMDRunConfig `json:"cimd,omitempty" yaml:"cimd,omitempty"`

	// InsecureAllowHTTP permits an http:// issuer URL for non-localhost hosts.
	// Only set this for in-cluster Kubernetes deployments on a trusted network.
	// Production deployments reachable outside the cluster MUST use https://.
	//nolint:lll // field tags require full JSON+YAML names
	InsecureAllowHTTP bool `json:"insecure_allow_http,omitempty" yaml:"insecure_allow_http,omitempty"`

	// TrustedIssuers lists external OIDC issuers whose tokens are accepted as
	// RFC 8693 subject tokens or RFC 7523 JWT-bearer assertions. Issuers with
	// jwtBearerGrant enabled may be used for the JWT-bearer grant without an
	// RFC 8693 delegation policy. Empty (the default) means only self-issued
	// subject tokens are accepted.
	//
	// See tokenexchange.TrustedIssuer for the per-issuer field reference, and
	// docs/arch/17-token-exchange-delegation.md for the trust model, consent
	// signals, and operator-facing constraints (audience/scope bounding,
	// subject namespace qualification, required client binding) that aren't
	// visible from the config shape alone.
	//nolint:lll // field tags require full JSON+YAML names
	TrustedIssuers []tokenexchange.TrustedIssuer `json:"trusted_issuers,omitempty" yaml:"trusted_issuers,omitempty"`

	// AllowConfidentialClientRegistration permits Dynamic Client Registration
	// of confidential clients: when true, /oauth/register accepts
	// token_endpoint_auth_method values client_secret_basic and
	// client_secret_post in addition to "none" (still the default on
	// omission) and mints a client_secret returned exactly once. Confidential
	// clients are restricted to https non-loopback redirect URIs, and
	// registrations idle for more than DefaultDCRClientTTL (30 days) are
	// evicted and must re-register. This gates registration only: disabling
	// it does not revoke or reject already-minted secrets at the token
	// endpoint.
	//
	// Security: /oauth/register is unauthenticated, so this issues client
	// secrets to any caller. Combining it with InsecureAllowHTTP is rejected
	// by Validate.
	//nolint:lll // field tags require full JSON+YAML names
	AllowConfidentialClientRegistration bool `json:"allow_confidential_client_registration,omitempty" yaml:"allow_confidential_client_registration,omitempty"`

	// AllowPrivateKeyJWTRegistration permits Dynamic Client Registration of
	// clients using private_key_jwt authentication. This is independent of
	// AllowConfidentialClientRegistration and defaults to false. Registration
	// behavior is controlled independently by the DCR handler and discovery
	// metadata.
	//
	// Security: /oauth/register is unauthenticated. Unlike
	// AllowConfidentialClientRegistration, this is NOT rejected when combined
	// with InsecureAllowHTTP: registration never returns a secret for a
	// private_key_jwt client, so there is nothing for cleartext HTTP to
	// expose.
	//nolint:lll // field tags require full JSON+YAML names
	AllowPrivateKeyJWTRegistration bool `json:"allow_private_key_jwt_registration,omitempty" yaml:"allow_private_key_jwt_registration,omitempty"`

	// ForceConfidentialRedirectURIs lists redirect URIs that must be registered
	// as confidential clients regardless of the token_endpoint_auth_method the
	// DCR request declares. A registration whose redirect_uris contains an
	// EXACT match for one of these entries is issued a real client_secret and
	// reported back as token_endpoint_auth_method "client_secret_post", even
	// if the request said "none" or omitted the field.
	//
	// This exists for MCP clients (Perplexity is the known case) that declare
	// themselves public (token_endpoint_auth_method: "none") per RFC 7591 but
	// then refuse to proceed because the response carries no client_secret —
	// a self-contradictory request no conformant server can satisfy as
	// written. RFC 7591 §3.2.1 permits the server to substitute metadata, so
	// this takes such a client at its word that it wants a secret.
	//
	// Exact matching is deliberate: it is not a way to obtain a usable
	// credential for another client. An attacker who registers with someone
	// else's callback URI is issued a secret for a client whose authorization
	// codes are delivered to that someone else's redirect endpoint, not to
	// the attacker — the secret is useless without also controlling the
	// callback.
	//
	// Requires AllowConfidentialClientRegistration; every entry must be a
	// valid https non-loopback URI (Validate rejects loopback entries — the
	// same restriction AllowConfidentialClientRegistration itself enforces
	// exists so secrets do not land in distributed native apps, and this
	// override must not bypass it). Remove an entry once the client is fixed
	// to handle "none" registrations correctly.
	//nolint:lll // field tags require full JSON+YAML names
	ForceConfidentialRedirectURIs []string `json:"force_confidential_redirect_uris,omitempty" yaml:"force_confidential_redirect_uris,omitempty"`

	// InsecureAllowConfidentialOverLoopbackHTTP opts in to confidential clients
	// when Issuer is a plain-HTTP loopback URL. Without this flag, that
	// combination is rejected: a loopback http:// issuer is normally fine for
	// local development (the traffic never leaves the machine), but client
	// secrets would otherwise travel over cleartext. Defaults to false. Has no
	// effect when there are no confidential clients or Issuer is https.
	//
	// Applies identically to delegate clients and DCR-registered clients. The
	// Kubernetes CRD requires the explicit opt-in for a delegate client with an
	// HTTP issuer; the shared transport validator enforces that its host is
	// loopback — see EmbeddedAuthServerConfig's doc comment.
	//
	// private_key_jwt registration has no equivalent flag or transport
	// restriction: unlike confidential registration, it never returns a
	// client_secret (or any other secret) in the DCR response, so there is
	// nothing here for cleartext HTTP to expose.
	//nolint:lll // field tags require full JSON+YAML names
	InsecureAllowConfidentialOverLoopbackHTTP bool `json:"insecure_allow_confidential_over_loopback_http,omitempty" yaml:"insecure_allow_confidential_over_loopback_http,omitempty"`

	// DelegateClients declares confidential OAuth clients to register at
	// authorization-server startup, including clients intended for RFC 8693
	// token exchange.
	//
	// Independent of AllowConfidentialClientRegistration: declaring a client
	// here does not require or enable self-service confidential DCR, and
	// setting that flag does not declare or enable any client here. They
	// govern different endpoints — this field is static configuration the
	// operator controls directly, while the flag is admission policy for the
	// unauthenticated /oauth/register endpoint.
	//
	// See DelegateClientRunConfig for the per-client field reference.
	DelegateClients []DelegateClientRunConfig `json:"delegate_clients,omitempty" yaml:"delegate_clients,omitempty"`
}

// DelegateClientRunConfig declares a pre-provisioned confidential OAuth
// client for authorization-server startup, so it can act as the client in an
// RFC 8693 token-exchange request. The secret is always a reference (file or
// environment variable), never an inline literal. The grant type is fixed to
// RFC 8693 token exchange internally and is not configurable.
type DelegateClientRunConfig struct {
	// ClientID is the OAuth client_id this client presents at the token endpoint.
	ClientID string `json:"client_id" yaml:"client_id"`

	// ClientSecretFile is the path to a file containing the client secret.
	// If both this and ClientSecretEnvVar are set, the file takes precedence.
	ClientSecretFile string `json:"client_secret_file,omitempty" yaml:"client_secret_file,omitempty"`

	// ClientSecretEnvVar is the name of an environment variable containing
	// the client secret. One of ClientSecretFile or ClientSecretEnvVar is
	// required.
	//nolint:lll // field tags require full JSON+YAML names
	ClientSecretEnvVar string `json:"client_secret_env_var,omitempty" yaml:"client_secret_env_var,omitempty"`

	// Scopes are the OAuth scopes this client may request. Required, and
	// must be a subset of RunConfig.ScopesSupported: a declared client must
	// not receive every supported scope just because this was left empty.
	Scopes []string `json:"scopes" yaml:"scopes"`

	// Audiences are the RFC 8707 resource values this client may request a
	// token for. Required, and must be a subset of RunConfig.AllowedAudiences:
	// a declared client must not receive every allowed audience just because
	// this was left empty.
	Audiences []string `json:"audiences" yaml:"audiences"`
}

// Validate checks that the on-disk RunConfig is internally consistent. Called
// by the runner before resolving secrets and building the runtime Config; it
// catches operator-supplied misconfiguration early so server startup fails
// loudly instead of degrading silently at runtime.
func (c *RunConfig) Validate() error {
	if c.CIMD != nil {
		if err := c.CIMD.Validate(); err != nil {
			return fmt.Errorf("cimd: %w", err)
		}
	}
	// Also checked by Config.Validate() (same reasoning as
	// validateBaselineClientScopes below): buildUpstreamConfigs performs live
	// RFC 7591 registration against upstream IdPs before authserver.New ever
	// reaches Config.Validate(), so a malformed delegate-client config must
	// fail here, before that side-effecting work runs — not on a crash loop
	// after it, which would also orphan an upstream registration on every
	// restart with the default in-memory DCR store.
	if err := validateAllowedAudiences(c.AllowedAudiences); err != nil {
		return err
	}
	if err := validateDelegateClients(c.DelegateClients, c.ScopesSupported, c.AllowedAudiences); err != nil {
		return err
	}
	if err := validateTrustedIssuers(c.TrustedIssuers, c.Issuer, c.AllowedAudiences); err != nil {
		return err
	}
	if err := ValidateConfidentialClientTransport(
		c.AllowConfidentialClientRegistration || len(c.DelegateClients) > 0, c.InsecureAllowHTTP,
		c.Issuer, c.InsecureAllowConfidentialOverLoopbackHTTP); err != nil {
		return err
	}
	if err := ValidateForceConfidentialRedirectURIs(
		c.ForceConfidentialRedirectURIs, c.AllowConfidentialClientRegistration); err != nil {
		return err
	}
	return c.validateBaselineClientScopes()
}

// validateBaselineClientScopes ensures every entry in BaselineClientScopes is
// also present in ScopesSupported. If a baseline scope is not advertised by
// ScopesSupported, the embedded DCR handler would later try to register a
// client with a scope the server does not support, which fosite rejects at
// /oauth/authorize with invalid_scope.
//
// When ScopesSupported is empty, the check uses registration.DefaultScopes as
// the superset (matching what applyDefaults would substitute at startup), so
// operators can omit ScopesSupported and still configure standard OIDC scopes
// as baseline without error.
func (c *RunConfig) validateBaselineClientScopes() error {
	effective := c.ScopesSupported
	if len(effective) == 0 {
		effective = registration.DefaultScopes
	}
	return registration.ValidateScopeSubset(c.BaselineClientScopes, effective, "baseline_client_scopes")
}

func validateAllowedAudiences(audiences []string) error {
	for _, audience := range audiences {
		if audience == "" {
			return fmt.Errorf("allowed_audiences must not contain an empty audience")
		}
		if err := oauthserver.ValidateAudienceURI(audience); err != nil {
			return fmt.Errorf("allowed_audiences contains invalid audience %q: %w", audience, err)
		}
	}
	return nil
}

// validateDelegateClients validates the structural and narrowing invariants for
// RunConfig.DelegateClients. Scopes and audiences are required and must be
// subsets of the server-supported values so a declared client never defaults
// to every supported scope or audience.
func validateDelegateClients(
	clients []DelegateClientRunConfig, scopesSupported, allowedAudiences []string,
) error {
	effectiveScopes := scopesSupported
	if len(effectiveScopes) == 0 {
		effectiveScopes = registration.DefaultScopes
	}

	seen := make(map[string]struct{}, len(clients))
	for _, client := range clients {
		if client.ClientID == "" {
			return fmt.Errorf("delegate_clients: client_id is required")
		}
		if _, ok := seen[client.ClientID]; ok {
			return fmt.Errorf("delegate_clients: duplicate client_id %q", client.ClientID)
		}
		seen[client.ClientID] = struct{}{}

		if client.ClientSecretFile == "" && client.ClientSecretEnvVar == "" {
			return fmt.Errorf(
				"delegate_clients: client_id %q: client_secret_file or client_secret_env_var is required", client.ClientID)
		}
		if len(client.Scopes) == 0 {
			return fmt.Errorf("delegate_clients: client_id %q: scopes is required", client.ClientID)
		}
		if err := registration.ValidateScopeSubset(
			client.Scopes, effectiveScopes, fmt.Sprintf("delegate_clients[%s].scopes", client.ClientID)); err != nil {
			return err
		}
		if len(client.Audiences) == 0 {
			return fmt.Errorf("delegate_clients: client_id %q: audiences is required", client.ClientID)
		}
		for _, audience := range client.Audiences {
			if !slices.Contains(allowedAudiences, audience) {
				return fmt.Errorf(
					"delegate_clients: client_id %q: audience %q is not in allowed_audiences", client.ClientID, audience)
			}
		}
	}
	return nil
}

// minDelegateClientSecretLength is the minimum accepted length for a resolved
// delegate-client secret. Delegate-client secrets come from the operator (a
// file or environment variable) rather than being minted by this server's own
// DCR path, so — unlike SHA256Hasher's DCR-issued-secret assumption (see that
// type's doc comment) — nothing else guarantees they carry meaningful
// entropy. 32 matches the byte length GenerateClientSecret uses for DCR
// secrets (32 bytes of crypto/rand, base64url-encoded), so this floor accepts
// anything at that scale without requiring a specific encoding.
const minDelegateClientSecretLength = 32

// validateResolvedDelegateClients validates resolved runtime delegate clients.
// The resolved secret must be nonempty and at least minDelegateClientSecretLength
// characters, because Config callers bypass secret resolution and
// RunConfig.Validate, and unlike DCR-minted secrets, a delegate-client secret's
// entropy is never otherwise checked.
func validateResolvedDelegateClients(
	clients []DelegateClient, scopesSupported, allowedAudiences []string,
) error {
	runClients := make([]DelegateClientRunConfig, len(clients))
	for i, client := range clients {
		if client.ClientSecret == "" {
			return fmt.Errorf("delegate_clients: client_id %q: resolved client secret is required", client.ClientID)
		}
		if len(client.ClientSecret) < minDelegateClientSecretLength {
			return fmt.Errorf(
				"delegate_clients: client_id %q: resolved client secret must be at least %d characters",
				client.ClientID, minDelegateClientSecretLength)
		}
		runClients[i] = DelegateClientRunConfig{
			ClientID:           client.ClientID,
			ClientSecretEnvVar: "resolved",
			Scopes:             client.Scopes,
			Audiences:          client.Audiences,
		}
	}
	return validateDelegateClients(runClients, scopesSupported, allowedAudiences)
}

// CIMDRunConfig controls client_id metadata document (CIMD) support.
type CIMDRunConfig struct {
	// Enabled activates CIMD client lookup when true.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// CacheMaxSize is the maximum number of CIMD documents held in the LRU cache.
	// Defaults to 256 when Enabled is true and this field is zero.
	CacheMaxSize int `json:"cache_max_size,omitempty" yaml:"cache_max_size,omitempty"`

	// CacheFallbackTTL is the fixed TTL applied to every cached CIMD document.
	// Cache-Control header parsing is not yet implemented; all entries use this value.
	// Format: Go duration string (e.g. "5m", "10m", "1h").
	// Defaults to 5 minutes when Enabled is true and this field is omitted.
	CacheFallbackTTL string `json:"cache_fallback_ttl,omitempty" yaml:"cache_fallback_ttl,omitempty" example:"5m"`
}

// Validate checks that the CIMDRunConfig fields are internally consistent.
func (c *CIMDRunConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.CacheMaxSize < 0 {
		return fmt.Errorf("cache_max_size must be non-negative when CIMD is enabled, got %d", c.CacheMaxSize)
	}
	if c.CacheFallbackTTL != "" {
		d, err := time.ParseDuration(c.CacheFallbackTTL)
		if err != nil {
			return fmt.Errorf("cache_fallback_ttl: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("cache_fallback_ttl must be positive when CIMD is enabled, got %s", c.CacheFallbackTTL)
		}
	}
	return nil
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

// ResolveFirstUpstreamName returns the resolved name of the first element of
// names, or DefaultUpstreamName when names is empty. It is the single
// implementation of the "first upstream or default" pattern used wherever a
// subject-provider name must be derived from a list of configured upstreams.
func ResolveFirstUpstreamName(names []string) string {
	if len(names) > 0 {
		return ResolveUpstreamName(names[0])
	}
	return DefaultUpstreamName
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

	// SubjectClaim names the validated ID-token claim to use as the upstream
	// subject. Defaults to "sub" when empty. Set for IdPs where "sub" isn't
	// stable per user (e.g. Entra/Azure AD's "oid"). See upstream.OIDCConfig.
	SubjectClaim string `json:"subject_claim,omitempty" yaml:"subject_claim,omitempty"`

	// CAFilePath is the path to a PEM CA bundle added to the system roots.
	CAFilePath string `json:"ca_file_path,omitempty" yaml:"ca_file_path,omitempty"`

	// AllowPrivateIPs permits the OIDC discovery and token HTTP clients to
	// connect to private IP ranges (RFC-1918, link-local). Use only when the
	// upstream is hosted inside the same cluster and has no public endpoint.
	// HTTP-scheme restrictions are unchanged — HTTPS is still required for
	// non-localhost hosts. Defaults to false.
	AllowPrivateIPs bool `json:"allow_private_ips,omitempty" yaml:"allow_private_ips,omitempty"`

	// InsecureAllowHTTP permits a plain-HTTP issuer URL and HTTP discovery
	// endpoints for this upstream. Only for in-cluster development environments
	// (e.g. Dex served over HTTP in a kind cluster) where TLS is not available.
	// Never set this in production.
	//nolint:lll // field tags require full JSON+YAML names
	InsecureAllowHTTP bool `json:"insecure_allow_http,omitempty" yaml:"insecure_allow_http,omitempty"`
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

	// UserInfo contains configuration for fetching user information.
	// Optional: when nil, the upstream OAuth2 provider derives a deterministic
	// subject by SHA-256-hashing the access token (with a "tk-" prefix) instead
	// of calling a userinfo endpoint. OIDC providers always derive Subject from
	// the ID token and are unaffected.
	UserInfo *UserInfoRunConfig `json:"userinfo,omitempty" yaml:"userinfo,omitempty"`

	// TokenResponseMapping configures custom field extraction from non-standard token responses.
	// When set, the token exchange bypasses golang.org/x/oauth2 and extracts fields using
	// the configured dot-notation paths.
	//nolint:lll // field tags require full JSON+YAML names
	TokenResponseMapping *TokenResponseMappingRunConfig `json:"token_response_mapping,omitempty" yaml:"token_response_mapping,omitempty"`

	// IdentityFromToken extracts user identity (subject, name, email) directly from the
	// OAuth2 token-endpoint response body using gjson dot-notation paths. When set, the
	// embedded auth server skips the userinfo HTTP call entirely. Mirrors the CRD type
	// (cmd/thv-operator/api/v1beta1.IdentityFromTokenConfig) — the authoritative
	// trust-model and uniqueness documentation lives there.
	//nolint:lll // field tags require full JSON+YAML names
	IdentityFromToken *IdentityFromTokenRunConfig `json:"identity_from_token,omitempty" yaml:"identity_from_token,omitempty"`

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

	// CAFilePath is the path to a PEM CA bundle added to the system roots.
	CAFilePath string `json:"ca_file_path,omitempty" yaml:"ca_file_path,omitempty"`

	// AllowPrivateIPs permits the upstream provider's HTTP client to connect to
	// private IP ranges (RFC-1918, link-local). When DCRConfig is set, this
	// also gates the DCR discovery and registration calls made on this
	// upstream's behalf (see pkg/authserver/runner/dcr_adapter.go), so a
	// single flag covers the whole upstream rather than needing a separate
	// DCR-specific setting. Use only when the upstream is hosted inside the
	// same cluster and has no public endpoint. HTTP-scheme restrictions are
	// unchanged — HTTPS is still required for non-localhost hosts. Defaults
	// to false.
	AllowPrivateIPs bool `json:"allow_private_ips,omitempty" yaml:"allow_private_ips,omitempty"`

	// InsecureAllowHTTP permits plain-HTTP authorization and token endpoint URLs
	// for this upstream. Only for in-cluster development environments (e.g. an
	// OAuth2 provider served over HTTP in a kind cluster) where TLS is not
	// available. Never set this in production.
	//nolint:lll // field tags require full JSON+YAML names
	InsecureAllowHTTP bool `json:"insecure_allow_http,omitempty" yaml:"insecure_allow_http,omitempty"`
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
//
// Trust assumption: DiscoveryURL and RegistrationEndpoint are operator-supplied
// URLs validated only for HTTPS-or-loopback. The DCR resolver will issue
// outbound HTTP requests — possibly carrying the RFC 7591 initial access token
// as a bearer header — to whatever address those URLs resolve to. There is
// currently no allowlist or RFC1918 / link-local / cloud-metadata-service
// guard, because the operator role is fully trusted today. If the trust
// boundary ever changes (e.g. a multi-tenant operator deployment, or a less-
// privileged role gains write access to this struct via a CRD or YAML
// surface), this field becomes a confused-deputy SSRF vector. Hardening is
// tracked in https://github.com/stacklok/toolhive/issues/5135.
type DCRUpstreamConfig struct {
	// DiscoveryURL is the exact RFC 8414 / OIDC Discovery document URL to
	// fetch at runtime. The resolver issues a single GET against this URL
	// (no well-known-path fallback) and reads registration_endpoint,
	// authorization_endpoint, token_endpoint,
	// token_endpoint_auth_methods_supported, and scopes_supported from the
	// response. Per RFC 8414 §3.3, the document's "issuer" field must
	// exactly match the upstream issuer configured on the parent
	// run-config.
	//
	// Use this field when the upstream publishes discovery metadata at a
	// path that differs from the issuer-derived well-known paths — for
	// example a multi-tenant IdP whose metadata lives at
	// https://idp.example.com/tenants/acme/.well-known/openid-configuration.
	//
	// Mutually exclusive with RegistrationEndpoint.
	DiscoveryURL string `json:"discovery_url,omitempty" yaml:"discovery_url,omitempty"`

	// RegistrationEndpoint is the RFC 7591 registration endpoint URL used
	// directly, bypassing discovery. Because no discovery is performed,
	// server-capability fields (token_endpoint_auth_methods_supported,
	// scopes_supported) are unavailable on this code path; the caller is
	// expected to also supply AuthorizationEndpoint, TokenEndpoint, and an
	// explicit Scopes list on the parent OAuth2UpstreamRunConfig. Auth
	// method falls back to the resolver's default (client_secret_basic).
	//
	// Mutually exclusive with DiscoveryURL.
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

// IdentityFromTokenRunConfig configures extracting user identity claims directly from
// the token-endpoint response body. Mirrors the CRD type
// (cmd/thv-operator/api/v1beta1.IdentityFromTokenConfig) — the authoritative
// trust-model and uniqueness documentation lives there.
type IdentityFromTokenRunConfig struct {
	// SubjectPath is the dot-notation path to the subject (user ID) field.
	// Required when IdentityFromToken is set.
	SubjectPath string `json:"subject_path" yaml:"subject_path"`

	// NamePath is the dot-notation path to the display name field.
	NamePath string `json:"name_path,omitempty" yaml:"name_path,omitempty"`

	// EmailPath is the dot-notation path to the email address field.
	EmailPath string `json:"email_path,omitempty" yaml:"email_path,omitempty"`
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

	// DelegationTokenLifespan is the maximum lifetime for delegated tokens issued
	// via RFC 8693 token exchange. The actual lifetime is the minimum of this value
	// and the subject token's remaining lifetime. If zero, defaults to 15 minutes.
	DelegationTokenLifespan time.Duration

	// Upstreams contains configurations for connecting to upstream IDPs.
	// At least one upstream is required - the server delegates authentication to the upstream IDP.
	// Multiple upstreams form a sequential authorization chain.
	Upstreams []UpstreamConfig

	// UpstreamFactory, when set, is used instead of DefaultUpstreamFactory to
	// construct each configured upstream's OAuth2Provider. Two reasons to set it:
	//
	//   - Connection reuse. DefaultUpstreamFactory builds a private HTTP client
	//     per upstream, so a process that reconstructs the server to change its
	//     upstream set creates a fresh connection pool each time. A factory that
	//     passes a client shared per issuer host via upstream.WithHTTPClient /
	//     upstream.WithOAuth2HTTPClient creates none.
	//   - Per-upstream failure isolation. Upstream construction inside New is
	//     otherwise all-or-nothing: one unreachable or mistyped issuer_url fails
	//     the whole call. A factory owning construction can apply a per-upstream
	//     deadline and skip or substitute a single failing provider while the
	//     rest serve.
	//
	// SECURITY: the returned provider validates the upstream's ID tokens and
	// resolves user identity. A factory that returns a permissive provider
	// bypasses upstream authentication for that leg of the chain. Delegate to
	// DefaultUpstreamFactory for upstreams you do not need to customize.
	UpstreamFactory UpstreamProviderFactory

	// UpstreamFilter, when set, narrows the upstream authorization chain after the
	// first leg resolves (see handlers.WithUpstreamFilter). When nil, all
	// configured upstreams are walked — the current behavior. Pass nil itself,
	// not a nil-valued concrete pointer implementing UpstreamFilter — a typed-nil
	// interface value is non-nil and will still be wired in. Has no effect with
	// fewer than 2 configured upstreams; Validate rejects that combination.
	UpstreamFilter handlers.UpstreamFilter

	// ScopesSupported lists the OAuth 2.0 scope values advertised in discovery documents.
	// If nil or empty, defaults to registration.DefaultScopes (["openid", "profile", "email", "offline_access"]).
	// This is advertised in /.well-known/openid-configuration and
	// /.well-known/oauth-authorization-server discovery endpoints.
	ScopesSupported []string

	// BaselineClientScopes is a baseline set of OAuth 2.0 scopes the embedded
	// DCR handler unions into every newly registered client's scope set. Empty
	// means current behavior is preserved (DCR registers exactly what the client
	// requested, or the intersection of registration.DefaultScopes with
	// ScopesSupported if the client requested none).
	// All entries must also be present in ScopesSupported. When ScopesSupported
	// is empty, the validation gate uses registration.DefaultScopes as the
	// superset — so standard OIDC scopes (e.g. "offline_access") work without
	// enumerating ScopesSupported explicitly.
	BaselineClientScopes []string

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

	// CIMDEnabled enables the CIMD storage decorator so the authorization server
	// accepts HTTPS URLs as client_id values without prior DCR registration.
	CIMDEnabled bool

	// CIMDCacheMaxSize is the maximum number of CIMD documents held in the LRU
	// cache. Zero is replaced by a default (256) in applyDefaults when CIMDEnabled
	// is true.
	CIMDCacheMaxSize int

	// CIMDCacheFallbackTTL is the fixed TTL applied to all cached CIMD documents
	// (Cache-Control header parsing is not yet implemented). Zero is replaced by
	// a default (5 minutes) in applyDefaults when CIMDEnabled is true.
	CIMDCacheFallbackTTL time.Duration

	// InsecureAllowHTTP permits an http:// issuer URL for non-localhost hosts.
	// Only set this for in-cluster Kubernetes deployments on a trusted network.
	// Production deployments reachable outside the cluster MUST use https://.
	InsecureAllowHTTP bool

	// TrustedIssuers lists external OIDC issuers whose tokens are accepted as
	// RFC 8693 subject tokens or RFC 7523 JWT-bearer assertions. See the
	// identically named field on RunConfig for the full doc comment, including
	// the fail-closed AllowedActors semantics and the audience/scope constraints
	// operators must account for.
	TrustedIssuers []tokenexchange.TrustedIssuer

	// AllowConfidentialClientRegistration permits DCR of confidential clients
	// (client_secret_basic / client_secret_post). See RunConfig for the full
	// semantics; disabling it does not revoke already-minted secrets.
	AllowConfidentialClientRegistration bool

	// AllowPrivateKeyJWTRegistration permits DCR of clients using
	// private_key_jwt authentication. See RunConfig for the full semantics.
	AllowPrivateKeyJWTRegistration bool

	// ForceConfidentialRedirectURIs lists redirect URIs that are always
	// registered as confidential clients, even when the DCR request declares
	// "none". See the identically named field on RunConfig for the full
	// semantics and rationale.
	ForceConfidentialRedirectURIs []string

	// InsecureAllowConfidentialOverLoopbackHTTP opts in to confidential clients
	// when Issuer is a plain-HTTP loopback URL. See the identically named field
	// on RunConfig for the full semantics and rationale.
	InsecureAllowConfidentialOverLoopbackHTTP bool

	// DelegateClients are pre-provisioned confidential OAuth clients in their
	// resolved runtime form. ClientSecret has already been read from its file
	// or environment-variable reference. See RunConfig.DelegateClients for the
	// serialized configuration.
	DelegateClients []DelegateClient
}

// DelegateClient is the resolved form of DelegateClientRunConfig: the secret
// has already been read from its file or environment variable reference.
type DelegateClient struct {
	// ClientID is the OAuth client_id this client presents at the token endpoint.
	ClientID string

	// ClientSecret is the resolved (plaintext) client secret.
	ClientSecret string //nolint:gosec // G117: field legitimately holds sensitive data

	// Scopes are the OAuth scopes this client may request.
	Scopes []string

	// Audiences are the RFC 8707 resource values this client may request a token for.
	Audiences []string
}

// Validate checks that the Config is valid.
func (c *Config) Validate() error {
	slog.Debug("validating authserver config", "issuer", c.Issuer)

	if err := validateIssuerURL(c.Issuer, c.InsecureAllowHTTP); err != nil {
		return fmt.Errorf("issuer: %w", err)
	}

	if err := c.validateConfidentialClientConfig(); err != nil {
		return err
	}

	if c.AuthorizationEndpointBaseURL != "" {
		if err := validateIssuerURL(c.AuthorizationEndpointBaseURL, c.InsecureAllowHTTP); err != nil {
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

	if err := c.validateUpstreamFilter(); err != nil {
		return err
	}

	// AllowedAudiences is required for MCP compliance.
	// Per MCP specification, clients MUST include the "resource" parameter (RFC 8707),
	// which requires the server to have configured allowed audiences to validate against.
	if len(c.AllowedAudiences) == 0 {
		return fmt.Errorf("at least one allowed audience is required for MCP compliance (RFC 8707 resource parameter validation)")
	}

	if err := validateAllowedAudiences(c.AllowedAudiences); err != nil {
		return err
	}

	// BaselineClientScopes must be a subset of ScopesSupported. RunConfig.Validate
	// catches this for the YAML-loaded path, but a caller that constructs Config
	// directly bypasses that; failing here gives them a clearer call stack than
	// the inner validateParams in the provider layer.
	// When ScopesSupported is empty, use DefaultScopes as the superset (matching
	// what applyDefaults substitutes at startup).
	if err := c.validateBaselineClientScopes(); err != nil {
		return err
	}

	if err := c.validateCIMDBounds(); err != nil {
		return err
	}

	if err := c.validateDelegationTokenLifespan(); err != nil {
		return err
	}

	// RunConfig.Validate() also runs these checks (see the comment there for
	// why: buildUpstreamConfigs's live DCR registration happens before this
	// method is reached), but a caller that constructs Config directly bypasses
	// that, same as the BaselineClientScopes check above.
	if err := c.validateDelegationConfig(); err != nil {
		return err
	}
	c.warnTrustedIssuerAudiences()

	slog.Debug("authserver config validation passed",
		"issuer", c.Issuer,
		"upstream_count", len(c.Upstreams),
	)
	return nil
}

// validateBaselineClientScopes ensures every baseline scope is advertised by
// ScopesSupported. When it is empty, applyDefaults supplies DefaultScopes.
func (c *Config) validateBaselineClientScopes() error {
	effective := c.ScopesSupported
	if len(effective) == 0 {
		effective = registration.DefaultScopes
	}
	return registration.ValidateScopeSubset(c.BaselineClientScopes, effective, "baseline_client_scopes")
}

// validateDelegationConfig validates delegate clients and trusted issuers for
// callers that construct a runtime Config directly.
func (c *Config) validateDelegationConfig() error {
	if err := validateResolvedDelegateClients(c.DelegateClients, c.ScopesSupported, c.AllowedAudiences); err != nil {
		return err
	}
	return validateTrustedIssuers(c.TrustedIssuers, c.Issuer, c.AllowedAudiences)
}

// validateConfidentialClientConfig groups cleartext-transport validation for
// all confidential clients and the force-confidential-redirect-uris override
// behind a single call site, keeping Config.Validate's own cyclomatic
// complexity down.
func (c *Config) validateConfidentialClientConfig() error {
	if err := ValidateConfidentialClientTransport(
		c.AllowConfidentialClientRegistration || len(c.DelegateClients) > 0, c.InsecureAllowHTTP,
		c.Issuer, c.InsecureAllowConfidentialOverLoopbackHTTP); err != nil {
		return err
	}
	return ValidateForceConfidentialRedirectURIs(c.ForceConfidentialRedirectURIs, c.AllowConfidentialClientRegistration)
}

// validateCIMDBounds rejects invalid CIMD cache bounds when CIMD is enabled.
// When CIMD is disabled the cache fields are ignored.
func (c *Config) validateCIMDBounds() error {
	if !c.CIMDEnabled {
		return nil
	}
	if c.CIMDCacheMaxSize < 1 {
		return fmt.Errorf("cimd.cache_max_size must be >= 1 when CIMD is enabled")
	}
	if c.CIMDCacheFallbackTTL < 0 {
		return fmt.Errorf("cimd.cache_fallback_ttl must be non-negative when CIMD is enabled")
	}
	return nil
}

// validateDelegationTokenLifespan rejects negative or excessively long delegation
// token lifespans. Capped at oauthserver.MaxAccessTokenLifespan (the same ceiling
// the token-exchange Factory enforces) so validation and construction agree on a
// single source of truth — delegated tokens should be short-lived. Zero is
// accepted; applyDefaults substitutes the default.
func (c *Config) validateDelegationTokenLifespan() error {
	if c.DelegationTokenLifespan < 0 {
		return fmt.Errorf("delegation token lifespan must not be negative")
	}
	if c.DelegationTokenLifespan > oauthserver.MaxAccessTokenLifespan {
		return fmt.Errorf("delegation token lifespan must not exceed %v", oauthserver.MaxAccessTokenLifespan)
	}
	return nil
}

// validateTrustedIssuers checks every configured TrustedIssuer as early as
// RunConfig.Validate can catch it, so a bad actor_claim, actor_matcher, or
// duplicate issuer_url fails before buildUpstreamConfigs performs live RFC 7591
// registration against upstream IdPs — not after it, on a crash loop that
// orphans an upstream registration on every restart. Shared by
// RunConfig.Validate() and Config.Validate() (mirrors
// validateBaselineClientScopes); NewMultiIssuerTokenValidator repeats these
// checks again at server startup as defence in depth.
//
// issuer_url is checked by validateTrustedIssuerURL, jwks_url (when set) by
// validateJWKSEndpointURL — see their doc comments for the URL rules each
// enforces. The remaining structural checks (required fields, self-issuer
// collision, duplicate issuers, ActorClaim reachability, and ActorMatcher
// compilation) run via tokenexchange.ValidateTrustedIssuers.
func validateTrustedIssuers(issuers []tokenexchange.TrustedIssuer, selfIssuer string, allowedAudiences []string) error {
	for _, ti := range issuers {
		if err := validateTrustedIssuerURL(ti.IssuerURL, ti.InsecureAllowHTTP); err != nil {
			return fmt.Errorf("trusted_issuers: issuer_url %q: %w", ti.IssuerURL, err)
		}
		// AllowPrivateIPs without a hand-configured jwks_url would let OIDC
		// discovery — a document fetched from, and thus influenceable by,
		// the external issuer itself — choose the private target the dial
		// is allowed to reach. Requiring jwks_url pins that target to
		// operator-supplied config instead.
		//
		// This is the fail-fast layer, not the only one: validateTrustedIssuer
		// (multi_issuer_validator.go) enforces the same invariant inside
		// NewMultiIssuerTokenValidator, so a caller constructing a validator
		// without routing through Config.Validate is still covered. Note that
		// ensureRegistered's ValidateJWKSURL does NOT cover it — that check is
		// gated on net.ParseIP, so it only rejects private IP *literals*, and a
		// discovery document advertising a private *hostname* passes it
		// cleanly. Checking here and in the constructor is deliberate
		// duplication, not redundancy.
		if ti.AllowPrivateIPs && ti.JWKSURL == "" {
			return fmt.Errorf(
				"trusted_issuers: issuer_url %q: allow_private_ips requires jwks_url to be set explicitly; "+
					"otherwise OIDC discovery — fetched from the external issuer — would choose the private target",
				ti.IssuerURL)
		}
		if ti.JWKSURL != "" {
			if err := validateJWKSEndpointURL(ti.JWKSURL, ti.InsecureAllowHTTP, ti.AllowPrivateIPs); err != nil {
				return fmt.Errorf("trusted_issuers: jwks_url %q: %w", ti.JWKSURL, err)
			}
		}
	}
	if err := tokenexchange.ValidateTrustedIssuers(issuers, selfIssuer, allowedAudiences); err != nil {
		return fmt.Errorf("trusted_issuers: %w", err)
	}
	for _, issuer := range issuers {
		if issuer.JWTBearerGrant == nil {
			continue
		}
		for _, binding := range issuer.JWTBearerGrant.SubjectBindings {
			for _, resource := range binding.AllowedResources {
				if !slices.Contains(allowedAudiences, resource) {
					return fmt.Errorf(
						"trusted_issuers: issuer_url %q: jwt_bearer_grant subject %q resource %q is not in allowed_audiences",
						issuer.IssuerURL, binding.Subject, resource)
				}
			}
		}
	}
	return nil
}

// validateJWKSEndpointURL checks that rawURL parses, has a host, uses the
// "https" scheme (or "http" when insecureAllowHTTP is set), and — when the
// host is an IP literal — is not a private or loopback address unless
// allowPrivateIPs permits it. Unlike validateIssuerURL, it does not enforce
// OIDC issuer-identifier rules (no query/fragment/trailing-slash) since a
// JWKS endpoint legitimately carries those.
//
// Delegates to tokenexchange.ValidateJWKSURL, the same predicate the runtime
// choke point (ensureRegistered, called on every JWKS fetch) enforces — the two
// were previously separate implementations that had drifted apart (a
// runtime check laxer than this one would silently defeat this config-time
// guard), so this is now the single source of truth for both.
//
// Deliberately not networking.ValidateEndpointURL /
// ValidateEndpointURLWithInsecure: both also honor the
// INSECURE_DISABLE_URL_VALIDATION environment variable, which would let an
// unrelated env var silently disable this SSRF-relevant scheme check; the
// insecure variant also skips the parse/host check entirely rather than
// only relaxing the scheme. This helper takes its "insecure" bits solely
// from the issuer's own explicit InsecureAllowHTTP/AllowPrivateIPs fields.
func validateJWKSEndpointURL(rawURL string, insecureAllowHTTP, allowPrivateIPs bool) error {
	return tokenexchange.ValidateJWKSURL(rawURL, insecureAllowHTTP, allowPrivateIPs)
}

// warnTrustedIssuerAudiences logs a warning for each TrustedIssuer whose
// ExpectedAudience is absent from AllowedAudiences. This is not a hard
// error: a subject token may carry additional audiences beyond
// ExpectedAudience, so the mismatch is sufficient-but-not-necessary for
// every exchange from that issuer to fail — an operator may know a specific
// subject token will present a matching aud even though ExpectedAudience
// itself is not in AllowedAudiences. But when it's not intentional, this is
// the invalid_target footgun documented on the TrustedIssuers field: warn so
// it surfaces at startup instead of at the first exchange attempt.
func (c *Config) warnTrustedIssuerAudiences() {
	for _, ti := range c.TrustedIssuers {
		if !slices.Contains(c.AllowedAudiences, ti.ExpectedAudience) {
			slog.Warn("trusted issuer's expected_audience is not in allowed_audiences; "+
				"token exchange will fail with invalid_target unless subject tokens from it "+
				"carry an additional audience matching one",
				"issuer", ti.IssuerURL, "expected_audience", ti.ExpectedAudience)
		}
	}
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
//
// Called from buildPureOAuth2Config at the RunConfig → upstream.OAuth2Config
// conversion boundary so that DCR-specific fields are validated before they
// are dropped during conversion.
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

		// When the operator configures DCRConfig.RegistrationEndpoint, the
		// resolver bypasses discovery and therefore cannot populate
		// AuthorizationEndpoint or TokenEndpoint from server metadata. The
		// run-config must supply both explicitly or the upstream is
		// unusable: registration would succeed and the first authorize or
		// token-exchange call would silently fail with empty endpoints.
		// Discovery flow (DCRConfig.DiscoveryURL) is unaffected — those
		// fields populate from metadata.
		if c.DCRConfig.RegistrationEndpoint != "" {
			if c.AuthorizationEndpoint == "" || c.TokenEndpoint == "" {
				return fmt.Errorf(
					"oauth2 upstream: authorization_endpoint and token_endpoint are required " +
						"when dcr_config.registration_endpoint is set (no discovery to populate them)")
			}
		}
	}

	if c.IdentityFromToken != nil && c.IdentityFromToken.SubjectPath == "" {
		return fmt.Errorf("oauth2 upstream: identity_from_token.subject_path must not be empty when identity_from_token is configured")
	}

	return nil
}

// Validate checks that the DCRUpstreamConfig specifies exactly one of
// DiscoveryURL or RegistrationEndpoint, that the configured URL is well-formed
// and uses HTTPS (or http on a loopback host for local development), and that
// the two initial-access-token sources (InitialAccessTokenFile and
// InitialAccessTokenEnvVar) are not both set.
//
// DiscoveryURL triggers runtime resolution of the registration endpoint via
// RFC 8414 / OIDC Discovery; RegistrationEndpoint bypasses discovery for
// providers that do not publish metadata. Requiring exactly one prevents
// ambiguity about which URL the authserver should contact for registration.
//
// URL well-formedness and HTTPS are enforced here at the schema-validation
// boundary so misconfiguration fails fast at startup rather than at first DCR
// attempt; the runtime callers (pkg/oauthproto/discovery.go and
// pkg/oauthproto/dcr.go) defend in depth, but this is the natural fail-fast
// point.
//
// Rejecting a config that supplies both an InitialAccessTokenFile and an
// InitialAccessTokenEnvVar prevents a credential-rotation footgun: if both
// were accepted, an operator updating the env-var value would not realize
// the file source still wins (or vice versa) and would silently keep
// presenting a stale token at registration.
func (c *DCRUpstreamConfig) Validate() error {
	hasDiscovery := c.DiscoveryURL != ""
	hasRegistration := c.RegistrationEndpoint != ""
	switch {
	case !hasDiscovery && !hasRegistration:
		return fmt.Errorf("dcr_config: either discovery_url or registration_endpoint is required")
	case hasDiscovery && hasRegistration:
		return fmt.Errorf("dcr_config: discovery_url and registration_endpoint are mutually exclusive")
	case hasDiscovery:
		if err := networking.ValidateEndpointURL(c.DiscoveryURL); err != nil {
			return fmt.Errorf("dcr_config: invalid discovery_url: %w", err)
		}
	case hasRegistration:
		if err := networking.ValidateEndpointURL(c.RegistrationEndpoint); err != nil {
			return fmt.Errorf("dcr_config: invalid registration_endpoint: %w", err)
		}
	}

	if c.InitialAccessTokenFile != "" && c.InitialAccessTokenEnvVar != "" {
		return fmt.Errorf(
			"dcr_config: initial_access_token_file and initial_access_token_env_var are mutually exclusive")
	}

	return nil
}

func validateZeroUpstreamMode(upstreamCount, delegateClientCount int, issuers []tokenexchange.TrustedIssuer) error {
	if upstreamCount > 0 || delegateClientCount > 0 || JWTBearerGrantEnabled(issuers) {
		return nil
	}
	return fmt.Errorf("at least one upstream is required unless delegate clients or a " +
		"trusted issuer with JWT bearer grant is configured")
}

// validateUpstreams validates the upstream configurations.
func (c *Config) validateUpstreams() error {
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

		if err := validateUpstreamType(up, c.InsecureAllowHTTP); err != nil {
			return err
		}
	}

	return validateZeroUpstreamMode(len(c.Upstreams), len(c.DelegateClients), c.TrustedIssuers)
}

// validateUpstreamFilter rejects an UpstreamFilter configured with fewer than
// 2 upstreams. handlers.computeChain consults the filter only when there is a
// non-first upstream to narrow, so with a single upstream the filter would
// silently never be invoked; this fails loudly instead of letting it no-op
// without any indication to the caller.
func (c *Config) validateUpstreamFilter() error {
	if c.UpstreamFilter != nil && len(c.Upstreams) < 2 {
		return fmt.Errorf("upstream_filter is configured but has no effect with fewer than 2 upstreams")
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
func validateUpstreamType(up *UpstreamConfig, insecureAllowHTTP bool) error {
	switch up.Type {
	case UpstreamProviderTypeOIDC:
		if up.OIDCConfig == nil {
			return fmt.Errorf("upstream %q: oidc_config is required for OIDC provider", up.Name)
		}
		if up.OAuth2Config != nil {
			return fmt.Errorf("upstream %q: oauth2_config must not be set when type is %q", up.Name, up.Type)
		}
		if err := up.OIDCConfig.ValidateWithInsecure(insecureAllowHTTP || up.OIDCConfig.InsecureAllowHTTP); err != nil {
			return fmt.Errorf("upstream %q: %w", up.Name, err)
		}
	case UpstreamProviderTypeOAuth2:
		if up.OAuth2Config == nil {
			return fmt.Errorf("upstream %q: oauth2_config is required for OAuth2 provider", up.Name)
		}
		if up.OIDCConfig != nil {
			return fmt.Errorf("upstream %q: oidc_config must not be set when type is %q", up.Name, up.Type)
		}
		if err := up.OAuth2Config.ValidateWithInsecure(insecureAllowHTTP || up.OAuth2Config.InsecureAllowHTTP); err != nil {
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
	if c.DelegationTokenLifespan == 0 {
		c.DelegationTokenLifespan = 15 * time.Minute
		slog.Debug("applied default delegation token lifespan", "duration", c.DelegationTokenLifespan)
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
	// One-time operator-facing signal for the omitted-scope registration
	// behavior (see registration.ValidateScopes and issue #6186). The outcome
	// is fully determined by ScopesSupported, so it is logged once here
	// rather than on every unauthenticated /oauth/register call or CIMD
	// resolution — those record it at Debug.
	if intersection, dropped, dcrErr := registration.ValidateScopes(nil, c.ScopesSupported); dcrErr != nil {
		slog.Warn("scopes_supported shares no scopes with the default set; "+
			"clients that omit scope will be rejected and must declare scope explicitly",
			"scopes_supported", c.ScopesSupported,
			"default_scopes", registration.DefaultScopes)
	} else if len(dropped) > 0 {
		slog.Info("scopes_supported does not cover the default scope set; "+
			"clients that omit scope will register with the intersection",
			"intersection", intersection,
			"dropped_defaults", dropped)
	}
	if c.CIMDEnabled && c.CIMDCacheMaxSize == 0 {
		c.CIMDCacheMaxSize = 256
		slog.Debug("applied default cimd cache_max_size", "size", c.CIMDCacheMaxSize)
	}
	if c.CIMDEnabled && c.CIMDCacheFallbackTTL == 0 {
		c.CIMDCacheFallbackTTL = 5 * time.Minute
		slog.Debug("applied default cimd cache_fallback_ttl", "ttl", c.CIMDCacheFallbackTTL)
	}
	return nil
}

// ValidateConfidentialClientTransport rejects cleartext HTTP configurations
// when any confidential client is enabled, whether it is admitted through DCR
// or statically declared. Static clients do not enable DCR; they share this
// validation because their secrets are sent to the token endpoint.
//
//  1. insecureAllowHTTP is set: the server accepts a plain-HTTP issuer for
//     any host, not just loopback. Always rejected for confidential clients.
//  2. issuer is a plain-HTTP loopback URL (e.g. "http://localhost:18080").
//     This is rejected by default but may be explicitly enabled with
//     insecureAllowConfidentialOverLoopbackHTTP. The opt-in does not permit
//     non-loopback HTTP issuers and still requires a valid issuer URL.
func ValidateConfidentialClientTransport(
	allowConfidential, insecureAllowHTTP bool,
	issuer string, insecureAllowConfidentialOverLoopbackHTTP bool,
) error {
	if !allowConfidential {
		return nil
	}
	if insecureAllowHTTP {
		return fmt.Errorf("allow_confidential_client_registration cannot be combined with insecure_allow_http: " +
			"confidential clients would send secrets over cleartext HTTP")
	}
	parsed, err := url.Parse(issuer)
	if err != nil {
		return errors.New("confidential clients require a valid issuer URL")
	}
	if parsed.Scheme != "http" {
		return nil
	}
	if insecureAllowConfidentialOverLoopbackHTTP && networking.IsLocalhost(parsed.Host) {
		if err := validateIssuerURL(issuer, false); err != nil {
			return errors.New("confidential clients require a valid issuer URL")
		}
	}
	if insecureAllowConfidentialOverLoopbackHTTP && !networking.IsLocalhost(parsed.Host) {
		return fmt.Errorf(
			"allow_confidential_client_registration cannot use the loopback HTTP opt-in with a non-loopback issuer (%q): "+
				"confidential clients would send secrets over cleartext HTTP", issuer)
	}
	if !insecureAllowConfidentialOverLoopbackHTTP && networking.IsLocalhost(parsed.Host) {
		return fmt.Errorf("allow_confidential_client_registration cannot be combined with a plain-HTTP loopback issuer (%q) unless "+
			"insecure_allow_confidential_over_loopback_http is set: confidential clients would send secrets over cleartext HTTP",
			issuer)
	}
	if !networking.IsLocalhost(parsed.Host) {
		return fmt.Errorf("allow_confidential_client_registration cannot use a plain-HTTP non-loopback issuer (%q): "+
			"confidential clients would send secrets over cleartext HTTP", issuer)
	}
	return nil
}

// ValidateForceConfidentialRedirectURIs rejects a misconfigured
// force_confidential_redirect_uris list: a non-empty list requires
// allowConfidential (there is no confidential-client path to force a
// registration onto otherwise), and every entry must be a valid https
// non-loopback redirect URI. The https-non-loopback requirement mirrors the
// restriction validateAuthMethod (pkg/authserver/server/registration/dcr.go)
// already applies to ordinary confidential DCR: a redirect URI reachable on
// loopback or a private scheme is by construction a public client (OAuth 2.1
// §2.1), and this override must not be a way to punch through that
// restriction and hand a distributed native app a secret.
func ValidateForceConfidentialRedirectURIs(uris []string, allowConfidential bool) error {
	if len(uris) == 0 {
		return nil
	}
	if !allowConfidential {
		return fmt.Errorf(
			"force_confidential_redirect_uris requires allow_confidential_client_registration to be true")
	}
	for _, uri := range uris {
		if err := oauthproto.ValidateRedirectURI(uri, oauthproto.RedirectURIPolicyStrict); err != nil {
			return fmt.Errorf("force_confidential_redirect_uris: %q: %w", uri, err)
		}
		parsed, err := url.Parse(uri)
		if err != nil {
			return fmt.Errorf("force_confidential_redirect_uris: %q: invalid URL: %w", uri, err)
		}
		if networking.IsLocalhost(parsed.Hostname()) {
			return fmt.Errorf(
				"force_confidential_redirect_uris: %q must not be a loopback redirect URI; "+
					"a loopback client is a public client by construction and must not be issued a secret", uri)
		}
	}
	return nil
}

// validateIssuerURL validates that the issuer is a valid URL.
// Per OIDC Core Section 3.1.2.1 and RFC 8414 Section 2, the issuer
// MUST use the "https" scheme, except for localhost during development.
// When insecureAllowHTTP is true, http:// is also permitted for non-localhost
// hosts (for in-cluster Kubernetes deployments on trusted networks).
//
// This server's own issuer is additionally held to a no-trailing-slash rule
// that OIDC itself does not require (see validateIssuerURLCore's
// allowTrailingSlash parameter) — defensible here only because we control
// this value, unlike a trusted external issuer (validateTrustedIssuerURL).
func validateIssuerURL(issuer string, insecureAllowHTTP bool) error {
	return validateIssuerURLCore(issuer, insecureAllowHTTP, true, false)
}

// validateTrustedIssuerURL is like validateIssuerURL but never exempts
// localhost from the HTTPS requirement: a trusted external issuer is not
// this server's own issuer, so it must not inherit the same-host
// development convenience validateIssuerURL grants the server's own issuer
// and AuthorizationEndpointBaseURL. Without this, "issuer_url:
// http://localhost:9000" with insecure_allow_http: false would pass config
// validation here yet fail at runtime, since the per-issuer HTTP client is
// still built with InsecureAllowHTTP=false (see NewMultiIssuerTokenValidator)
// — jwks_url has no such exemption, so the two would otherwise disagree.
//
// Unlike validateIssuerURL, a trailing slash is accepted: OIDC Discovery §3
// forbids query and fragment components on an issuer identifier, but not a
// trailing slash — §4.1 only requires one be trimmed before the well-known
// discovery path is appended, which presupposes a trailing-slash issuer is
// legal in the first place, and §4.3 requires the discovery document's
// "issuer" to match the token's "iss" verbatim. Microsoft Entra ID v1 — the
// default for a newly registered API — issues
// "iss": "https://sts.windows.net/{tenant}/" with a trailing slash, so
// rejecting it here would make v1 tokens impossible to configure at all.
func validateTrustedIssuerURL(issuer string, insecureAllowHTTP bool) error {
	return validateIssuerURLCore(issuer, insecureAllowHTTP, false, true)
}

// validateIssuerURLCore is the shared implementation behind validateIssuerURL
// and validateTrustedIssuerURL. localhostExempt controls whether a loopback
// host is treated as HTTPS-exempt regardless of insecureAllowHTTP.
// allowTrailingSlash controls whether a trailing slash on the issuer is
// accepted — see validateTrustedIssuerURL's doc comment for why the trusted-
// issuer path must allow it while this server's own issuer does not.
func validateIssuerURLCore(issuer string, insecureAllowHTTP, localhostExempt, allowTrailingSlash bool) error {
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
	// Userinfo is rejected on two independent grounds. It cannot work: OIDC
	// Discovery 1.0 Section 4.3 compares the discovery document's "issuer"
	// against this value by exact string match, and no provider echoes back
	// embedded credentials, so such an issuer always fails discovery. And it
	// must not be stored: a password here would sit in the RunConfig and be
	// echoed by the validation errors and startup warnings that quote the
	// issuer URL. Rejecting it outright beats redacting it at every use.
	// Note that parsed.User is non-nil even for "https://user@host" with no
	// password, which is equally unusable as an issuer identifier.
	if parsed.User != nil {
		return fmt.Errorf("must not contain userinfo (credentials in the URL)")
	}

	// HTTPS is required unless it's a loopback address (for development, and
	// only when localhostExempt) or insecureAllowHTTP is explicitly set.
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" {
			return fmt.Errorf("scheme must be https (or http for localhost)")
		}
		if !insecureAllowHTTP && (!localhostExempt || !networking.IsLocalhost(parsed.Host)) {
			return fmt.Errorf("http scheme is only allowed for localhost, use https for %s", parsed.Hostname())
		}
	}

	// Not an OIDC requirement — see validateTrustedIssuerURL's doc comment.
	// ToolHive's own issuer is held to this stricter, self-imposed rule
	// since we control the value; a trusted external issuer is not.
	if !allowTrailingSlash && strings.HasSuffix(issuer, "/") {
		return fmt.Errorf("must not have trailing slash")
	}

	return nil
}
