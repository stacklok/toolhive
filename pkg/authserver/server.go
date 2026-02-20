// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

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

	// Close releases resources held by the server.
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
