// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

// TokenEndpointPath is the path of the token endpoint, relative to the issuer.
// Discovery advertises the issuer joined with it, the router serves it, and
// client assertions may name that URL as their audience.
const TokenEndpointPath = "/oauth/token" //nolint:gosec // G101: URL path, not a credential
