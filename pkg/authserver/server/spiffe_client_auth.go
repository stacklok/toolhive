// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"github.com/ory/fosite"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

// SPIFFEClientResolver resolves a verified SPIFFE identity to its configured
// OAuth client. It is the seam that lets the client-authentication strategy
// here reach the association registry and storage constructed in package
// authserver, which this package cannot import (authserver imports server).
// One signature covers both X.509 and JWT credentials, with method as an
// explicit discriminator, so the two arms share a single resolution path
// instead of each inventing its own. spiffeID is passed explicitly rather
// than pulled from ctx, so an identity resolved from the wrong source cannot
// be mistaken for a verified one.
type SPIFFEClientResolver func(
	ctx context.Context, spiffeID, clientID string, method spiffeauth.SPIFFEAuthenticationMethod,
) (fosite.Client, error)

func newSPIFFEClientAuthenticationStrategy(
	defaultStrategy fosite.ClientAuthenticationStrategy,
	resolver SPIFFEClientResolver,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		// No SPIFFE trust configured: this server genuinely does not do SPIFFE,
		// so neither arm applies and every request goes to the default strategy.
		if resolver == nil {
			return defaultStrategy(ctx, r, form)
		}
		// An explicit assertion type takes precedence over an ambient mTLS identity.
		// A repeated form key must be checked in full: form.Get would only see the
		// first value, letting a SPIFFE assertion type hidden behind an earlier
		// value slip through to the default strategy.
		if slices.Contains(form["client_assertion_type"], spiffeauth.SPIFFEJWTAssertionType) {
			return nil, fosite.ErrInvalidClient.WithHint("SPIFFE JWT client authentication is not implemented")
		}
		if _, ok := spiffeauth.SPIFFEIDFromContext(ctx); ok {
			return nil, fosite.ErrInvalidClient.WithHint("SPIFFE X.509 client authentication is not implemented")
		}
		return defaultStrategy(ctx, r, form)
	}
}
