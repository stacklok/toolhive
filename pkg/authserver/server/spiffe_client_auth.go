// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/url"

	"github.com/ory/fosite"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

func newSPIFFEClientAuthenticationStrategy(
	defaultStrategy fosite.ClientAuthenticationStrategy,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		// An explicit assertion type takes precedence over an ambient mTLS identity.
		if form.Get("client_assertion_type") == spiffeauth.SPIFFEJWTAssertionType {
			return nil, fosite.ErrInvalidClient.WithHint("SPIFFE JWT client authentication is not implemented")
		}
		if _, ok := spiffeauth.SPIFFEIDFromContext(ctx); ok {
			return nil, fosite.ErrInvalidClient.WithHint("SPIFFE X.509 client authentication is not implemented")
		}
		return defaultStrategy(ctx, r, form)
	}
}
