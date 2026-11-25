// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/internal/fositejose"
)

// newUnsafeJWSGuardClientAuthenticationStrategy refuses client assertions
// that fositejose.IsUnsafeJWS flags before next, which may be fosite's default
// strategy, parses them with go-jose v3. Every value of a repeated
// client_assertion is checked, since fosite reads the first and a later
// strategy might read another.
func newUnsafeJWSGuardClientAuthenticationStrategy(
	next fosite.ClientAuthenticationStrategy,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		for _, assertion := range form["client_assertion"] {
			if fositejose.IsUnsafeJWS(assertion) {
				slog.Warn("rejected client assertion that is not a compact JWS with a JSON header and payload and no embedded key")
				return nil, fosite.ErrInvalidClient.WithHint(
					"The client_assertion must be a compact JWS with a JSON header and payload and no embedded JSON Web Key.")
			}
		}
		return next(ctx, r, form)
	}
}
