// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authserver provides a centralized OAuth 2.0 Authorization Server
// implementation using ory/fosite for issuing JWTs to clients.
//
// The auth server supports:
//   - OAuth 2.0 Authorization Code flow with PKCE (RFC 7636)
//   - Dynamic Client Registration (RFC 7591)
//   - Upstream IDP delegation (authenticates users via external IdP like Google, Okta)
//   - JWT access tokens with configurable lifespans
//   - OIDC discovery (/.well-known/openid-configuration)
//   - OAuth 2.0 Authorization Server Metadata (/.well-known/oauth-authorization-server, RFC 8414)
//
// # Usage
//
// The primary entry point is authserver.New(), which creates an OAuth authorization
// server with a single handler. Storage is a required parameter:
//
//	stor := storage.NewMemoryStorage()
//	server, err := authserver.New(ctx, cfg, stor)
//	if err != nil {
//	    return err
//	}
//	// Mount handler on your HTTP server (serves all OAuth/OIDC endpoints)
//	mux.Handle("/", server.Handler())
//
// # Configuration
//
// The server requires a Config struct with issuer URL, signing key configuration,
// upstream IDP settings, and allowed audiences. See the Config type for details.
//
//	cfg := authserver.Config{
//	    Issuer:           "https://auth.example.com",
//	    Upstreams:        []authserver.UpstreamConfig{{Config: upstreamCfg}},
//	    AllowedAudiences: []string{"https://mcp.example.com"},
//	}
//	stor := storage.NewMemoryStorage()
//	server, err := authserver.New(ctx, cfg, stor)
//
// # Storage
//
// The auth server requires a storage backend for tokens, authorization codes,
// and client registrations. Currently available:
//   - In-memory storage (suitable for single-instance deployments)
//
// Example with memory storage:
//
//	stor := storage.NewMemoryStorage()
//	server, err := authserver.New(ctx, cfg, stor)
//
// # IDP Token Storage
//
// When using upstream IDP delegation, tokens from the external IdP are stored
// and can be retrieved via the IDPTokenStorage interface for use by middleware
// (e.g., token swap middleware that replaces JWT auth with upstream tokens).
//
// # Subpackages
//
// The authserver package is organized into subpackages:
//   - server: HTTP handlers and OAuth server configuration
//   - storage: Token and authorization storage backends
//   - upstream: Upstream Identity Provider communication
package authserver
