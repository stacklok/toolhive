// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authserver provides an embeddable OAuth 2.0 Authorization Server
// implementation built on ory/fosite for issuing JWT access tokens to clients.
//
// The auth server supports:
//   - OAuth 2.0 Authorization Code flow with PKCE (RFC 7636) and rotating refresh tokens
//   - Opt-in Device Authorization Grant (RFC 8628)
//   - Dynamic Client Registration (RFC 7591) and Client ID Metadata Documents (CIMD)
//   - Upstream OIDC and OAuth 2.0 IDP delegation (including multi-upstream chains)
//   - Token Exchange (RFC 8693), JWT-Bearer (RFC 7523), and ID-JAG issuance/redemption
//   - SPIFFE workload authentication (JWT-SVID and X.509-SVID) for client_credentials and token exchange
//   - Asymmetric JWT access tokens published via JWKS (/.well-known/jwks.json)
//   - OAuth 2.0 Authorization Server Metadata (RFC 8414) and OIDC discovery compatibility
//
// # Usage
//
// The primary Go entry point is [New], which creates an OAuth authorization
// server with a single HTTP handler. Storage is required and borrowed on both
// success and failure. Stop and drain HTTP serving, close each server instance,
// then close storage after all users have stopped:
//
//	stor := storage.NewMemoryStorage()
//	defer stor.Close()
//
//	srv, err := authserver.New(ctx, cfg, stor)
//	if err != nil {
//	    return err
//	}
//	defer srv.Close()
//
//	// Mount OAuth and discovery endpoints explicitly so sibling well-known
//	// routes (such as /.well-known/oauth-protected-resource) stay reachable.
//	h := srv.Handler()
//	mux.Handle("/oauth/", h)
//	mux.Handle("/.well-known/oauth-authorization-server", h)
//	mux.Handle("/.well-known/oauth-authorization-server/", h)
//	mux.Handle("/.well-known/openid-configuration", h)
//	mux.Handle("/.well-known/openid-configuration/", h)
//	mux.Handle("/.well-known/jwks.json", h)
//
// # Configuration
//
// [Config] holds resolved Go configuration (issuer URL, signing key provider,
// upstream IDP settings, and required allowed audiences). Ephemeral signing keys
// and HMAC secrets are generated when omitted (development only). For
// YAML/JSON-driven deployments that resolve secrets from files or environment
// variables, see [RunConfig] and pkg/authserver/runner.
//
//	cfg := authserver.Config{
//	    Issuer:           "https://auth.example.com",
//	    AllowedAudiences: []string{"https://api.example.com"},
//	    Upstreams: []authserver.UpstreamConfig{{
//	        Name:       "corp",
//	        Type:       authserver.UpstreamProviderTypeOIDC,
//	        OIDCConfig: oidcCfg,
//	    }},
//	}
//
// # Storage
//
// The auth server requires a [storage.Storage] backend whose unwrapped store
// also implements [storage.DCRCredentialStore] (and optional capability
// interfaces for enabled features). Two backends are included:
//   - In-memory storage (storage.NewMemoryStorage) for development and single-instance deployments
//   - Redis storage (storage.NewRedisStorage) for multi-replica deployments (standalone, Cluster, Sentinel)
//
// # IDP Token Storage
//
// When using upstream IDP delegation, tokens from the external IdP are stored
// server-side and can be retrieved and refreshed via [Server.IDPTokenStorage]
// and [Server.UpstreamTokenRefresher].
//
// # Subpackages
//
// Key subpackages under pkg/authserver:
//   - runner: File/env-driven EmbeddedAuthServer and optional TLS listener
//   - storage: Storage interface, memory and Redis backends, and decorators
//   - upstream: Upstream OIDC and OAuth 2.0 provider clients
//   - upstreamfilter: Multi-upstream authorization chain filtering
//   - authorizationdetails: RFC 9396 authorization_details parsing and hooks
//   - server: Fosite provider wiring, keys, registration, handlers, and grant factories
package authserver
