// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/go-jose/go-jose/v4"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/token/jwt"

	"github.com/stacklok/toolhive/pkg/authserver/internal/fositejose"
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

// Signer holds the server's signing key. It exposes Sign, for JWTs that set
// their own typ, and AccessTokenStrategy, for access tokens. It never hands out
// the raw jwt.Signer, whose Generate takes any header and whose Decode and
// Validate reach fosite's go-jose v3 parser.
type Signer struct{ raw jwt.Signer }

// NewSigner returns the Signer for key, a go-jose v4 key.
func NewSigner(key *jose.JSONWebKey) *Signer {
	// Convert go-jose/v4 JWK to go-jose/v3 JWK for fosite compatibility.
	// Fosite v0.49.0 depends on go-jose/v3, while we use v4 internally.
	// This ensures the "kid" (key ID) is included in JWT headers so resource
	// servers can look up the correct public key from our JWKS endpoint.
	keyV3 := fositejose.ToFositeKey(key)
	return &Signer{raw: &jwt.DefaultSigner{
		GetPrivateKey: func(_ context.Context) (interface{}, error) { return keyV3, nil },
	}}
}

// Sign signs claims with the given JOSE typ. The typ must not be empty, and must
// not be an access-token type ("at+jwt" or "JWT", compared case-insensitively):
// Sign shares the access-token key, so a token it signs must never be typed as
// an access token.
func (s *Signer) Sign(ctx context.Context, claims map[string]any, typ string) (string, error) {
	if s == nil || s.raw == nil {
		return "", errNoSigner
	}
	if typ == "" {
		return "", errors.New("authserver: Sign requires a non-empty typ")
	}
	if strings.EqualFold(typ, AccessTokenTypeATJWT) || strings.EqualFold(typ, "JWT") {
		return "", fmt.Errorf("authserver: Sign refuses the access-token typ %q", typ)
	}
	token, _, err := s.raw.Generate(ctx, jwt.MapClaims(claims), &jwt.Headers{Extra: map[string]any{"typ": typ}})
	return token, err
}

// AccessTokenStrategy returns fosite's JWT access-token strategy signed with
// this key, with fosite's HMAC strategy for authorization codes and refresh
// tokens. It returns the CoreStrategy interface so the strategy's signer is not
// reachable without a type assertion.
//
// When atJWT is true the signer is wrapped so access tokens get typ: at+jwt
// (RFC 9068) unless they set their own typ. The type is set at signing time,
// not in the session, because storage persists the session's JWT header and a
// per-session typ would survive a configuration change across refreshes. The
// wrapper signs only access tokens: auth codes and refresh tokens use the HMAC
// strategy, and no ID-token strategy is registered.
func (s *Signer) AccessTokenStrategy(config *fosite.Config, atJWT bool) (oauth2.CoreStrategy, error) {
	if s == nil || s.raw == nil {
		return nil, errNoSigner
	}
	signer := s.raw
	if atJWT {
		signer = NewATJWTAccessTokenSigner(signer)
	}
	return &oauth2.DefaultJWTStrategy{
		Signer:          signer,
		HMACSHAStrategy: compose.NewOAuth2HMACStrategy(config),
		Config:          config,
	}, nil
}

var errNoSigner = errors.New("authserver: Signer has no signing key; build it with NewSigner")
