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
	"github.com/stacklok/toolhive/pkg/networking"
)

// server is the internal implementation of the Server interface.
type server struct {
	handler   http.Handler
	storage   storage.Storage
	upstreams []handlers.NamedUpstream
	// refreshTokenLifespan mirrors the validated Config.RefreshTokenLifespan.
	// It is threaded into upstreamTokenRefresher so the refresh path can
	// re-anchor SessionExpiresAt for legacy storage rows missing that field.
	refreshTokenLifespan time.Duration
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
	case UpstreamProviderTypeOIDCTrust:
		if cfg.OIDCConfig == nil {
			return nil, fmt.Errorf("oidc_config is required for oidc-trust upstream")
		}
		return upstream.NewOIDCTrustProvider(cfg.OIDCConfig.Issuer, cfg.OIDCConfig.ClientID, cfg.OIDCConfig.CABundlePath), nil
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
		AllowedAudiences:             cfg.AllowedAudiences,
		AuthorizationEndpointBaseURL: cfg.AuthorizationEndpointBaseURL,
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
	// This must happen before factory creation so that oidc-trust providers
	// can be extracted to derive TrustedIssuers for the token exchange handler.
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

	// Derive trusted issuers (and their CA bundle paths, if any) from oidc-trust
	// upstreams for multi-issuer token exchange.
	var trustedIssuers []tokenexchange.TrustedIssuer
	var caBundlePath string
	for _, u := range upstreams {
		if tp, ok := u.Provider.(*upstream.OIDCTrustProvider); ok {
			trustedIssuers = append(trustedIssuers, tokenexchange.TrustedIssuer{
				IssuerURL:        tp.IssuerURL(),
				ExpectedAudience: tp.ExpectedAudience(),
			})
			if caBundlePath == "" && tp.CABundlePath() != "" {
				caBundlePath = tp.CABundlePath()
			}
		}
	}

	// Build HTTP client with CA trust for OIDC discovery/JWKS fetching.
	var httpClient *http.Client
	if caBundlePath != "" {
		var buildErr error
		httpClient, buildErr = networking.NewHttpClientBuilder().
			WithCABundle(caBundlePath).
			Build()
		if buildErr != nil {
			return nil, fmt.Errorf("failed to build HTTP client for token exchange: %w", buildErr)
		}
		slog.Debug("token exchange HTTP client configured with CA bundle",
			"ca_bundle", caBundlePath,
		)
	}

	// Build extra factories for extension grant types.
	extraFactories := []oauthserver.Factory{
		tokenexchange.Factory(tokenexchange.FactoryConfig{
			DelegationLifespan: cfg.DelegationTokenLifespan,
			TrustedIssuers:     trustedIssuers,
			HTTPClient:         httpClient,
		}),
	}

	// Create fosite provider
	slog.Debug("creating fosite OAuth2 provider")
	fositeProvider := createProvider(authServerConfig, stor, extraFactories...)


	// Run one-shot bulk migration of legacy data before handler construction.
	// TODO(migration): Remove once all deployments have upgraded past this version.
	if rs, ok := stor.(*storage.RedisStorage); ok {
		for i := range cfg.Upstreams {
			upCfg := &cfg.Upstreams[i]
			if err := rs.MigrateLegacyUpstreamData(ctx, upCfg.Name, string(upCfg.Type)); err != nil {
				return nil, fmt.Errorf("legacy data migration failed for upstream %q: %w", upCfg.Name, err)
			}
		}
	}

	handlerInstance, err := handlers.NewHandler(fositeProvider, authServerConfig, stor, upstreams)
	if err != nil {
		return nil, fmt.Errorf("failed to create handler: %w", err)
	}

	// Create HTTP handler serving all endpoints
	router := handlerInstance.Routes()

	slog.Debug("oauth authorization server initialized",
		"issuer", cfg.Issuer,
	)

	return &server{
		handler:              router,
		storage:              stor,
		upstreams:            upstreams,
		refreshTokenLifespan: cfg.RefreshTokenLifespan,
	}, nil
}

// Handler returns the HTTP handler that serves all OAuth/OIDC endpoints.
func (s *server) Handler() http.Handler {
	return s.handler
}

// IDPTokenStorage returns the IDP token storage interface.
func (s *server) IDPTokenStorage() storage.UpstreamTokenStorage {
	return s.storage
}

// UpstreamTokenRefresher returns a refresher that wraps the upstream providers
// and storage to transparently refresh expired upstream tokens. The refresher
// dispatches to the correct provider based on each token's ProviderID.
func (s *server) UpstreamTokenRefresher() storage.UpstreamTokenRefresher {
	if len(s.upstreams) == 0 {
		return nil
	}
	providers := make(map[string]upstream.OAuth2Provider, len(s.upstreams))
	for _, u := range s.upstreams {
		providers[u.Name] = u.Provider
	}
	return &upstreamTokenRefresher{
		providers:            providers,
		storage:              s.storage,
		refreshTokenLifespan: s.refreshTokenLifespan,
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
//   - Client credentials grant (RFC 6749 Section 4.4)
//   - Any extra factories passed in (e.g., RFC 8693 token exchange)
func createProvider(
	authServerConfig *oauthserver.AuthorizationServerConfig,
	stor storage.Storage,
	extraFactories ...oauthserver.Factory,
) fosite.OAuth2Provider {
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
		wrapComposeFactory(compose.OAuth2AuthorizeExplicitFactory),      // Authorization code grant
		wrapComposeFactory(compose.OAuth2RefreshTokenGrantFactory),      // Refresh token grant
		wrapComposeFactory(compose.OAuth2PKCEFactory),                   // PKCE for public clients
		wrapComposeFactory(compose.OAuth2ClientCredentialsGrantFactory), // Client credentials grant
	}
	factories = append(factories, extraFactories...)

	return oauthserver.NewAuthorizationServer(
		authServerConfig,
		stor,
		commonStrategy,
		factories...,
	)
}

// wrapComposeFactory adapts a compose.Factory to a server.Factory.
// Compose factories take (fosite.Configurator, interface{}, interface{}) while
// server factories take (*AuthorizationServerConfig, fosite.Storage, any).
// The embedded *fosite.Config satisfies fosite.Configurator.
func wrapComposeFactory(cf func(fosite.Configurator, interface{}, interface{}) interface{}) oauthserver.Factory {
	return func(config *oauthserver.AuthorizationServerConfig, storage fosite.Storage, strategy any) any {
		return cf(config.Config, storage, strategy)
	}
}
