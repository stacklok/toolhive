// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// UpstreamProviderFactory constructs the OAuth2Provider for one configured
// upstream IDP. Set it on Config.UpstreamFactory to own upstream construction;
// DefaultUpstreamFactory is the built-in implementation and can be delegated to.
type UpstreamProviderFactory func(ctx context.Context, cfg *UpstreamConfig) (upstream.OAuth2Provider, error)

// Server is the OAuth authorization server.
// It provides HTTP handlers that serve all OAuth/OIDC endpoints.
type Server interface {
	// Handler returns an http.Handler that serves all OAuth/OIDC endpoints:
	//   - /.well-known/openid-configuration (OIDC Discovery)
	//   - /.well-known/oauth-authorization-server (RFC 8414 OAuth AS Metadata)
	//   - /.well-known/jwks.json (JSON Web Key Set)
	//   - /oauth/authorize (Authorization endpoint)
	//   - /oauth/token (Token endpoint)
	//   - /oauth/callback (Upstream IDP callback)
	//   - /oauth/register (Dynamic Client Registration, RFC 7591)
	//
	// The handler uses internal routing - the consumer doesn't need to know
	// about the endpoint structure.
	Handler() http.Handler

	// IDPTokenStorage returns storage for upstream IDP tokens.
	// Returns nil if no upstream IDP is configured.
	IDPTokenStorage() storage.UpstreamTokenStorage

	// UpstreamTokenRefresher returns a refresher that can refresh expired upstream
	// tokens using the upstream provider's refresh token grant.
	// Returns nil if no upstream IDP is configured.
	UpstreamTokenRefresher() storage.UpstreamTokenRefresher

	// DCRStore returns the persistent DCR credential store the server is wired
	// against. This is the same DCRCredentialStore used by the upstream-DCR
	// resolver at boot, so callers can read RFC 7591 client registrations
	// without bypassing the storage backend the server itself reads from.
	//
	// SECURITY: the returned interface surfaces raw `client_secret` and
	// `registration_access_token` values. Callers MUST NOT log or render the
	// returned values; treat the handle the same way you would treat a
	// secrets manager client. Intended for admin / diagnostic code paths and
	// integration tests, not for general consumers.
	//
	// Lifecycle: the returned handle's lifetime is bound to Server.Close —
	// methods invoked after Close have backend-specific behavior (a
	// MemoryStorage continues to serve reads; a RedisStorage will error on
	// its closed connection pool).
	DCRStore() storage.DCRCredentialStore

	// CloseIdleConnections releases the idle keep-alive connections pooled by
	// the server's upstream IDP providers, without touching storage.
	//
	// Each upstream provider owns a private HTTP client whose connection pool
	// outlives the provider unless it is drained. An embedder that reconstructs
	// the server to change its upstream set — passing the same storage.Storage
	// and keys.KeyProvider so token and JWKS continuity is preserved — must
	// retire the superseded server with this method rather than Close, because
	// Close would also close the storage the new server is now serving through.
	//
	// Safe to call on a server that is still serving: only idle connections are
	// closed, in-flight requests are unaffected, and later upstream calls dial
	// again. Providers that do not expose the capability are skipped, as are
	// providers built around a caller-supplied HTTP client — the caller owns
	// that client's pool (see Config.UpstreamFactory).
	//
	// Scope: this releases the upstream providers' HTTP connection pools and
	// nothing else. A server configured with TrustedIssuers also holds one JWKS
	// refresh worker pool per issuer, started against context.Background() and
	// released by neither this method nor Close, so an embedder that
	// reconstructs repeatedly still grows goroutines through that path.
	//
	// Unlike upstream.IdleConnectionCloser, which is an optional capability an
	// OAuth2Provider may implement, this is a required part of the Server
	// interface: Server has a single in-repo implementation, so requiring it
	// keeps the call compile-time safe rather than a silent no-op, whereas
	// OAuth2Provider is documented as open to external implementations that
	// widening would break.
	CloseIdleConnections()

	// Close releases resources held by the server. It drains upstream idle
	// connections (see CloseIdleConnections) and then closes storage. Do not
	// call it on a server whose storage is shared with another live server.
	Close() error
}

// New creates a new OAuth authorization server.
// The storage parameter is required and determines where OAuth state is persisted.
// Use storage.NewMemoryStorage() for single-instance deployments or provide
// a distributed storage backend for production deployments.
func New(ctx context.Context, cfg Config, stor storage.Storage) (Server, error) {
	slog.Debug("creating new OAuth authorization server", "issuer", cfg.Issuer)
	return newServer(ctx, cfg, stor)
}
