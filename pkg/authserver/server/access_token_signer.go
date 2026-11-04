// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"maps"

	"github.com/ory/fosite/token/jwt"
)

var _ jwt.Signer = (*atJWTAccessTokenSigner)(nil)

// atJWTAccessTokenSigner types the tokens it signs as at+jwt (RFC 9068 §2.1)
// unless the caller has already set a typ.
type atJWTAccessTokenSigner struct {
	jwt.Signer
}

// NewATJWTAccessTokenSigner wraps signer so that tokens it generates carry the
// JOSE header typ: at+jwt, unless the caller's header already sets a typ. All
// other Signer methods delegate to signer.
func NewATJWTAccessTokenSigner(signer jwt.Signer) jwt.Signer {
	return &atJWTAccessTokenSigner{Signer: signer}
}

// Generate signs claims with a copy of header. The caller's header is never
// changed; the copy gets typ: at+jwt only if it has no typ key. A typ key is
// kept even when its value is empty.
func (s *atJWTAccessTokenSigner) Generate(ctx context.Context, claims jwt.MapClaims, header jwt.Mapper) (string, string, error) {
	// Copy the header: the session's stored header must not change.
	hdr := func() map[string]any {
		if header != nil {
			if m := maps.Clone(header.ToMap()); m != nil {
				return m
			}
		}
		return map[string]any{}
	}()
	if _, ok := hdr["typ"]; !ok {
		hdr["typ"] = AccessTokenTypeATJWT
	}
	return s.Signer.Generate(ctx, claims, &jwt.Headers{Extra: hdr})
}
