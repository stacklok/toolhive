// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// server is the internal implementation of the Server interface.
type server struct {
	handler http.Handler
	// storage is the active storage backend, potentially wrapped by decorators
	// such as CIMDStorageDecorator. Code that needs the concrete type must walk
	// the Unwrap() chain rather than asserting directly.
	storage storage.Storage
	// dcrStore is the same storage.Storage value asserted to
	// storage.DCRCredentialStore. The assertion runs once at construction
	// (newServer) so DCRStore() is a field read rather than re-asserting on
	// every call, and a backend that does not implement DCRCredentialStore
	// is rejected at boot rather than at first DCR resolve.
	dcrStore storage.DCRCredentialStore
	// upstreamRefresher is the single shared refresher instance constructed in
	// newServer. Storing the interface type (not *upstreamTokenRefresher) keeps
	// the nil-return contract: newUpstreamTokenRefresher returns a true nil
	// interface when there are no upstreams, so callers can check == nil safely.
	upstreamRefresher storage.UpstreamTokenRefresher
	upstreams         []handlers.NamedUpstream
}

// upstreamProviderFactory creates an upstream OAuth2Provider from configuration.
// This type enables dependency injection for testing.
type upstreamProviderFactory func(ctx context.Context, cfg *UpstreamConfig) (upstream.OAuth2Provider, error)

// serverOption configures the server during construction.
type serverOption func(*serverOptions)

// serverOptions holds optional configuration for server creation.
type serverOptions struct {
	upstreamFactory upstreamProviderFactory
}

// defaultUpstreamFactory creates the production upstream provider based on type.
// For OIDC providers, it creates an OIDCProviderImpl with discovery and ID token validation.
// For OAuth2 providers, it creates a BaseOAuth2Provider.
func defaultUpstreamFactory(ctx context.Context, cfg *UpstreamConfig) (upstream.OAuth2Provider, error) {
	switch cfg.Type {
	case UpstreamProviderTypeOIDC:
		return upstream.NewOIDCProvider(ctx, cfg.OIDCConfig)
	case UpstreamProviderTypeOAuth2:
		return upstream.NewOAuth2Provider(cfg.OAuth2Config)
	default:
		return nil, fmt.Errorf("unsupported upstream type: %s", cfg.Type)
	}
}

// withUpstreamFactory sets a custom upstream provider factory.
// This is intended for testing and is not part of the public API.
func withUpstreamFactory(factory upstreamProviderFactory) serverOption {
	return func(o *serverOptions) {
		o.upstreamFactory = factory
	}
}

// newServer creates a new OAuth authorization server.
// The opts parameter allows injecting dependencies for testing.
func newServer(ctx context.Context, cfg Config, stor storage.Storage, opts ...serverOption) (*server, error) {
	slog.Debug("initializing OAuth authorization server")

	// Apply server options
	options := &serverOptions{
		upstreamFactory: defaultUpstreamFactory,
	}
	for _, opt := range opts {
		opt(options)
	}

	// Apply defaults to config
	if err := cfg.applyDefaults(); err != nil {
		return nil, fmt.Errorf("failed to apply config defaults: %w", err)
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Validate storage is provided
	if stor == nil {
		return nil, fmt.Errorf("storage is required")
	}

	// Storage no longer embeds DCRCredentialStore (the embed widened secret
	// reach to every Storage consumer); obtain the DCR-capable handle via an
	// explicit assertion at the boundary. The per-backend
	// `var _ DCRCredentialStore = (*MemoryStorage)(nil)` /
	// `var _ DCRCredentialStore = (*RedisStorage)(nil)` checks make this
	// provably safe for the production backends; surfacing a bad backend as
	// a constructor error keeps misconfiguration fail-loud at boot rather
	// than at first DCR resolve.
	baseStore := unwrapStorage(stor)
	dcrStore, ok := baseStore.(storage.DCRCredentialStore)
	if !ok {
		return nil, fmt.Errorf("storage backend %T does not implement storage.DCRCredentialStore", baseStore)
	}

	slog.Debug("creating OAuth2 configuration")

	// Get signing key from KeyProvider
	signingKey, err := cfg.KeyProvider.SigningKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing key: %w", err)
	}

	// Create OAuth2 config from authserver.Config
	oauthParams := &oauthserver.AuthorizationServerParams{
		Issuer:                       cfg.Issuer,
		AccessTokenLifespan:          cfg.AccessTokenLifespan,
		RefreshTokenLifespan:         cfg.RefreshTokenLifespan,
		AuthCodeLifespan:             cfg.AuthCodeLifespan,
		HMACSecrets:                  cfg.HMACSecrets,
		SigningKeyID:                 signingKey.KeyID,
		SigningKeyAlgorithm:          signingKey.Algorithm,
		SigningKey:                   signingKey.Key,
		ScopesSupported:              cfg.ScopesSupported,
		BaselineClientScopes:         cfg.BaselineClientScopes,
		AllowedAudiences:             cfg.AllowedAudiences,
		AuthorizationEndpointBaseURL: cfg.AuthorizationEndpointBaseURL,
		CIMDEnabled:                  cfg.CIMDEnabled,
	}
	authServerConfig, err := oauthserver.NewAuthorizationServerConfig(oauthParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 config: %w", err)
	}

	slog.Debug("oauth2 configuration created",
		"access_token_lifespan", cfg.AccessTokenLifespan,
		"refresh_token_lifespan", cfg.RefreshTokenLifespan,
		"auth_code_lifespan", cfg.AuthCodeLifespan,
	)

	// Build ordered upstream provider list from all configured upstreams.
	upstreams := make([]handlers.NamedUpstream, 0, len(cfg.Upstreams))
	for i := range cfg.Upstreams {
		upCfg := &cfg.Upstreams[i]
		slog.Debug("creating upstream IDP provider", "type", upCfg.Type, "name", upCfg.Name)
		upstreamProvider, upErr := options.upstreamFactory(ctx, upCfg)
		if upErr != nil {
			return nil, fmt.Errorf("failed to create upstream provider %q: %w", upCfg.Name, upErr)
		}
		upstreams = append(upstreams, handlers.NamedUpstream{
			Name:     upCfg.Name,
			Provider: upstreamProvider,
		})
		slog.Debug("upstream IDP provider configured", "type", upCfg.Type, "name", upCfg.Name)
	}

	// Run one-shot bulk migration of legacy data before handler construction.
	// TODO(migration): Remove once all deployments have upgraded past this version.
	if err := runLegacyMigration(ctx, stor, cfg.Upstreams); err != nil {
		return nil, err
	}

	// Wrap storage with the CIMD decorator before constructing the fosite provider
	// so that GetClient calls for HTTPS client_id values are intercepted at the
	// fosite level (not just the handler level).
	stor, err = decorateStorageForCIMD(cfg, stor)
	if err != nil {
		return nil, err
	}

	// Create fosite provider with the (possibly decorated) storage.
	slog.Debug("creating fosite OAuth2 provider")
	fositeProvider, err := buildProvider(cfg, authServerConfig, stor)
	if err != nil {
		return nil, fmt.Errorf("failed to create fosite OAuth2 provider: %w", err)
	}

	// Give the handler a refresher so the authorization chain can transparently
	// refresh an expired upstream leg during login instead of skipping it and
	// failing later at MCP-request token-swap time. The same instance is stored
	// on the server so UpstreamTokenRefresher() returns the identical object,
	// ensuring both paths share one singleflight.Group.
	refresher := newUpstreamTokenRefresher(upstreams, stor, cfg.RefreshTokenLifespan)
	handlerInstance, err := handlers.NewHandler(fositeProvider, authServerConfig, stor, upstreams,
		buildHandlerOptions(refresher, cfg.UpstreamFilter)...)
	if err != nil {
		return nil, fmt.Errorf("failed to create handler: %w", err)
	}

	// Create HTTP handler serving all endpoints
	router := handlerInstance.Routes()

	slog.Debug("oauth authorization server initialized",
		"issuer", cfg.Issuer,
	)

	return &server{
		handler:           router,
		storage:           stor,
		dcrStore:          dcrStore,
		upstreams:         upstreams,
		upstreamRefresher: refresher,
	}, nil
}

// decorateStorageForCIMD wraps stor with the CIMD decorator when CIMD is enabled,
// so GetClient calls for HTTPS client_id values are intercepted at the fosite
// level (not just the handler level). Returns stor unchanged when CIMD is disabled.
func decorateStorageForCIMD(cfg Config, stor storage.Storage) (storage.Storage, error) {
	if !cfg.CIMDEnabled {
		return stor, nil
	}
	if len(cfg.BaselineClientScopes) > 0 {
		slog.Warn("CIMD is enabled with baseline_client_scopes configured; "+
			"any third-party client resolved via CIMD will also receive these scopes — "+
			"ensure they are scopes you would grant by default to any unknown client",
			"baseline_client_scopes", cfg.BaselineClientScopes)
	}
	decorated, err := storage.NewCIMDStorageDecorator(stor, storage.CIMDDecoratorConfig{
		Enabled:              true,
		CacheMaxSize:         cfg.CIMDCacheMaxSize,
		FallbackTTL:          cfg.CIMDCacheFallbackTTL,
		ScopesSupported:      cfg.ScopesSupported,
		BaselineClientScopes: cfg.BaselineClientScopes,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize CIMD storage decorator: %w", err)
	}
	return decorated, nil
}

// buildProvider assembles the fosite OAuth2 provider, registering the RFC 8693
// token-exchange handler as an extension grant alongside the standard grants.
func buildProvider(
	cfg Config, authServerConfig *oauthserver.AuthorizationServerConfig, stor storage.Storage,
) (fosite.OAuth2Provider, error) {
	tokenExchangeFactory, err := tokenexchange.Factory(cfg.DelegationTokenLifespan)
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange factory: %w", err)
	}
	return createProvider(authServerConfig, stor, tokenExchangeFactory)
}

// buildHandlerOptions assembles the handlers.Option list for NewHandler: the
// refresher is always wired, and the filter is added only when the caller's
// Config sets one so a nil Config.UpstreamFilter preserves the pre-filter
// behavior of walking every configured upstream.
func buildHandlerOptions(refresher storage.UpstreamTokenRefresher, filter handlers.UpstreamFilter) []handlers.Option {
	opts := []handlers.Option{handlers.WithUpstreamRefresher(refresher)}
	if filter != nil {
		opts = append(opts, handlers.WithUpstreamFilter(filter))
	}
	return opts
}

// Handler returns the HTTP handler that serves all OAuth/OIDC endpoints.
func (s *server) Handler() http.Handler {
	return s.handler
}

// IDPTokenStorage returns the IDP token storage interface.
func (s *server) IDPTokenStorage() storage.UpstreamTokenStorage {
	return s.storage
}

// DCRStore returns the persistent DCR credential store the server is wired
// against. See the Server interface doc for SECURITY and lifecycle notes.
func (s *server) DCRStore() storage.DCRCredentialStore {
	return s.dcrStore
}

// UpstreamTokenRefresher returns the single shared refresher constructed in
// newServer. Both the handler's chain-walk path and the runtime token-swap
// path use this instance, ensuring concurrent refreshes for the same
// (session, provider) pair are deduplicated by a single singleflight.Group.
func (s *server) UpstreamTokenRefresher() storage.UpstreamTokenRefresher {
	return s.upstreamRefresher
}

// newUpstreamTokenRefresher builds a refresher over the given upstreams, or nil
// when there are none. Called once during newServer so the returned instance
// can be shared between the handler and the UpstreamTokenRefresher() accessor.
func newUpstreamTokenRefresher(
	upstreams []handlers.NamedUpstream,
	stor storage.UpstreamTokenStorage,
	refreshTokenLifespan time.Duration,
) storage.UpstreamTokenRefresher {
	if len(upstreams) == 0 {
		return nil
	}
	providers := make(map[string]upstream.OAuth2Provider, len(upstreams))
	for _, u := range upstreams {
		providers[u.Name] = u.Provider
	}
	return &upstreamTokenRefresher{
		providers:            providers,
		storage:              stor,
		refreshTokenLifespan: refreshTokenLifespan,
	}
}

// Close releases resources held by the server.
func (s *server) Close() error {
	slog.Debug("closing OAuth authorization server")
	return s.storage.Close()
}

// createProvider creates a fosite OAuth2Provider configured for the authorization code flow.
//
// Fosite is an OAuth 2.0 framework that implements the protocol details. We use
// server.NewAuthorizationServer which accepts server.Factory functions to register
// grant type handlers. The standard compose factories are wrapped via wrapComposeFactory
// and any extra factories (e.g., token exchange) are appended.
//
// The provider is configured with:
//   - JWT strategy for access tokens (asymmetric signing, distributed validation via JWKS)
//   - HMAC strategy for authorization codes and refresh tokens (symmetric, internal only)
//   - Authorization code grant (RFC 6749 Section 4.1)
//   - Refresh token grant (RFC 6749 Section 6)
//   - PKCE (RFC 7636) for public client security
//   - Any extra factories passed in (e.g., RFC 8693 token exchange)
func createProvider(
	authServerConfig *oauthserver.AuthorizationServerConfig,
	stor storage.Storage,
	extraFactories ...oauthserver.Factory,
) (fosite.OAuth2Provider, error) {
	slog.Debug("configuring fosite OAuth2 provider",
		"key_id", authServerConfig.SigningKey.KeyID,
		"algorithm", authServerConfig.SigningKey.Algorithm,
	)

	// Convert go-jose/v4 JWK to go-jose/v3 JWK for fosite compatibility.
	// Fosite v0.49.0 depends on go-jose/v3, while we use v4 internally.
	// This ensures the "kid" (key ID) is included in JWT headers so resource
	// servers can look up the correct public key from our JWKS endpoint.
	signingKeyV4 := authServerConfig.SigningKey
	signingKeyV3 := &josev3.JSONWebKey{
		Key:       signingKeyV4.Key,
		KeyID:     signingKeyV4.KeyID,
		Algorithm: signingKeyV4.Algorithm,
		Use:       signingKeyV4.Use,
	}

	// Create a composed token strategy:
	// - JWT strategy (outer): signs access tokens as JWTs using the asymmetric signing key
	// - HMAC strategy (inner): signs authorization codes and refresh tokens using HMACSecret
	//
	// Access tokens are JWTs so resource servers can validate them without calling us.
	// Auth codes and refresh tokens are opaque HMAC tokens since only we validate them.
	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (interface{}, error) { return signingKeyV3, nil },
		compose.NewOAuth2HMACStrategy(authServerConfig.Config),
		authServerConfig.Config,
	)

	commonStrategy := &compose.CommonStrategy{CoreStrategy: jwtStrategy}

	// Wrap fosite's compose factories to match server.Factory signature.
	factories := []oauthserver.Factory{
		wrapComposeFactory(compose.OAuth2AuthorizeExplicitFactory), // Authorization code grant
		wrapComposeFactory(compose.OAuth2RefreshTokenGrantFactory), // Refresh token grant
		wrapComposeFactory(compose.OAuth2PKCEFactory),              // PKCE for public clients
	}
	factories = append(factories, extraFactories...)

	return oauthserver.NewAuthorizationServer(
		authServerConfig,
		stor,
		commonStrategy,
		factories...,
	)
}

// unwrapStorage peels off one decorator layer if the storage implements
// Unwrap(), returning the concrete backend. Both newServer (DCRCredentialStore
// assertion) and runLegacyMigration (RedisStorage type assertion) need this.
func unwrapStorage(stor storage.Storage) storage.Storage {
	if unwrapper, ok := stor.(interface{ Unwrap() storage.Storage }); ok {
		return unwrapper.Unwrap()
	}
	return stor
}

// runLegacyMigration runs one-shot Redis data migrations before handlers are
// constructed. It is a no-op for non-Redis backends and passes through any
// decorator wrapping so the concrete type can be reached.
func runLegacyMigration(ctx context.Context, stor storage.Storage, upstreams []UpstreamConfig) error {
	base := unwrapStorage(stor)
	rs, ok := base.(*storage.RedisStorage)
	if !ok {
		return nil
	}
	for i := range upstreams {
		upCfg := &upstreams[i]
		if err := rs.MigrateLegacyUpstreamData(ctx, upCfg.Name, string(upCfg.Type)); err != nil {
			return fmt.Errorf("legacy data migration failed for upstream %q: %w", upCfg.Name, err)
		}
	}
	return nil
}

// wrapComposeFactory adapts a compose.Factory to a server.Factory.
// Compose factories take (fosite.Configurator, interface{}, interface{}) while
// server factories take (*AuthorizationServerConfig, fosite.Storage, any).
// The embedded *fosite.Config satisfies fosite.Configurator.
func wrapComposeFactory(cf compose.Factory) oauthserver.Factory {
	return func(config *oauthserver.AuthorizationServerConfig, storage fosite.Storage, strategy any) (any, error) {
		return cf(config.Config, storage, strategy), nil
	}
}
