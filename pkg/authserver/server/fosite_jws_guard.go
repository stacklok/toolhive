// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/ory/fosite"
)

// IsUnsafeFositeJWS reports whether token must be refused before fosite
// parses it.
//
// fosite v0.49.0 parses client assertions and OpenID Connect request objects
// with go-jose v3, which validates an RSA key embedded in a JWS header without
// bounding its size (GHSA-mgcc-cww5-fp2q; no patched v3 exists). An
// unauthenticated caller can use that to burn CPU before fosite looks up any
// client. fosite never uses an embedded key, since it resolves keys from the
// registered client, so refusing them does not affect conforming clients.
//
// The check mirrors how go-jose v3's ParseSigned reads its input: whitespace
// is stripped, input starting with "{" is the JSON serialization (whose
// unprotected headers can also carry a key), and the compact form must have
// exactly three segments. Anything that cannot be decoded as a compact JWS
// header is reported unsafe, so a parsing difference fails closed. fosite
// rejects such input anyway.
//
// Remove this once fosite moves to go-jose v4.1.6 or later.
func IsUnsafeFositeJWS(token string) bool {
	token = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, token)
	if strings.HasPrefix(token, "{") || strings.Count(token, ".") != 2 {
		return true
	}
	rawHeader, _, _ := strings.Cut(token, ".")
	headerJSON, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(rawHeader, "="))
	if err != nil {
		return true
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return true
	}
	for name := range header {
		if strings.EqualFold(name, "jwk") {
			return true
		}
	}
	return false
}

// newEmbeddedJWKGuardClientAuthenticationStrategy refuses client assertions
// that IsUnsafeFositeJWS flags before next, which may be fosite's default
// strategy, parses them. Every value of a repeated client_assertion is checked,
// since fosite reads the first and a later strategy might read another.
func newEmbeddedJWKGuardClientAuthenticationStrategy(
	next fosite.ClientAuthenticationStrategy,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		for _, assertion := range form["client_assertion"] {
			if IsUnsafeFositeJWS(assertion) {
				slog.Warn("rejected client assertion with an embedded key or non-compact encoding")
				return nil, fosite.ErrInvalidClient.WithHint(
					"The client_assertion must be a compact JWS without an embedded JSON Web Key.")
			}
		}
		return next(ctx, r, form)
	}
}
