// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package handlers provides HTTP handlers for the OAuth 2.0 authorization server endpoints.
//
// This package implements the HTTP layer for the authorization server, including:
//   - OIDC Discovery endpoint (/.well-known/openid-configuration)
//   - JWKS endpoint (/.well-known/jwks.json)
//   - OAuth endpoints (authorize, token, callback, register) - to be implemented
//
// The Handler struct coordinates all handlers and provides route registration methods
// for integrating with standard Go HTTP servers.
package handlers
