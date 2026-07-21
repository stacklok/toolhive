// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package runner provides integration between the proxy runner and the auth server.
package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	tcredis "github.com/stacklok/toolhive-core/redis"
	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/tokenenc"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/bodylimit"
)

// Redis ACL credential environment variable names.
// These are set by the operator when Redis storage is configured.
const (
	// RedisUsernameEnvVar is the environment variable for the Redis ACL username.
	// #nosec G101 -- This is an environment variable name, not a hardcoded credential
	RedisUsernameEnvVar = "TOOLHIVE_AUTH_SERVER_REDIS_USERNAME"

	// RedisPasswordEnvVar is the environment variable for the Redis ACL password.
	// #nosec G101 -- This is an environment variable name, not a hardcoded credential
	RedisPasswordEnvVar = "TOOLHIVE_AUTH_SERVER_REDIS_PASSWORD"

	// maxAuthServerBodySize caps auth-server request bodies; shared with the DCR
	// endpoint via handlers.MaxDCRBodySize so the two bounds cannot drift.
	maxAuthServerBodySize = handlers.MaxDCRBodySize
)

// EmbeddedAuthServer wraps the authorization server for integration with the proxy runner.
// It handles configuration transformation from authserver.RunConfig to authserver.Config,
// manages resource lifecycle, and provides HTTP handlers for OAuth/OIDC endpoints.
//
// The DCR credential store is owned by the underlying authserver.Server and
// reached via DCRStore(); see that accessor's doc for SECURITY and lifecycle
// notes. Storing it twice on this struct would create a drift window with
// the server's copy, so we delegate through e.server.DCRStore() instead.
type EmbeddedAuthServer struct {
	server      authserver.Server
	keyProvider keys.KeyProvider
	closeOnce   sync.Once
	closeErr    error
}

// NewEmbeddedAuthServer creates an EmbeddedAuthServer from authserver.RunConfig.
// It loads signing keys from files, reads HMAC secrets from files,
// resolves the upstream client secret from file or environment variable, and initializes
// all auth server components.
//
// The cfg parameter contains file paths and environment variable names that are
// resolved at runtime to build the underlying authserver.Config.
func NewEmbeddedAuthServer(ctx context.Context, cfg *authserver.RunConfig) (*EmbeddedAuthServer, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	// Register gjson modifiers used by IdentityFromToken configs (e.g. @upstreamjwt).
	// Without this, modifier-bearing paths silently fail to resolve.
	upstream.RegisterModifiers()

	// Fail loudly on operator-supplied misconfiguration (e.g. a baseline
	// scope absent from scopes_supported) BEFORE touching storage or any
	// other side-effecting work, so a bad config never reaches the network
	// or filesystem. NewEmbeddedAuthServerWithStorage re-validates for callers
	// that invoke it directly; this earlier check keeps the failure ahead of
	// createStorage on this path.
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid run config: %w", err)
	}

	// Create the storage backend FIRST so the DCR resolver and the auth
	// server share the same persistence. Both MemoryStorage and RedisStorage
	// satisfy storage.DCRCredentialStore (verified by package-level var _
	// checks in pkg/authserver/storage), so an explicit type assertion at
	// the boundary is provably safe and keeps the wider Storage interface
	// from advertising secret-bearing DCR methods to every consumer. This
	// is the wiring change that lets a Redis-backed authserver reuse RFC
	// 7591 client registrations across replicas and restarts.
	stor, err := createStorage(ctx, cfg.Storage)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	return NewEmbeddedAuthServerWithStorage(ctx, cfg, stor)
}

// NewEmbeddedAuthServerWithStorage is the exported core constructor that
// builds an EmbeddedAuthServer around a caller-supplied storage backend. It
// lets external composition (e.g. an enterprise build) inject a decorated
// storage.Storage aggregate.
//
// What the injection does NOT solve: the supplied storage is the sole
// persistence boundary for this server instance. Injecting a shared backend
// (e.g. Redis) lets replicas share persisted state — DCR registrations,
// pending authorizations, upstream tokens — but it does not provide
// cross-replica message delivery or fan-out, and it does not establish session
// affinity. A request must still reach a replica that can resolve its session
// from the shared store; pinning a session to a replica (or ensuring all
// session state is in the shared store) remains the caller's responsibility,
// typically at the load balancer.
//
// The supplied storage MUST also implement storage.DCRCredentialStore (both
// OSS MemoryStorage and RedisStorage do); the constructor returns an error if
// it does not. It also validates cfg, so direct callers get the same
// fail-loud config check NewEmbeddedAuthServer performs before dispatch.
//
// NewEmbeddedAuthServer dispatches into this helper after running
// createStorage; tests dispatch into it directly so they can supply a
// closeTrackingStorage wrapper to verify the deferred-cleanup contract.
//
// Resource ownership: on success, the returned EmbeddedAuthServer takes
// ownership of stor (its Close releases the backend). On any error path
// after entry, the deferred cleanup closes stor before returning so a
// crash-looping caller (typical when DCR's network I/O fails) does not
// leak the Redis client connection pool / MemoryStorage cleanup goroutine
// on every restart. The named return retErr is the gate.
func NewEmbeddedAuthServerWithStorage(
	ctx context.Context,
	cfg *authserver.RunConfig,
	stor storage.Storage,
) (retEAS *EmbeddedAuthServer, retErr error) {
	// From here on, any error must close stor before returning.
	//
	// Both errors are passed through dcr.SanitizeErrorForLog before being
	// recorded: closeErr for symmetry with retErr, retErr because the
	// most common cause of reaching this gate is a wrapped DCR failure
	// whose error chain may inline several KiB of the upstream's raw
	// /register response body — that body is attacker-influenced and may
	// contain URL components that carry credentials (userinfo, query,
	// fragment). The existing dcr.LogStepError boundary log routes
	// through the same sanitiser; keep the two log paths consistent so
	// the cleanup log cannot regress to a less-defended state. The
	// "cause" key matches the package-wide vocabulary for the
	// triggering error.
	defer func() {
		if retErr != nil {
			if closeErr := stor.Close(); closeErr != nil {
				slog.Warn("failed to close storage on NewEmbeddedAuthServer error path",
					"error", dcr.SanitizeErrorForLog(closeErr),
					"cause", dcr.SanitizeErrorForLog(retErr),
				)
			}
		}
	}()

	// Validate cfg here too. NewEmbeddedAuthServer validates before
	// createStorage, but direct callers of this exported constructor would
	// otherwise skip the check. Placed inside the deferred-cleanup gate above so
	// a validation failure still closes the caller-supplied storage per the
	// resource-ownership contract.
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid run config: %w", err)
	}

	// 1. Create key provider from RunConfig.SigningKeyConfig
	keyProvider, err := createKeyProvider(cfg.SigningKeyConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create key provider: %w", err)
	}

	// 2. Load HMAC secrets from files
	hmacSecrets, err := loadHMACSecrets(cfg.HMACSecretFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to load HMAC secrets: %w", err)
	}

	// 3. Parse token lifespans
	accessLifespan, refreshLifespan, authCodeLifespan, err := parseTokenLifespans(cfg.TokenLifespans)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token lifespans: %w", err)
	}

	// 4. Type-assert to the DCR-capable handle for the resolver. The
	// per-backend `var _ DCRCredentialStore = (*MemoryStorage)(nil)` /
	// `(*RedisStorage)(nil)` checks make this provably safe for production
	// backends; surfacing a non-DCR backend as a constructor error keeps
	// misconfiguration fail-loud at boot rather than at first DCR resolve.
	dcrStore, ok := stor.(storage.DCRCredentialStore)
	if !ok {
		return nil, fmt.Errorf("storage backend %T does not implement storage.DCRCredentialStore", stor)
	}

	// 5. Build upstream configurations. The DCR resolver caches RFC 7591
	// resolutions in dcrStore so re-entrant boot/reload paths reuse
	// previously-registered upstream clients instead of re-registering.
	upstreams, err := buildUpstreamConfigs(
		ctx, cfg.Upstreams, cfg.Issuer, dcr.NewStorageBackedStore(dcrStore), cfg.InsecureAllowHTTP,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build upstream configs: %w", err)
	}

	// 6. Parse delegation token lifespan if configured.
	var delegationLifespan time.Duration
	if cfg.DelegationTokenLifespan != "" {
		delegationLifespan, err = time.ParseDuration(cfg.DelegationTokenLifespan)
		if err != nil {
			return nil, fmt.Errorf("invalid delegation token lifespan: %w", err)
		}
	}

	// 7. Build the resolved Config.
	//
	// Defensive copies of the scope/audience slices: cfg is operator-supplied
	// input that may be retained or mutated by the caller (e.g. tests, a
	// future hot-reload path). The DCR handler reads these slices on every
	// request, so a mid-request mutation of the original would race. Cloning
	// here once at the boundary lets all downstream stages share by reference
	// safely. Cost is negligible — each slice is bounded by validation (≤10
	// for BaselineClientScopes, low cardinality in practice for the others).
	cimdEnabled, cimdCacheMaxSize, cimdCacheFallbackTTL := resolveCIMDConfig(cfg.CIMD)

	resolvedCfg := authserver.Config{
		Issuer:                       cfg.Issuer,
		AuthorizationEndpointBaseURL: cfg.AuthorizationEndpointBaseURL,
		KeyProvider:                  keyProvider,
		HMACSecrets:                  hmacSecrets,
		AccessTokenLifespan:          accessLifespan,
		RefreshTokenLifespan:         refreshLifespan,
		AuthCodeLifespan:             authCodeLifespan,
		DelegationTokenLifespan:      delegationLifespan,
		Upstreams:                    upstreams,
		ScopesSupported:              slices.Clone(cfg.ScopesSupported),
		BaselineClientScopes:         slices.Clone(cfg.BaselineClientScopes),
		AllowedAudiences:             slices.Clone(cfg.AllowedAudiences),
		CIMDEnabled:                  cimdEnabled,
		CIMDCacheMaxSize:             cimdCacheMaxSize,
		CIMDCacheFallbackTTL:         cimdCacheFallbackTTL,
		InsecureAllowHTTP:            cfg.InsecureAllowHTTP,
	}

	// 8. Create the auth server. authserver.New also asserts the DCR
	// capability internally so its DCRStore() accessor returns the same
	// asserted handle this constructor used for buildUpstreamConfigs.
	server, err := authserver.New(ctx, resolvedCfg, stor)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth server: %w", err)
	}

	return &EmbeddedAuthServer{
		server:      server,
		keyProvider: keyProvider,
	}, nil
}

// Handler returns the HTTP handler for OAuth/OIDC endpoints.
// The handler uses internal chi routing and serves all endpoints:
//   - /oauth/authorize, /oauth/callback, /oauth/token, /oauth/register
//   - /.well-known/jwks.json, /.well-known/oauth-authorization-server, /.well-known/openid-configuration
//
// All auth-server endpoints are body-size-limited to handlers.MaxDCRBodySize
// (64KB) and reject oversized requests with HTTP 413 Request Entity Too Large.
func (e *EmbeddedAuthServer) Handler() http.Handler {
	// Cap request bodies on all auth-server endpoints (e.g. POST /oauth/token,
	// /oauth/register) so they cannot be used for memory-exhaustion DoS. These
	// routes are mounted outside the MCP middleware chain (the proxies and vMCP
	// mount them via Routes()/RegisterHandlers, which derive from this handler),
	// so the cap is applied here to cover every consumer.
	return bodylimit.Middleware(maxAuthServerBodySize)(e.server.Handler())
}

// Close releases resources held by the EmbeddedAuthServer.
// This method is idempotent - subsequent calls after the first will return
// the same error (if any) without attempting to close resources again.
// Should be called during runner shutdown.
func (e *EmbeddedAuthServer) Close() error {
	e.closeOnce.Do(func() {
		e.closeErr = e.server.Close()
	})
	return e.closeErr
}

// IDPTokenStorage returns storage for upstream IDP tokens.
// Returns nil if no upstream IDP is configured.
// This is used by the upstream swap middleware to exchange ToolHive JWTs
// for upstream IDP tokens.
func (e *EmbeddedAuthServer) IDPTokenStorage() storage.UpstreamTokenStorage {
	return e.server.IDPTokenStorage()
}

// UpstreamTokenRefresher returns a refresher that can refresh expired upstream
// tokens using the upstream provider's refresh token grant.
func (e *EmbeddedAuthServer) UpstreamTokenRefresher() storage.UpstreamTokenRefresher {
	return e.server.UpstreamTokenRefresher()
}

// KeyProvider returns the signing key provider used by the authorization server.
// This enables in-process JWKS key lookups, eliminating the need for
// self-referential HTTP calls when the token validator runs in the same process.
func (e *EmbeddedAuthServer) KeyProvider() keys.KeyProvider {
	return e.keyProvider
}

// DCRStore returns the persistent DCR credential store the authorization
// server is wired against. This delegates to the underlying authserver.Server
// so this struct does not hold a redundant copy that could drift if the
// server ever swaps backends. See authserver.Server.DCRStore for SECURITY
// and lifecycle notes — the returned interface surfaces raw client_secret
// and registration_access_token values and MUST NOT be logged or rendered.
func (e *EmbeddedAuthServer) DCRStore() storage.DCRCredentialStore {
	return e.server.DCRStore()
}

// Routes returns the authorization server's HTTP route map.
//
// The /.well-known/ paths are registered explicitly because that namespace is shared:
// the vMCP server owns /.well-known/oauth-protected-resource (RFC 9728) on the same
// mux. Adding a new AS /.well-known/ endpoint therefore requires an explicit entry here.
//
// Discovery paths are registered with both exact and trailing-slash (prefix) patterns.
// The trailing-slash variants support RFC 8414 Section 3.1 path-based issuers, where
// the client constructs /.well-known/oauth-authorization-server/{issuer-path}.
//
// The /oauth/ subtree is registered as a prefix, so new /oauth/* endpoints added to
// the chi router are picked up automatically without changes to this method.
func (e *EmbeddedAuthServer) Routes() map[string]http.Handler {
	handler := e.Handler()
	return map[string]http.Handler{
		"/.well-known/openid-configuration":        handler,
		"/.well-known/openid-configuration/":       handler,
		"/.well-known/oauth-authorization-server":  handler,
		"/.well-known/oauth-authorization-server/": handler,
		"/.well-known/jwks.json":                   handler,
		"/oauth/":                                  handler,
	}
}

// RegisterHandlers registers the authorization server's HTTP routes on the given mux.
func (e *EmbeddedAuthServer) RegisterHandlers(mux *http.ServeMux) {
	for pattern, handler := range e.Routes() {
		mux.Handle(pattern, handler)
	}
}

// createKeyProvider creates a KeyProvider from SigningKeyRunConfig.
// Returns a GeneratingProvider if config is nil or empty (development mode).
func createKeyProvider(cfg *authserver.SigningKeyRunConfig) (keys.KeyProvider, error) {
	if cfg == nil || cfg.SigningKeyFile == "" {
		// Development mode: use ephemeral key
		return keys.NewGeneratingProvider(keys.DefaultAlgorithm), nil
	}

	keyCfg := keys.Config{
		KeyDir:           cfg.KeyDir,
		SigningKeyFile:   cfg.SigningKeyFile,
		FallbackKeyFiles: cfg.FallbackKeyFiles,
	}

	return keys.NewFileProvider(keyCfg)
}

// loadHMACSecrets reads HMAC secrets from files.
// Returns nil if no files are configured (development mode - authserver will generate ephemeral secret).
func loadHMACSecrets(files []string) (*servercrypto.HMACSecrets, error) {
	if len(files) == 0 {
		// Development mode: let authserver generate ephemeral secret
		return nil, nil
	}

	// Read current (first) secret
	// #nosec G304 - file path is from configuration, not user input
	current, err := os.ReadFile(files[0])
	if err != nil {
		return nil, fmt.Errorf("failed to read HMAC secret from %s: %w", files[0], err)
	}

	// Trim whitespace (Kubernetes Secret mounts may include trailing newlines)
	current = bytes.TrimSpace(current)

	secrets := &servercrypto.HMACSecrets{
		Current: current,
	}

	// Read rotated secrets (remaining files)
	for _, file := range files[1:] {
		if file == "" {
			continue // Skip empty paths
		}
		// #nosec G304 - file path is from configuration, not user input
		secret, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read rotated HMAC secret from %s: %w", file, err)
		}
		secrets.Rotated = append(secrets.Rotated, bytes.TrimSpace(secret))
	}

	return secrets, nil
}

// parseTokenLifespans parses duration strings from TokenLifespanRunConfig.
// Returns zero values for unset durations (defaults applied by authserver).
func parseTokenLifespans(cfg *authserver.TokenLifespanRunConfig) (access, refresh, authCode time.Duration, err error) {
	if cfg == nil {
		return 0, 0, 0, nil
	}

	if cfg.AccessTokenLifespan != "" {
		access, err = time.ParseDuration(cfg.AccessTokenLifespan)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid access token lifespan: %w", err)
		}
	}

	if cfg.RefreshTokenLifespan != "" {
		refresh, err = time.ParseDuration(cfg.RefreshTokenLifespan)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid refresh token lifespan: %w", err)
		}
	}

	if cfg.AuthCodeLifespan != "" {
		authCode, err = time.ParseDuration(cfg.AuthCodeLifespan)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid auth code lifespan: %w", err)
		}
	}

	return access, refresh, authCode, nil
}

// buildUpstreamConfigs converts UpstreamRunConfig slice to UpstreamConfig slice.
// It preserves the provider type so the factory can create the correct provider
// (OIDCProviderImpl for OIDC, BaseOAuth2Provider for OAuth2).
//
// For OAuth2 upstreams configured with DCRConfig, buildUpstreamConfigs performs
// RFC 7591 Dynamic Client Registration against the upstream authorization
// server (hitting the network on first call, using dcrStore on subsequent
// calls) and overlays the resulting ClientID / ClientSecret onto the output
// config via consumeResolution + applyResolutionToOAuth2Config (see
// dcr_adapter.go). The caller's runConfigs slice is not mutated: in-place
// mutation of caller-provided values surprises callers and can cause data
// races, so each element is cloned before applying DCR resolution.
//
// Error logging: this function is the boundary for DCR errors — on any
// failure from dcr.ResolveCredentials it emits exactly one structured
// slog.Error via dcr.LogStepError and returns the wrapped error to the
// caller without logging further. The resolver itself does not log
// errors, which avoids the log-and-return double-reporting pattern.
func buildUpstreamConfigs(
	ctx context.Context,
	runConfigs []authserver.UpstreamRunConfig,
	issuer string,
	dcrStore dcr.CredentialStore,
	insecureAllowHTTP bool,
) ([]authserver.UpstreamConfig, error) {
	configs := make([]authserver.UpstreamConfig, 0, len(runConfigs))

	for _, rc := range runConfigs {
		// Shallow copy of the outer UpstreamRunConfig so DCR resolution never
		// mutates the caller's slice element.
		rcCopy := rc

		var dcrResolution *dcr.Resolution
		// needsDCR returns false for nil input, so the explicit Type ==
		// OAuth2 guard is redundant. Keeping a single source of truth for
		// "does this upstream require DCR" avoids drift if the condition
		// ever needs to be extended (e.g., to support OIDC DCR).
		if needsDCR(rcCopy.OAuth2Config) {
			// Take a local copy of the OAuth2 sub-config. dcr.ResolveCredentials
			// reads it but does not mutate; consumeResolution is value-in /
			// value-out, so the caller's original OAuth2Config pointer target
			// is never reached by either call.
			o2 := *rcCopy.OAuth2Config

			req, err := newDCRRequest(&o2, issuer)
			if err != nil {
				return nil, fmt.Errorf("upstream %q: %w", rc.Name, err)
			}
			resolution, err := dcr.ResolveCredentials(ctx, req, dcrStore)
			if err != nil {
				// Emit the single boundary Error record with enough context to
				// correlate the failure back to this upstream; then return the
				// wrapped error without further logging.
				dcr.LogStepError(rc.Name, err)
				return nil, fmt.Errorf("upstream %q: %w", rc.Name, err)
			}
			o2 = consumeResolution(o2, resolution)
			rcCopy.OAuth2Config = &o2
			dcrResolution = resolution
		}

		cfg, err := buildUpstreamConfig(&rcCopy, insecureAllowHTTP)
		if err != nil {
			return nil, fmt.Errorf("upstream %q: %w", rc.Name, err)
		}

		// Apply the DCR-resolved ClientSecret to the built OAuth2Config.
		// The split between consumeResolution (run-config fields) and
		// applyResolutionToOAuth2Config (inline-only ClientSecret) is
		// documented in dcr_adapter.go — both calls must be paired to
		// produce a fully-resolved DCR client.
		if dcrResolution != nil && cfg.OAuth2Config != nil {
			applied := applyResolutionToOAuth2Config(*cfg.OAuth2Config, dcrResolution)
			cfg.OAuth2Config = &applied
		}

		configs = append(configs, *cfg)
	}

	return configs, nil
}

// buildUpstreamConfig builds an authserver.UpstreamConfig from UpstreamRunConfig.
// It preserves the provider type and builds the appropriate config.
func buildUpstreamConfig(rc *authserver.UpstreamRunConfig, insecureAllowHTTP bool) (*authserver.UpstreamConfig, error) {
	switch rc.Type {
	case authserver.UpstreamProviderTypeOIDC:
		oidcCfg, err := buildOIDCConfig(rc, insecureAllowHTTP)
		if err != nil {
			return nil, err
		}
		return &authserver.UpstreamConfig{
			Name:       rc.Name,
			Type:       authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: oidcCfg,
		}, nil

	case authserver.UpstreamProviderTypeOAuth2:
		oauth2Cfg, err := buildPureOAuth2Config(rc, insecureAllowHTTP)
		if err != nil {
			return nil, err
		}
		return &authserver.UpstreamConfig{
			Name:         rc.Name,
			Type:         authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: oauth2Cfg,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported upstream type: %s", rc.Type)
	}
}

// buildOIDCConfig builds an upstream.OIDCConfig for an OIDC provider.
// Discovery is deferred to the provider factory - we only resolve secrets here.
//
// Note: OIDCUpstreamRunConfig.UserInfoOverride is intentionally NOT propagated.
// OIDC providers resolve user identity from the ID token's "sub" claim (validated
// by OIDCProviderImpl.ExchangeCodeForIdentity), not from the UserInfo endpoint.
// The UserInfo endpoint may still be discovered via OIDC discovery for other
// purposes, but it is not used for identity resolution.
func buildOIDCConfig(rc *authserver.UpstreamRunConfig, insecureAllowHTTP bool) (*upstream.OIDCConfig, error) {
	if rc.OIDCConfig == nil {
		return nil, fmt.Errorf("oidc_config required for OIDC provider")
	}

	oidc := rc.OIDCConfig

	// Warn if UserInfoOverride is configured but won't be used
	if oidc.UserInfoOverride != nil {
		slog.Warn("userinfo_override is configured for OIDC provider but will not be used; "+
			"OIDC providers resolve identity from the ID token, not the UserInfo endpoint",
			"upstream", rc.Name,
		)
	}

	clientSecret, err := resolveSecret(oidc.ClientSecretFile, oidc.ClientSecretEnvVar)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve OIDC client secret: %w", err)
	}

	// Default scopes if not specified. The default includes offline_access
	// (standard OIDC mechanism for refresh tokens). Providers like Google that
	// use access_type=offline instead should specify explicit scopes in their
	// config to avoid sending both mechanisms.
	scopes := oidc.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "offline_access"}
	}

	return &upstream.OIDCConfig{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:                      oidc.ClientID,
			ClientSecret:                  clientSecret,
			RedirectURI:                   oidc.RedirectURI,
			Scopes:                        scopes,
			AdditionalAuthorizationParams: oidc.AdditionalAuthorizationParams,
		},
		Issuer:            oidc.IssuerURL,
		SubjectClaim:      oidc.SubjectClaim,
		AllowPrivateIPs:   oidc.AllowPrivateIPs,
		InsecureAllowHTTP: insecureAllowHTTP || oidc.InsecureAllowHTTP,
	}, nil
}

// buildPureOAuth2Config builds an upstream.OAuth2Config for a pure OAuth2 provider.
//
// Run-config-specific invariants (e.g. ClientID/DCRConfig mutual exclusion) are
// enforced here via OAuth2UpstreamRunConfig.Validate before secrets are
// resolved, since the downstream upstream.OAuth2Config validator only sees the
// flattened runtime shape and cannot observe DCR fields.
func buildPureOAuth2Config(rc *authserver.UpstreamRunConfig, insecureAllowHTTP bool) (*upstream.OAuth2Config, error) {
	if rc.OAuth2Config == nil {
		return nil, fmt.Errorf("oauth2_config required for OAuth2 provider")
	}

	oauth2 := rc.OAuth2Config
	if err := oauth2.Validate(); err != nil {
		return nil, err
	}
	clientSecret, err := resolveSecret(oauth2.ClientSecretFile, oauth2.ClientSecretEnvVar)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve OAuth2 client secret: %w", err)
	}

	cfg := &upstream.OAuth2Config{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:                      oauth2.ClientID,
			ClientSecret:                  clientSecret,
			RedirectURI:                   oauth2.RedirectURI,
			Scopes:                        oauth2.Scopes,
			AdditionalAuthorizationParams: oauth2.AdditionalAuthorizationParams,
		},
		AuthorizationEndpoint: oauth2.AuthorizationEndpoint,
		TokenEndpoint:         oauth2.TokenEndpoint,
		UserInfo:              convertUserInfoConfig(oauth2.UserInfo),
		AllowPrivateIPs:       oauth2.AllowPrivateIPs,
		InsecureAllowHTTP:     insecureAllowHTTP || oauth2.InsecureAllowHTTP,
	}

	if oauth2.TokenResponseMapping != nil {
		cfg.TokenResponseMapping = &upstream.TokenResponseMapping{
			AccessTokenPath:  oauth2.TokenResponseMapping.AccessTokenPath,
			ScopePath:        oauth2.TokenResponseMapping.ScopePath,
			RefreshTokenPath: oauth2.TokenResponseMapping.RefreshTokenPath,
			ExpiresInPath:    oauth2.TokenResponseMapping.ExpiresInPath,
		}
	}

	if oauth2.IdentityFromToken != nil {
		cfg.IdentityFromToken = &upstream.IdentityFromTokenConfig{
			SubjectPath: oauth2.IdentityFromToken.SubjectPath,
			NamePath:    oauth2.IdentityFromToken.NamePath,
			EmailPath:   oauth2.IdentityFromToken.EmailPath,
		}
	}

	return cfg, nil
}

// resolveSecret reads a secret from file or environment variable.
// File takes precedence over env var. Returns an error if file is specified but
// unreadable, or if envVar is specified but not set. Returns empty string with
// no error if neither file nor envVar is specified.
func resolveSecret(file, envVar string) (string, error) {
	if file != "" {
		// #nosec G304 - file path is from configuration, not user input
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("failed to read secret file %q: %w", file, err)
		}
		return string(bytes.TrimSpace(data)), nil
	}
	if envVar != "" {
		value := os.Getenv(envVar)
		if value == "" {
			return "", fmt.Errorf("environment variable %q is not set", envVar)
		}
		return value, nil
	}
	slog.Debug("no client secret configured (neither file nor env var specified)")
	return "", nil
}

// convertUserInfoConfig converts UserInfoRunConfig to upstream.UserInfoConfig.
func convertUserInfoConfig(rc *authserver.UserInfoRunConfig) *upstream.UserInfoConfig {
	if rc == nil {
		return nil
	}
	return &upstream.UserInfoConfig{
		EndpointURL:       rc.EndpointURL,
		HTTPMethod:        rc.HTTPMethod,
		AdditionalHeaders: rc.AdditionalHeaders,
		FieldMapping:      convertFieldMapping(rc.FieldMapping),
	}
}

// convertFieldMapping converts UserInfoFieldMappingRunConfig to upstream.UserInfoFieldMapping.
func convertFieldMapping(rc *authserver.UserInfoFieldMappingRunConfig) *upstream.UserInfoFieldMapping {
	if rc == nil {
		return nil
	}
	return &upstream.UserInfoFieldMapping{
		SubjectFields: rc.SubjectFields,
		NameFields:    rc.NameFields,
		EmailFields:   rc.EmailFields,
	}
}

// createStorage creates the appropriate storage backend based on configuration.
func createStorage(ctx context.Context, cfg *storage.RunConfig) (storage.Storage, error) {
	if cfg == nil || cfg.Type == "" || cfg.Type == string(storage.TypeMemory) {
		return storage.NewMemoryStorage(), nil
	}
	if cfg.Type == string(storage.TypeRedis) {
		redisCfg, err := convertRedisRunConfig(cfg.RedisConfig)
		if err != nil {
			return nil, fmt.Errorf("invalid Redis config: %w", err)
		}
		kr, err := resolveTokenEncryptionKeyring(cfg.RedisConfig.TokenEncryption)
		if err != nil {
			return nil, fmt.Errorf("invalid Redis token encryption config: %w", err)
		}
		var opts []storage.RedisStorageOption
		if kr != nil {
			opts = append(opts, storage.WithTokenEncryption(kr))
		}
		return storage.NewRedisStorage(ctx, redisCfg, cfg.RedisConfig.KeyPrefix, opts...)
	}
	return nil, fmt.Errorf("unsupported storage type: %s", cfg.Type)
}

// resolveTokenEncryptionKeyring builds the upstream-token encryption keyring
// from the serializable config. A nil config returns a nil keyring (encryption
// disabled). Otherwise every referenced env var is resolved and decoded, and
// the keyring is validated — any failure is fatal to startup so a
// misconfigured deployment can never silently degrade to plaintext.
// Key material is never logged.
func resolveTokenEncryptionKeyring(rc *storage.TokenEncryptionRunConfig) (tokenenc.Keyring, error) {
	if rc == nil {
		return nil, nil
	}

	kekByID := make(map[string][]byte, len(rc.Keys))
	for id, envVar := range rc.Keys {
		if envVar == "" {
			return nil, fmt.Errorf("token encryption: key %q has no environment variable name", id)
		}
		value := os.Getenv(envVar)
		if value == "" {
			return nil, fmt.Errorf("token encryption: environment variable %q for key %q is not set", envVar, id)
		}
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("token encryption: key %q is not valid base64: %w", id, err)
		}
		kekByID[id] = key
	}

	kr, err := tokenenc.NewStaticKeyring(rc.ActiveKeyID, kekByID)
	if err != nil {
		return nil, err
	}
	return kr, nil
}

// convertRedisRunConfig converts a serializable RedisRunConfig to a runtime
// tcredis.Config. It resolves ACL credentials from environment variables and
// parses duration strings. Connection-mode topology and defaulting are handled
// by the shared toolhive-core redis package when the client is constructed.
func convertRedisRunConfig(rc *storage.RedisRunConfig) (tcredis.Config, error) {
	if rc == nil {
		return tcredis.Config{}, fmt.Errorf("redis config is required when storage type is redis")
	}

	cfg := tcredis.Config{
		Addr:        rc.Addr,
		ClusterMode: rc.ClusterMode,
	}

	if rc.SentinelConfig != nil {
		cfg.SentinelConfig = &tcredis.SentinelConfig{
			MasterName:    rc.SentinelConfig.MasterName,
			SentinelAddrs: rc.SentinelConfig.SentinelAddrs,
		}
		cfg.DB = rc.SentinelConfig.DB
	}

	acl, err := convertRedisACLConfig(rc.ACLUserConfig)
	if err != nil {
		return tcredis.Config{}, fmt.Errorf("failed to convert ACL config: %w", err)
	}
	cfg.Username = acl.username
	cfg.Password = acl.password

	if err := applyRedisTimeouts(rc, &cfg); err != nil {
		return tcredis.Config{}, fmt.Errorf("failed to apply redis timeouts: %w", err)
	}

	tlsCfg, err := convertRedisTLSRunConfig(rc.TLS)
	if err != nil {
		return tcredis.Config{}, fmt.Errorf("master TLS config: %w", err)
	}
	cfg.TLS = tlsCfg

	// SentinelTLS only applies in Sentinel mode
	if rc.SentinelConfig != nil {
		sentinelTLSCfg, err := convertRedisTLSRunConfig(rc.SentinelTLS)
		if err != nil {
			return tcredis.Config{}, fmt.Errorf("sentinel TLS config: %w", err)
		}
		cfg.SentinelTLS = sentinelTLSCfg
	}

	return cfg, nil
}

// redisACLCredentials carries resolved Redis ACL credentials between
// convertRedisACLConfig and its caller. Named fields prevent positional
// swaps of two same-typed strings at the call site.
type redisACLCredentials struct {
	username string
	password string
}

// convertRedisACLConfig resolves ACL user credentials from environment variables.
// When UsernameEnvVar is empty, no username is resolved; go-redis then sends
// HELLO with "default" as the username (or falls back to legacy AUTH <password>
// for servers that do not support HELLO). This is required for managed Redis
// tiers without ACL users (e.g. GCP Memorystore Basic/Standard HA, Azure Cache
// for Redis).
func convertRedisACLConfig(rc *storage.ACLUserRunConfig) (redisACLCredentials, error) {
	if rc == nil {
		return redisACLCredentials{}, fmt.Errorf("acl user config is required")
	}
	var username string
	if rc.UsernameEnvVar != "" {
		var err error
		username, err = resolveEnvVar(rc.UsernameEnvVar)
		if err != nil {
			return redisACLCredentials{}, fmt.Errorf("failed to resolve Redis username: %w", err)
		}
	}
	password, err := resolveEnvVar(rc.PasswordEnvVar)
	if err != nil {
		return redisACLCredentials{}, fmt.Errorf("failed to resolve Redis password: %w", err)
	}
	return redisACLCredentials{username: username, password: password}, nil
}

// applyRedisTimeouts parses and applies optional timeout duration strings to cfg.
func applyRedisTimeouts(rc *storage.RedisRunConfig, cfg *tcredis.Config) error {
	if rc.DialTimeout != "" {
		d, err := time.ParseDuration(rc.DialTimeout)
		if err != nil {
			return fmt.Errorf("invalid dial timeout: %w", err)
		}
		cfg.DialTimeout = d
	}
	if rc.ReadTimeout != "" {
		d, err := time.ParseDuration(rc.ReadTimeout)
		if err != nil {
			return fmt.Errorf("invalid read timeout: %w", err)
		}
		cfg.ReadTimeout = d
	}
	if rc.WriteTimeout != "" {
		d, err := time.ParseDuration(rc.WriteTimeout)
		if err != nil {
			return fmt.Errorf("invalid write timeout: %w", err)
		}
		cfg.WriteTimeout = d
	}
	return nil
}

// convertRedisTLSRunConfig converts a RedisTLSRunConfig to a runtime
// tcredis.TLSConfig. Returns an error if a CA cert file is configured but
// cannot be read — this is treated as a hard error because silently falling
// back to system CAs could mask a misconfiguration and cause confusing TLS
// failures downstream.
func convertRedisTLSRunConfig(rc *storage.RedisTLSRunConfig) (*tcredis.TLSConfig, error) {
	if rc == nil {
		return nil, nil
	}
	cfg := &tcredis.TLSConfig{
		InsecureSkipVerify: rc.InsecureSkipVerify,
	}
	if rc.CACertFile != "" {
		// #nosec G304 - file path is from configuration, not user input
		data, err := os.ReadFile(rc.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read Redis CA cert file %q: %w", rc.CACertFile, err)
		}
		cfg.CACert = data
	}
	return cfg, nil
}

// resolveCIMDConfig extracts CIMD settings from a CIMDRunConfig.
// Returns zero values when cfg is nil (CIMD disabled).
// The CacheFallbackTTL string is parsed to time.Duration; callers must ensure
// CIMDRunConfig.Validate() has already been called so the string is well-formed.
func resolveCIMDConfig(cfg *authserver.CIMDRunConfig) (enabled bool, cacheMaxSize int, cacheFallbackTTL time.Duration) {
	if cfg == nil {
		return false, 0, 0
	}
	var ttl time.Duration
	if cfg.CacheFallbackTTL != "" {
		var err error
		ttl, err = time.ParseDuration(cfg.CacheFallbackTTL)
		if err != nil {
			// Should not happen when called after CIMDRunConfig.Validate().
			slog.Warn("invalid cimd cache_fallback_ttl, zero will be replaced by default",
				"value", cfg.CacheFallbackTTL, "err", err)
		}
	}
	return cfg.Enabled, cfg.CacheMaxSize, ttl
}

// resolveEnvVar reads a value from the named environment variable.
// An empty value is returned without error — empty credentials are valid for
// unauthenticated backends (e.g. a no-auth Redis where the operator injects a
// blank password from a Kubernetes Secret).
func resolveEnvVar(envVar string) (string, error) {
	if envVar == "" {
		return "", fmt.Errorf("environment variable name is empty")
	}
	value, ok := os.LookupEnv(envVar)
	if !ok {
		return "", fmt.Errorf("environment variable %q is not set", envVar)
	}
	return value, nil
}
