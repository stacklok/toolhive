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
	handler     http.Handler
	storage     storage.Storage
	upstreamIDP idp.Provider
}

// newServer creates a new OAuth authorization server.
func newServer(ctx context.Context, cfg Config, stor storage.Storage) (*server, error) {
	// Apply defaults to config
	cfg.applyDefaults()

	// Validate config
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Validate storage is provided
	if stor == nil {
		return nil, fmt.Errorf("storage is required")
	}

	// Create OAuth2 config from authserver.Config
	oauthCfg := &oauth.AuthServerConfig{
		Issuer:               cfg.Issuer,
		AccessTokenLifespan:  cfg.AccessTokenLifespan,
		RefreshTokenLifespan: cfg.RefreshTokenLifespan,
		AuthCodeLifespan:     cfg.AuthCodeLifespan,
		HMACSecret:           cfg.HMACSecret,
		SigningKeyID:         cfg.SigningKey.KeyID,
		SigningKeyAlgorithm:  cfg.SigningKey.Algorithm,
		SigningKey:           cfg.SigningKey.Key,
	}
	oauth2Config, err := oauth.NewOAuth2ConfigFromAuthServerConfig(oauthCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 config: %w", err)
	}

	// Register clients
	registerClients(stor, cfg.Clients)

	// Create fosite provider
	provider := createProvider(oauth2Config, stor)

	// Validate upstream config is provided
	if cfg.Upstream == nil {
		return nil, fmt.Errorf("upstream IDP configuration is required")
	}

	// Create upstream IDP provider from config
	upstreamCfg := upstreamConfigToIDP(cfg.Upstream)
	upstreamIDP, err := idp.NewFromConfig(ctx, upstreamCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create upstream provider: %w", err)
	}

	router := oauth.NewRouter(slog.Default(), provider, oauth2Config, stor, upstreamIDP)

	// Create mux serving all endpoints
	mux := http.NewServeMux()
	router.Routes(mux)

	logger.Infof("OAuth authorization server configured with issuer: %s", cfg.Issuer)

	return &server{
		handler:     mux,
		storage:     stor,
		upstreamIDP: upstreamIDP,
	}, nil
}

// Handler returns the HTTP handler that serves all OAuth/OIDC endpoints.
func (s *server) Handler() http.Handler {
	return s.handler
}

// IDPTokenStorage returns the IDP token storage interface.
func (s *server) IDPTokenStorage() storage.IDPTokenStorage {
	return s.storage
}

// Close releases resources held by the server.
func (*server) Close() error {
	// Currently no resources to clean up
	return nil
}

// registerClients adds clients from config to storage.
func registerClients(stor storage.Storage, clients []ClientConfig) {
	for _, c := range clients {
		client := oauth.NewClient(oauth.ClientConfig{
			ID:           c.ID,
			Secret:       c.Secret,
			RedirectURIs: c.RedirectURIs,
			Public:       c.Public,
		})
		stor.RegisterClient(client)
	}
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
func createProvider(oauth2Config *oauth.OAuth2Config, stor storage.Storage) fosite.OAuth2Provider {
	// Convert go-jose/v4 JWK to go-jose/v3 JWK for fosite compatibility.
	// Fosite v0.49.0 depends on go-jose/v3, while we use v4 internally.
	// This ensures the "kid" (key ID) is included in JWT headers so resource
	// servers can look up the correct public key from our JWKS endpoint.
	signingKeyV4 := oauth2Config.SigningKey
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
		compose.NewOAuth2HMACStrategy(oauth2Config.Config),
		oauth2Config.Config,
	)

	// compose.Compose wires together all the pieces into an OAuth2Provider:
	// - Config: token lifespans, issuer URL, HMAC secret
	// - Storage: where to persist authorization codes, tokens, and client data
	// - Strategy: how to generate and validate tokens
	// - Factories: which OAuth grant types to enable (each adds handlers for specific flows)
	return compose.Compose(
		oauth2Config.Config,
		stor,
		&compose.CommonStrategy{CoreStrategy: jwtStrategy},
		compose.OAuth2AuthorizeExplicitFactory, // Authorization code grant
		compose.OAuth2RefreshTokenGrantFactory, // Refresh token grant
		compose.OAuth2PKCEFactory,              // PKCE for public clients
	)
}

// upstreamConfigToIDP converts authserver.UpstreamConfig to idp.UpstreamConfig.
// Returns nil if upstream is nil.
func upstreamConfigToIDP(upstream *UpstreamConfig) *idp.UpstreamConfig {
	if upstream == nil {
		return nil
	}
	return &idp.UpstreamConfig{
		Issuer:       upstream.Issuer,
		ClientID:     upstream.ClientID,
		ClientSecret: upstream.ClientSecret,
		Scopes:       upstream.Scopes,
		RedirectURI:  upstream.RedirectURI,
	}
}
