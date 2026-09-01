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
	jose "github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto"
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

func getAdditionalKeys(ctx context.Context, kp keys.KeyProvider, signingKeyID string) []jose.JSONWebKey {
	pubKeys, err := kp.PublicKeys(ctx)
	if err != nil {
		slog.Warn("failed to get public keys for JWKS, serving signing key only", "error", err)
		return nil
	}
	return additionalJWKs(signingKeyID, pubKeys)
}

func additionalJWKs(signingKeyID string, pubKeys []*keys.PublicKeyData) []jose.JSONWebKey {
	var additional []jose.JSONWebKey
	for _, pk := range pubKeys {
		if pk.KeyID == signingKeyID {
			continue
		}
		additional = append(additional, jose.JSONWebKey{
			Key:       pk.PublicKey,
			KeyID:     pk.KeyID,
			Algorithm: pk.Algorithm,
			Use:       "sig",
		})
	}
	return additional
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

	logConfidentialClientStartup(cfg.AllowConfidentialClientRegistration)

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

	if err := registerDelegateClients(ctx, stor, cfg.DelegateClients); err != nil {
		return nil, err
	}

	slog.Debug("creating OAuth2 configuration")

	// Get signing key from KeyProvider
	signingKey, err := cfg.KeyProvider.SigningKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing key: %w", err)
	}

	additionalKeys := getAdditionalKeys(ctx, cfg.KeyProvider, signingKey.KeyID)

	// Create OAuth2 config from authserver.Config
	oauthParams := &oauthserver.AuthorizationServerParams{
		Issuer:                              cfg.Issuer,
		AccessTokenLifespan:                 cfg.AccessTokenLifespan,
		RefreshTokenLifespan:                cfg.RefreshTokenLifespan,
		AuthCodeLifespan:                    cfg.AuthCodeLifespan,
		HMACSecrets:                         cfg.HMACSecrets,
		SigningKeyID:                        signingKey.KeyID,
		SigningKeyAlgorithm:                 signingKey.Algorithm,
		SigningKey:                          signingKey.Key,
		AdditionalKeys:                      additionalKeys,
		ScopesSupported:                     cfg.ScopesSupported,
		BaselineClientScopes:                cfg.BaselineClientScopes,
		AllowedAudiences:                    cfg.AllowedAudiences,
		AuthorizationEndpointBaseURL:        cfg.AuthorizationEndpointBaseURL,
		CIMDEnabled:                         cfg.CIMDEnabled,
		AllowConfidentialClientRegistration: cfg.AllowConfidentialClientRegistration,
		AllowPrivateKeyJWTRegistration:      cfg.AllowPrivateKeyJWTRegistration,
		HasStaticDelegateClients:            len(cfg.DelegateClients) > 0,
		ForceConfidentialRedirectURIs:       cfg.ForceConfidentialRedirectURIs,
		JWTBearerGrantEnabled:               JWTBearerGrantEnabled(cfg.TrustedIssuers),
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
	// ensuring both paths share one process-local singleflight.Group keyed by
	// opaque storage-row identity.
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

func registerDelegateClients(ctx context.Context, stor storage.Storage, delegateClients []DelegateClient) error {
	for _, delegateClient := range delegateClients {
		client, err := registration.NewStaticDelegateClient(registration.Config{
			ID:         delegateClient.ClientID,
			Secret:     delegateClient.ClientSecret,
			GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
			Scopes:     delegateClient.Scopes,
			Audience:   delegateClient.Audiences,
		})
		if err != nil {
			return fmt.Errorf("failed to create delegate client %q: %w", delegateClient.ClientID, err)
		}
		if err := stor.RegisterClient(ctx, client); err != nil {
			return fmt.Errorf("failed to register delegate client %q: %w", delegateClient.ClientID, err)
		}
		slog.Warn("delegate client has blanket self-issued token exchange rights: "+
			"it may exchange any user's self-issued ToolHive token for a delegated token, "+
			"with no per-subject binding to the client that originally obtained that token",
			"client_id", delegateClient.ClientID, "scopes", delegateClient.Scopes, "audiences", delegateClient.Audiences)
	}
	return nil
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

// JWTBearerGrantEnabled reports whether any trusted issuer has the RFC 7523
// JWT-bearer grant configured.
func JWTBearerGrantEnabled(trustedIssuers []tokenexchange.TrustedIssuer) bool {
	for _, issuer := range trustedIssuers {
		if issuer.JWTBearerGrant != nil {
			return true
		}
	}
	return false
}

// buildProvider assembles the fosite OAuth2 provider, registering the RFC 8693
// token-exchange handler as an extension grant alongside the standard grants.
func buildProvider(
	cfg Config, authServerConfig *oauthserver.AuthorizationServerConfig, stor storage.Storage,
) (fosite.OAuth2Provider, error) {
	delegateClientIDs := make([]string, len(cfg.DelegateClients))
	for i, c := range cfg.DelegateClients {
		delegateClientIDs[i] = c.ClientID
	}
	jwtBearerEnabled := JWTBearerGrantEnabled(cfg.TrustedIssuers)

	// Built once, up front, and handed to both factories below when the
	// JWT-bearer grant is also enabled: otherwise each factory would build
	// its own MultiIssuerTokenValidator over the same trusted issuers,
	// doubling every issuer's JWKS cache and background refresh goroutines
	// for no benefit. authServerConfig is the exact *AuthorizationServerConfig
	// each factory closure would otherwise receive at call time (see
	// createProvider/NewAuthorizationServer), so building it here first is
	// equivalent.
	var shared *tokenexchange.MultiIssuerTokenValidator
	if jwtBearerEnabled {
		var err error
		shared, err = tokenexchange.NewSharedTrustedIssuerValidator(authServerConfig, cfg.TrustedIssuers)
		if err != nil {
			return nil, fmt.Errorf("failed to create shared trusted-issuer validator: %w", err)
		}
	}

	tokenExchangeFactory, err := tokenexchange.FactoryWithSharedTrustedIssuerValidator(
		cfg.DelegationTokenLifespan, cfg.TrustedIssuers, delegateClientIDs, shared)
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange factory: %w", err)
	}
	if !jwtBearerEnabled {
		return createProvider(authServerConfig, stor, tokenExchangeFactory)
	}
	jwtBearerFactory, err := tokenexchange.JWTBearerIssuanceFactory(cfg.TrustedIssuers, shared)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT-bearer factory: %w", err)
	}
	return createProvider(authServerConfig, stor, tokenExchangeFactory, jwtBearerFactory)
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
// path use this instance, ensuring concurrent refreshes for the same logical
// upstream-token row are deduplicated by one process-local singleflight.Group.
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

// logConfidentialClientStartup emits an Info line naming the consequence of
// enabling confidential-client DCR. Combining it with cleartext HTTP is
// rejected by Config.Validate (see ValidateConfidentialClientTransport), so
// that combination can no longer reach this function.
func logConfidentialClientStartup(allowConfidentialClientRegistration bool) {
	if !allowConfidentialClientRegistration {
		return
	}
	slog.Info("confidential-client dynamic registration is enabled: " +
		"this server issues client secrets over unauthenticated dynamic registration")
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
