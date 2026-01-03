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

// Option is a functional option for configuring the server.
type Option func(*serverOptions)

// serverOptions holds optional configuration for the server.
type serverOptions struct {
	storage storage.Storage
}

// WithStorage sets the storage backend for the server.
// If not provided, an in-memory storage is used.
func WithStorage(s storage.Storage) Option {
	return func(o *serverOptions) {
		o.storage = s
	}
}

// New creates a new OAuth authorization server.
// If no storage is provided via WithStorage, an in-memory storage is used.
func New(ctx context.Context, cfg Config, opts ...Option) (Server, error) {
	return newServer(ctx, cfg, opts...)
}
