// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package idjag

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	jwt "github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Sentinel errors returned by [FromExchange]. Use errors.Is to tell them apart.
var (
	ErrNoToken          = errors.New("no token")
	ErrIssuedTokenType  = errors.New("issued_token_type is not the ID-JAG type")
	ErrTokenType        = errors.New("token_type is not N_A")
	ErrEmptyAssertion   = errors.New("empty ID-JAG assertion")
	ErrNotJWT           = errors.New("ID-JAG assertion is not a valid JWT")
	ErrAudienceMismatch = errors.New("ID-JAG aud does not match the requested audience")
	ErrResourceMismatch = errors.New("ID-JAG resource does not contain the requested resource")
)

// FromExchange checks the token an RFC 8693 token exchange at the IdP returned
// for an ID-JAG request (draft 4.3.4) and returns the ID-JAG assertion. The
// token is the one a pkg/oauthproto/tokenexchange token source returns.
//
// The checks run in this order: issued_token_type is the ID-JAG type, token_type
// is N_A, the assertion is not empty, the assertion parses as a JWT, aud is
// exactly the requested audience, and, when resource is not empty, the resource
// claim contains it. A typ header that is not the ID-JAG type is logged and
// accepted. The JWT is parsed without verifying its signature: this is a check
// that the IdP honoured the request, not a trust decision.
//
// Error messages hold only the checked value and the wanted value, never the
// assertion.
func FromExchange(tok *oauth2.Token, audience, resource string) (string, error) {
	if tok == nil {
		return "", ErrNoToken
	}

	got, _ := tok.Extra(oauthproto.ParamIssuedTokenType).(string)
	if got != oauthproto.TokenTypeIDJAG {
		return "", newExchangeError(ErrIssuedTokenType,
			"IdP returned issued_token_type=%q, want %q", got, oauthproto.TokenTypeIDJAG)
	}

	if !strings.EqualFold(tok.TokenType, oauthproto.TokenTypeNotApplicable) {
		return "", newExchangeError(ErrTokenType,
			"IdP returned token_type=%q, want N_A (draft §4.3.4)", tok.TokenType)
	}

	assertion := tok.AccessToken
	if assertion == "" {
		return "", newExchangeError(ErrEmptyAssertion, "IdP returned an empty ID-JAG assertion")
	}

	parsed, _, err := jwt.NewParser().ParseUnverified(assertion, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotJWT, err)
	}
	if typ, _ := parsed.Header["typ"].(string); !IsIDJAG(typ) {
		slog.Debug("ID-JAG typ header is not oauth-id-jag+jwt",
			"got", typ, "want", oauthproto.JWTTypeIDJAG,
		)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("%w: claims are not a JSON object", ErrNotJWT)
	}

	if aud, err := claims.GetAudience(); err != nil || len(aud) != 1 || aud[0] != audience {
		return "", newExchangeError(ErrAudienceMismatch,
			"ID-JAG aud claim %v does not match requested audience %q", claims["aud"], audience)
	}

	// Draft 4.3.3: each granted requested resource MUST appear in the resource
	// claim exactly as sent, and the IdP MUST answer invalid_target if it
	// grants none. With one requested resource, this is that rule.
	if resource != "" && !claimContains(claims["resource"], resource) {
		return "", newExchangeError(ErrResourceMismatch,
			"ID-JAG resource claim %v does not contain requested resource %q", claims["resource"], resource)
	}

	return assertion, nil
}

// claimContains reports whether a string-or-array claim holds want. Array
// elements that are not strings are skipped.
func claimContains(claim any, want string) bool {
	switch v := claim.(type) {
	case string:
		return v == want
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// exchangeError carries a message and the sentinel it matches. Error returns
// the message alone.
type exchangeError struct {
	msg      string
	sentinel error
}

func (e *exchangeError) Error() string { return e.msg }
func (e *exchangeError) Unwrap() error { return e.sentinel }

func newExchangeError(sentinel error, format string, args ...any) error {
	return &exchangeError{msg: fmt.Sprintf(format, args...), sentinel: sentinel}
}
