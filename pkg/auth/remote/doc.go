// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package remote provides authentication handling for remote MCP servers.
//
// This package implements OAuth/OIDC-based authentication with automatic
// discovery support for remote MCP servers. It handles:
//   - OAuth issuer discovery (RFC 8414)
//   - Protected resource metadata (RFC 9728)
//   - OAuth flow execution (PKCE-based)
//   - Token source creation for HTTP transports
//
// The main entry point is Handler.Authenticate() which takes a remote URL
// and performs all necessary discovery and authentication steps.
//
// Configuration is defined in pkg/runner.RemoteAuthConfig as part of the
// runner's RunConfig structure.
package remote
