// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// server is the internal implementation of the Server interface.
type server struct {
	handler     http.Handler
	storage     storage.Storage
	upstreamIDP upstream.OAuth2Provider
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

	slog.Debug("creating OAuth2 configuration")

	// Get signing key from KeyProvider
	signingKey, err := cfg.KeyProvider.SigningKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing key: %w", err)
	}

	// Create OAuth2 config from authserver.Config
	oauthParams := &oauthserver.AuthorizationServerParams{
		Issuer:               cfg.Issuer,
		AccessTokenLifespan:  cfg.AccessTokenLifespan,
		RefreshTokenLifespan: cfg.RefreshTokenLifespan,
		AuthCodeLifespan:     cfg.AuthCodeLifespan,
		HMACSecrets:          cfg.HMACSecrets,
		SigningKeyID:         signingKey.KeyID,
		SigningKeyAlgorithm:  signingKey.Algorithm,
		SigningKey:           signingKey.Key,
		ScopesSupported:      cfg.ScopesSupported,
		AllowedAudiences:     cfg.AllowedAudiences,
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

	// Create fosite provider
	slog.Debug("creating fosite OAuth2 provider")
	provider := createProvider(authServerConfig, stor)

	// Get upstream config
	upstreamCfg := cfg.GetUpstream()

	// Create upstream IDP provider using factory
	slog.Debug("creating upstream IDP provider", "type", upstreamCfg.Type, "name", upstreamCfg.Name)
	upstreamIDP, err := options.upstreamFactory(ctx, upstreamCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create upstream provider: %w", err)
	}
	slog.Debug("upstream IDP provider configured", "type", upstreamCfg.Type, "name", upstreamCfg.Name)

	handlerInstance := handlers.NewHandler(provider, authServerConfig, stor, upstreamIDP)

	// Create HTTP handler serving all endpoints
	router := handlerInstance.Routes()

	slog.Debug("oauth authorization server initialized",
		"issuer", cfg.Issuer,
	)

	return &server{
		handler:     router,
		storage:     stor,
		upstreamIDP: upstreamIDP,
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

// Close releases resources held by the server.
func (s *server) Close() error {
	slog.Debug("closing OAuth authorization server")
	return s.storage.Close()
}

// createProvider creates a fosite OAuth2Provider configured for the authorization code flow.
//
// Fosite is an OAuth 2.0 framework that implements the protocol details. The compose package
// provides a builder pattern to wire together configuration, storage, token strategies,
// and grant type handlers into a single OAuth2Provider that can handle all OAuth endpoints.
//
// The provider is configured with:
//   - JWT strategy for access tokens (asymmetric signing, distributed validation via JWKS)
//   - HMAC strategy for authorization codes and refresh tokens (symmetric, internal only)
//   - Authorization code grant (RFC 6749 Section 4.1)
//   - Refresh token grant (RFC 6749 Section 6)
//   - PKCE (RFC 7636) for public client security
func createProvider(authServerConfig *oauthserver.AuthorizationServerConfig, stor storage.Storage) fosite.OAuth2Provider {
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

	// compose.Compose wires together all the pieces into an OAuth2Provider:
	// - Config: token lifespans, issuer URL, HMAC secret
	// - Storage: where to persist authorization codes, tokens, and client data
	// - Strategy: how to generate and validate tokens
	// - Factories: which OAuth grant types to enable (each adds handlers for specific flows)
	return compose.Compose(
		authServerConfig.Config,
		stor,
		&compose.CommonStrategy{CoreStrategy: jwtStrategy},
		compose.OAuth2AuthorizeExplicitFactory, // Authorization code grant
		compose.OAuth2RefreshTokenGrantFactory, // Refresh token grant
		compose.OAuth2PKCEFactory,              // PKCE for public clients
	)
}
