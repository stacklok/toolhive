// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"

	"github.com/stacklok/toolhive/pkg/authserver/idp"
	"github.com/stacklok/toolhive/pkg/authserver/oauth"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/logger"
)

// server is the internal implementation of the Server interface.
type server struct {
	handler          http.Handler
	oauthHandler     http.Handler
	wellKnownHandler http.Handler
	storage          storage.Storage
	upstreamIDP      idp.Provider
}

// newServer creates a new OAuth authorization server.
func newServer(ctx context.Context, cfg Config, opts ...Option) (*server, error) {
	// Apply options
	o := &serverOptions{}
	for _, opt := range opts {
		opt(o)
	}

	// Apply defaults to config
	cfg.applyDefaults()

	// Validate config
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Create storage if not provided
	stor := o.storage
	if stor == nil {
		stor = storage.NewMemoryStorage()
	}

	// Convert generic Config to oauth.Config
	oauthCfg := toOAuthConfig(&cfg)

	// Create OAuth2 config and provider
	oauth2Config, err := oauth.NewOAuth2Config(oauthCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 config: %w", err)
	}

	// Register clients
	registerClients(stor, cfg.Clients)

	// Create fosite provider
	provider := createProvider(oauth2Config, stor)

	// Create router with optional upstream
	routerOpts, upstreamIDP, err := createRouterOpts(ctx, cfg.Upstream)
	if err != nil {
		return nil, err
	}

	router := oauth.NewRouter(slog.Default(), provider, oauth2Config, stor, routerOpts...)

	// Create combined mux serving all endpoints
	combinedMux := http.NewServeMux()
	router.Routes(combinedMux)

	// Create separate muxes for backward compatibility
	oauthMux := http.NewServeMux()
	router.OAuthRoutes(oauthMux)

	wellKnownMux := http.NewServeMux()
	router.WellKnownRoutes(wellKnownMux)

	logger.Infof("OAuth authorization server configured with issuer: %s", cfg.Issuer)

	return &server{
		handler:          combinedMux,
		oauthHandler:     oauthMux,
		wellKnownHandler: wellKnownMux,
		storage:          stor,
		upstreamIDP:      upstreamIDP,
	}, nil
}

// Handler returns the HTTP handler that serves all OAuth/OIDC endpoints.
func (s *server) Handler() http.Handler {
	return s.handler
}

// OAuthHandler returns the HTTP handler for OAuth endpoints only.
func (s *server) OAuthHandler() http.Handler {
	return s.oauthHandler
}

// WellKnownHandler returns the HTTP handler for well-known endpoints only.
func (s *server) WellKnownHandler() http.Handler {
	return s.wellKnownHandler
}

// IDPTokenStorage returns the IDP token storage interface.
func (s *server) IDPTokenStorage() IDPTokenStorage {
	return s.storage
}

// Close releases resources held by the server.
func (*server) Close() error {
	// Currently no resources to clean up
	return nil
}

// toOAuthConfig converts generic Config to oauth.Config.
func toOAuthConfig(cfg *Config) *oauth.Config {
	oauthCfg := &oauth.Config{
		Issuer:               cfg.Issuer,
		AccessTokenLifespan:  cfg.AccessTokenLifespan,
		RefreshTokenLifespan: cfg.RefreshTokenLifespan,
		AuthCodeLifespan:     cfg.AuthCodeLifespan,
		Secret:               cfg.HMACSecret,
		PrivateKeys: []oauth.PrivateKey{{
			KeyID:     cfg.SigningKey.KeyID,
			Algorithm: cfg.SigningKey.Algorithm,
			Key:       cfg.SigningKey.Key,
		}},
	}

	// Configure upstream if present
	if cfg.Upstream != nil {
		oauthCfg.Upstream = oauth.UpstreamConfig{
			Issuer:       cfg.Upstream.Issuer,
			ClientID:     cfg.Upstream.ClientID,
			ClientSecret: cfg.Upstream.ClientSecret,
			Scopes:       cfg.Upstream.Scopes,
			RedirectURI:  cfg.Upstream.RedirectURI,
		}
	}

	return oauthCfg
}

// registerClients adds clients from config to storage.
// Public clients are wrapped in LoopbackClient to support RFC 8252 Section 7.3
// compliant loopback redirect URI matching for native OAuth clients.
func registerClients(stor storage.Storage, clients []ClientConfig) {
	for _, c := range clients {
		defaultClient := &fosite.DefaultClient{
			ID:            c.ID,
			RedirectURIs:  c.RedirectURIs,
			ResponseTypes: []string{"code"},
			GrantTypes:    []string{"authorization_code", "refresh_token"},
			Scopes:        []string{"openid", "profile", "email"},
			Public:        c.Public,
		}
		if !c.Public && c.Secret != "" {
			defaultClient.Secret = []byte(c.Secret)
		}

		// Use LoopbackClient for public clients to support RFC 8252 Section 7.3
		// dynamic port matching for native app loopback redirect URIs.
		var client fosite.Client
		if c.Public {
			client = oauth.NewLoopbackClient(defaultClient)
		} else {
			client = defaultClient
		}
		stor.RegisterClient(client)
	}
}

// createProvider creates a fosite provider with JWT strategy.
func createProvider(oauth2Config *oauth.OAuth2Config, stor storage.Storage) fosite.OAuth2Provider {
	// Convert v4 JWK to v3 JWK for fosite compatibility.
	// Fosite v0.49.0 uses go-jose/v3, not v4.
	// This ensures the kid is included in the JWT header.
	signingKeyV4 := oauth2Config.SigningKey
	signingKeyV3 := &josev3.JSONWebKey{
		Key:       signingKeyV4.Key,
		KeyID:     signingKeyV4.KeyID,
		Algorithm: signingKeyV4.Algorithm,
		Use:       signingKeyV4.Use,
	}

	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (interface{}, error) { return signingKeyV3, nil },
		compose.NewOAuth2HMACStrategy(oauth2Config.Config),
		oauth2Config.Config,
	)

	return compose.Compose(
		oauth2Config.Config,
		stor,
		&compose.CommonStrategy{CoreStrategy: jwtStrategy},
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2PKCEFactory,
	)
}

// createRouterOpts creates router options, including upstream provider if configured.
func createRouterOpts(ctx context.Context, upstream *UpstreamConfig) ([]oauth.RouterOption, idp.Provider, error) {
	if upstream == nil || upstream.Issuer == "" {
		return nil, nil, nil
	}

	upstreamCfg := idp.Config{
		Issuer:       upstream.Issuer,
		ClientID:     upstream.ClientID,
		ClientSecret: upstream.ClientSecret,
		Scopes:       upstream.Scopes,
		RedirectURI:  upstream.RedirectURI,
	}

	upstreamProvider, err := idp.NewOIDCProvider(ctx, upstreamCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create upstream provider: %w", err)
	}

	return []oauth.RouterOption{oauth.WithIDPProvider(upstreamProvider)}, upstreamProvider, nil
}
