// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

// TokenEndpointPath is the path of the token endpoint, relative to the issuer.
// Discovery advertises the issuer joined with it and the router serves it.
// Clients send client assertions to this URL but address them to the issuer:
// an assertion with this URL as its audience is refused.
const TokenEndpointPath = "/oauth/token" //nolint:gosec // G101: URL path, not a credential
