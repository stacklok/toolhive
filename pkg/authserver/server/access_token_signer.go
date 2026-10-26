// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"maps"

	"github.com/ory/fosite/token/jwt"
)

var _ jwt.Signer = (*atJWTAccessTokenSigner)(nil)

// atJWTAccessTokenSigner types every token it signs as at+jwt (RFC 9068 §2.1).
type atJWTAccessTokenSigner struct {
	jwt.Signer
}

// NewATJWTAccessTokenSigner wraps signer so every token it generates carries
// the JOSE header typ: at+jwt. All other Signer methods delegate to signer.
func NewATJWTAccessTokenSigner(signer jwt.Signer) jwt.Signer {
	return &atJWTAccessTokenSigner{Signer: signer}
}

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
	hdr[string(jwt.JWTHeaderType)] = AccessTokenTypeATJWT
	return s.Signer.Generate(ctx, claims, &jwt.Headers{Extra: hdr})
}
