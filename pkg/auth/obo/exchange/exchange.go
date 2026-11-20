// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package exchange defines the provider-agnostic contract for an on-behalf-of
// (OBO) token exchange: the Exchanger and TokenExchanger interfaces, the Token
// result, and the rule that collapses an audience/scopes pair into the single
// exchange target a provider receives.
//
// It is a stdlib-only leaf so that the provider (pkg/auth/obo/entra), the cache
// decorator (pkg/auth/obo/cache), the proxy middleware (pkg/auth/obo), and the
// vMCP strategy can all share it without an import cycle and without linking
// Kubernetes code into the proxyrunner.
package exchange

import (
	"context"
	"strings"
	"time"
)

// Exchanger swaps an incoming end-user token for a backend-scoped token,
// presented to the upstream MCP server on behalf of the user.
//
// The audience parameter is a provider-defined target identifier for the
// backend the token is minted for. The Entra implementation sends it verbatim
// as the OAuth scope (typically "<resource>/.default"). It is opaque to the
// cache decorator, which only uses it to partition entries. Callers derive it
// with Target so every surface sends the provider the same value for the same
// configuration.
type Exchanger interface {
	// Exchange returns the backend-scoped token to present to the upstream MCP
	// server on behalf of the user identified by userToken, for the given
	// audience, or an error.
	//
	// Implementations MUST NOT include the user token, the issued backend token,
	// or any provider secret in the returned error, so callers may safely log it.
	Exchange(ctx context.Context, userToken, audience string) (string, error)
}

// Token is the result of a single on-behalf-of exchange: the backend-scoped
// access token together with the instant it expires.
//
// A zero Expiry means the provider did not report a lifetime. Callers, and the
// caching layer in particular, MUST treat such a token as non-cacheable: there
// is no safe point at which to consider it stale, so it is used once and never
// stored.
type Token struct {
	// AccessToken is the backend-scoped bearer token to present to the upstream
	// MCP server on behalf of the user.
	AccessToken string

	// Expiry is the absolute instant the AccessToken stops being valid, derived
	// from the provider's expires_in. The zero value means "lifetime unknown".
	Expiry time.Time
}

// TokenExchanger is the provider-facing contract that backs Exchanger. A
// provider returns the exchanged Token (access token plus expiry) so that a
// caching decorator can honour the provider's expires_in. Exchanger hides the
// expiry because its callers only ever want a token that is valid right now.
//
// Adding a new identity provider means adding a new TokenExchanger
// implementation; the middleware, the vMCP strategy, and the cache decorator
// are unchanged.
type TokenExchanger interface {
	// ExchangeToken returns the backend-scoped Token for the user identified by
	// userToken and the given audience (see Exchanger), or an error.
	ExchangeToken(ctx context.Context, userToken, audience string) (Token, error)
}

// Target collapses a configuration's distinct audience and scopes fields onto
// the single value an Exchanger takes as its audience argument. Space-joined
// scopes win when present (the Entra-native form, e.g.
// "api://<backend>/.default"); otherwise the trimmed audience is used. An empty
// result means neither was usefully set.
//
// This is the one implementation of the collapse rule. The proxy middleware,
// the operator validation, the vMCP converter, and the vMCP strategy all call
// it, so identical configuration yields the same provider scope and the same
// cache partition on every surface.
func Target(audience string, scopes []string) string {
	if joined := strings.TrimSpace(strings.Join(scopes, " ")); joined != "" {
		return joined
	}
	return strings.TrimSpace(audience)
}
