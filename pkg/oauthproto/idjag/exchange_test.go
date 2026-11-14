// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package idjag

import (
	"testing"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	testAudience = "https://as.example.com"
	testResource = "https://api.example.com/mcp"
)

// buildJWT returns a signed token that is only ever parsed unverified. A nil
// aud or resource value omits the claim.
func buildJWT(t *testing.T, typ string, aud, resource any) string {
	t.Helper()

	claims := jwt.MapClaims{"sub": "test"}
	if aud != nil {
		claims["aud"] = aud
	}
	if resource != nil {
		claims["resource"] = resource
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["typ"] = typ
	signed, err := token.SignedString([]byte("test-key-not-verified"))
	require.NoError(t, err)
	return signed
}

func exchangeToken(accessToken, tokenType, issuedType string) *oauth2.Token {
	tok := &oauth2.Token{AccessToken: accessToken, TokenType: tokenType}
	return tok.WithExtra(map[string]any{oauthproto.ParamIssuedTokenType: issuedType})
}

func TestFromExchange(t *testing.T) {
	t.Parallel()

	good := buildJWT(t, oauthproto.JWTTypeIDJAG, testAudience, testResource)

	tests := []struct {
		name      string
		tok       *oauth2.Token
		resource  string
		wantErr   string
		wantIs    error
		wantValue string
	}{
		{
			name:      "success",
			tok:       exchangeToken(good, "N_A", oauthproto.TokenTypeIDJAG),
			resource:  testResource,
			wantValue: good,
		},
		{
			name:      "token_type N_A matches case-insensitively",
			tok:       exchangeToken(good, "n_a", oauthproto.TokenTypeIDJAG),
			resource:  testResource,
			wantValue: good,
		},
		{
			name:    "nil token",
			tok:     nil,
			wantErr: "no token",
			wantIs:  ErrNoToken,
		},
		{
			name: "wrong issued_token_type",
			tok:  exchangeToken(good, "N_A", oauthproto.TokenTypeAccessToken),
			wantErr: `IdP returned issued_token_type="urn:ietf:params:oauth:token-type:access_token", want ` +
				`"urn:ietf:params:oauth:token-type:id-jag"`,
			wantIs: ErrIssuedTokenType,
		},
		{
			name:    "missing issued_token_type",
			tok:     &oauth2.Token{AccessToken: good, TokenType: "N_A"},
			wantErr: `IdP returned issued_token_type="", want "urn:ietf:params:oauth:token-type:id-jag"`,
			wantIs:  ErrIssuedTokenType,
		},
		{
			name:    "wrong token_type",
			tok:     exchangeToken(good, "Bearer", oauthproto.TokenTypeIDJAG),
			wantErr: `IdP returned token_type="Bearer", want N_A (draft §4.3.4)`,
			wantIs:  ErrTokenType,
		},
		{
			name: "both wrong reports issued_token_type first",
			tok:  exchangeToken(good, "Bearer", oauthproto.TokenTypeAccessToken),
			wantErr: `IdP returned issued_token_type="urn:ietf:params:oauth:token-type:access_token", want ` +
				`"urn:ietf:params:oauth:token-type:id-jag"`,
			wantIs: ErrIssuedTokenType,
		},
		{
			name:    "empty assertion",
			tok:     exchangeToken("", "N_A", oauthproto.TokenTypeIDJAG),
			wantErr: "IdP returned an empty ID-JAG assertion",
			wantIs:  ErrEmptyAssertion,
		},
		{
			name:    "assertion is not a JWT",
			tok:     exchangeToken("not-a-jwt", "N_A", oauthproto.TokenTypeIDJAG),
			wantErr: "ID-JAG assertion is not a valid JWT: token is malformed: token contains an invalid number of segments",
			wantIs:  ErrNotJWT,
		},
		{
			name:      "typ mismatch is accepted",
			tok:       exchangeToken(buildJWT(t, "JWT", testAudience, nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantValue: buildJWT(t, "JWT", testAudience, nil),
		},
		{
			name:      "aud string equal",
			tok:       exchangeToken(buildJWT(t, "JWT", testAudience, nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantValue: buildJWT(t, "JWT", testAudience, nil),
		},
		{
			name:    "aud string different",
			tok:     exchangeToken(buildJWT(t, "JWT", "https://other.example.com", nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantErr: `ID-JAG aud claim https://other.example.com does not match requested audience "https://as.example.com"`,
			wantIs:  ErrAudienceMismatch,
		},
		{
			name:      "aud one-element array equal",
			tok:       exchangeToken(buildJWT(t, "JWT", []any{testAudience}, nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantValue: buildJWT(t, "JWT", []any{testAudience}, nil),
		},
		{
			name:    "aud two-element array",
			tok:     exchangeToken(buildJWT(t, "JWT", []any{testAudience, "https://other.example.com"}, nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantErr: `ID-JAG aud claim [https://as.example.com https://other.example.com] does not match requested audience "https://as.example.com"`,
			wantIs:  ErrAudienceMismatch,
		},
		{
			name:    "aud empty array",
			tok:     exchangeToken(buildJWT(t, "JWT", []any{}, nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantErr: `ID-JAG aud claim [] does not match requested audience "https://as.example.com"`,
			wantIs:  ErrAudienceMismatch,
		},
		{
			name:    "aud array with a non-string element",
			tok:     exchangeToken(buildJWT(t, "JWT", []any{42}, nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantErr: `ID-JAG aud claim [42] does not match requested audience "https://as.example.com"`,
			wantIs:  ErrAudienceMismatch,
		},
		{
			name:    "aud number",
			tok:     exchangeToken(buildJWT(t, "JWT", 42, nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantErr: `ID-JAG aud claim 42 does not match requested audience "https://as.example.com"`,
			wantIs:  ErrAudienceMismatch,
		},
		{
			name:    "aud missing",
			tok:     exchangeToken(buildJWT(t, "JWT", nil, nil), "N_A", oauthproto.TokenTypeIDJAG),
			wantErr: `ID-JAG aud claim <nil> does not match requested audience "https://as.example.com"`,
			wantIs:  ErrAudienceMismatch,
		},
		{
			name:      "resource string equal",
			tok:       exchangeToken(good, "N_A", oauthproto.TokenTypeIDJAG),
			resource:  testResource,
			wantValue: good,
		},
		{
			name: "resource array containing the value",
			tok: exchangeToken(buildJWT(t, "JWT", testAudience, []any{"https://x.example.com", testResource}),
				"N_A", oauthproto.TokenTypeIDJAG),
			resource:  testResource,
			wantValue: buildJWT(t, "JWT", testAudience, []any{"https://x.example.com", testResource}),
		},
		{
			name: "resource array skips non-string elements",
			tok: exchangeToken(buildJWT(t, "JWT", testAudience, []any{42, nil, testResource}),
				"N_A", oauthproto.TokenTypeIDJAG),
			resource:  testResource,
			wantValue: buildJWT(t, "JWT", testAudience, []any{42, nil, testResource}),
		},
		{
			name: "resource string different",
			tok: exchangeToken(buildJWT(t, "JWT", testAudience, "https://x.example.com"),
				"N_A", oauthproto.TokenTypeIDJAG),
			resource: testResource,
			wantErr: `ID-JAG resource claim https://x.example.com does not contain requested resource ` +
				`"https://api.example.com/mcp"`,
			wantIs: ErrResourceMismatch,
		},
		{
			name:     "resource array without the value",
			tok:      exchangeToken(buildJWT(t, "JWT", testAudience, []any{42}), "N_A", oauthproto.TokenTypeIDJAG),
			resource: testResource,
			wantErr:  `ID-JAG resource claim [42] does not contain requested resource "https://api.example.com/mcp"`,
			wantIs:   ErrResourceMismatch,
		},
		{
			name:     "resource number",
			tok:      exchangeToken(buildJWT(t, "JWT", testAudience, 7), "N_A", oauthproto.TokenTypeIDJAG),
			resource: testResource,
			wantErr:  `ID-JAG resource claim 7 does not contain requested resource "https://api.example.com/mcp"`,
			wantIs:   ErrResourceMismatch,
		},
		{
			name:     "resource missing",
			tok:      exchangeToken(buildJWT(t, "JWT", testAudience, nil), "N_A", oauthproto.TokenTypeIDJAG),
			resource: testResource,
			wantErr:  `ID-JAG resource claim <nil> does not contain requested resource "https://api.example.com/mcp"`,
			wantIs:   ErrResourceMismatch,
		},
		{
			name: "resource not checked when none requested",
			tok: exchangeToken(buildJWT(t, "JWT", testAudience, "https://x.example.com"),
				"N_A", oauthproto.TokenTypeIDJAG),
			wantValue: buildJWT(t, "JWT", testAudience, "https://x.example.com"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := FromExchange(tt.tok, testAudience, tt.resource)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.wantValue, got)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
			assert.Empty(t, got)
			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}
