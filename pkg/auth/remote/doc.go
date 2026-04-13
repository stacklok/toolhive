// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package remote provides authentication handling for remote MCP servers,
// as well as general-purpose OAuth token source utilities used across the codebase.
//
// # Remote MCP server authentication
//
// Handler.Authenticate() is the main entry point: it takes a remote URL
// and performs all necessary discovery and authentication steps, including:
//   - OAuth issuer discovery (RFC 8414)
//   - Protected resource metadata (RFC 9728)
//   - OAuth flow execution (PKCE-based)
//   - Token source creation for HTTP transports
//
// Configuration is defined in pkg/runner.RemoteAuthConfig as part of the
// runner's RunConfig structure.
//
// # General-purpose token source utilities
//
// These types and functions are also used outside of remote MCP auth (e.g. registry auth):
//   - PersistingTokenSource / NewPersistingTokenSource — wraps an oauth2.TokenSource
//     and invokes a TokenPersister callback whenever tokens are refreshed.
//   - CreateTokenSourceFromCached — restores an oauth2.TokenSource from a cached
//     refresh token without requiring a new interactive flow.
//   - TokenPersistenceManager / NewTokenPersistenceManager — retrieves a cached
//     refresh token from a secrets provider and creates a token source from it.
package remote
