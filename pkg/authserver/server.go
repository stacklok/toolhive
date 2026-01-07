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
	"net/http"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/logger"
)

// Server is the OAuth authorization server.
// It provides HTTP handlers that serve all OAuth/OIDC endpoints.
type Server interface {
	// Handler returns an http.Handler that serves all OAuth/OIDC endpoints:
	//   - /.well-known/openid-configuration (OIDC Discovery)
	//   - /.well-known/jwks.json (JSON Web Key Set)
	//   - /oauth/authorize (Authorization endpoint)
	//   - /oauth/token (Token endpoint)
	//   - /oauth/callback (Upstream IDP callback)
	//   - /oauth2/register (Dynamic Client Registration)
	//
	// The handler uses internal routing - the consumer doesn't need to know
	// about the endpoint structure.
	Handler() http.Handler

	// IDPTokenStorage returns storage for upstream IDP tokens.
	// Returns nil if no upstream IDP is configured.
	IDPTokenStorage() storage.IDPTokenStorage

	// Close releases resources held by the server.
	Close() error
}

// New creates a new OAuth authorization server.
// The storage parameter is required and determines where OAuth state is persisted.
// Use storage.NewMemoryStorage() for single-instance deployments or provide
// a distributed storage backend for production deployments.
func New(ctx context.Context, cfg Config, stor storage.Storage) (Server, error) {
	logger.Infow("creating new OAuth authorization server", "issuer", cfg.Issuer)
	return newServer(ctx, cfg, stor)
}
