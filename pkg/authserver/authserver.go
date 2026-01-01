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

// Package authserver provides a centralized OAuth 2.0 Authorization Server
// implementation using ory/fosite for issuing JWTs to clients.
//
// The auth server supports:
//   - OAuth 2.0 Authorization Code flow with PKCE (RFC 7636)
//   - Dynamic Client Registration (RFC 7591)
//   - Upstream IDP delegation (authenticates users via external IdP like Google, Okta)
//   - JWT access tokens with configurable lifespans
//   - OIDC discovery (/.well-known/openid-configuration)
//
// # Usage
//
// The primary entry point is authserver.New(), which creates an OAuth authorization
// server with a single handler:
//
//	server, err := authserver.New(ctx, cfg)
//	if err != nil {
//	    return err
//	}
//	// Mount handler on your HTTP server (serves all OAuth/OIDC endpoints)
//	mux.Handle("/", server.Handler())
//
// # Configuration
//
// For ToolHive deployments, use the runconfig subpackage to convert ToolHive-specific
// configuration (with file paths, env vars) to generic Config:
//
//	genericCfg, err := runconfig.BuildConfig(runCfg, proxyPort)
//	if err != nil {
//	    return err
//	}
//	server, err := authserver.New(ctx, *genericCfg)
//
// # Storage
//
// The auth server supports pluggable storage backends:
//   - In-memory storage (default, suitable for single-instance deployments)
//   - Redis storage (for distributed deployments)
//
// Use WithStorage option to provide a custom storage backend:
//
//	server, err := authserver.New(ctx, cfg, authserver.WithStorage(customStorage))
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
//   - idp: Upstream Identity Provider communication
//   - storage: Token and authorization storage backends
//   - oauth: OAuth protocol handlers and configuration
//   - runconfig: ToolHive-specific configuration with file paths and env vars
package authserver

import (
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// Type aliases for backward compatibility and convenience.
// External consumers can use these without importing subpackages directly.
type (
	// Storage is the interface for OAuth authorization storage.
	Storage = storage.Storage

	// IDPTokenStorage is the interface for IDP token storage operations.
	IDPTokenStorage = storage.IDPTokenStorage

	// IDPTokens represents tokens obtained from an upstream Identity Provider.
	IDPTokens = storage.IDPTokens

	// PendingAuthorization tracks a client's authorization request.
	PendingAuthorization = storage.PendingAuthorization
)
