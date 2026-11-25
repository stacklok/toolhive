// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	// e2eNumberType is a test type whose entries carry a number, which the
	// MCP call type does not allow.
	e2eNumberType = "https://example.com/e2e-number"
	// e2eToolCall is one MCP tool-call entry.
	e2eToolCall = `{"type":"` + toolcalldetails.Type + `","server":"github","tool":"github_pull_request_read",` +
		`"arguments_sha256":"` + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + `",` +
		`"operation_id":"op-1"}`
)

// e2eDetailsRegistry registers the MCP call type and e2eNumberType, both for
// token exchange and the JWT-bearer grant. e2eNumberType's hook keeps every
// entry.
func e2eDetailsRegistry(t *testing.T) *authorizationdetails.Registry {
	t.Helper()
	types := map[string]authorizationdetails.Type{
		e2eNumberType: {
			GrantTypes: []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer},
			Grant: func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
				return authorizationdetails.Result{Entries: req.Entries}, nil
			},
		},
	}
	toolcalldetails.Register(types)
	registry, err := authorizationdetails.New(types)
	require.NoError(t, err)
	return registry
}

// jwtDetailsClaim returns the raw bytes of a JWT's authorization_details
// claim, nil when the claim is absent. It does not verify the signature.
func jwtDetailsClaim(t *testing.T, token string) json.RawMessage {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &members))
	return members["authorization_details"]
}

// withDetails sets the authorization_details parameter of a request form.
func withDetails(values ...string) func(url.Values) {
	return func(form url.Values) { form["authorization_details"] = values }
}

// TestIDJAGDetailsAcrossTwoServers proves the end-to-end path: AS-1 mints an
// ID-JAG with the requested entries signed into its authorization_details
// claim, AS-2 redeems it with no parameter, and the access token carries the
// same entries byte for byte. One entry holds an integer above 2^53, which a
// float64 would round, and characters encoding/json escapes. The four copies
// (AS-1's response member, the ID-JAG claim, AS-2's response member and the
// access token's claim) are all the same bytes.
func TestIDJAGDetailsAcrossTwoServers(t *testing.T) {
	t.Parallel()
	h := startTwoServerIDJAGConfigured(t, []string{xrtIDJAGResource}, nil, nil,
		[]authserverrunner.EmbeddedAuthServerOption{authserverrunner.WithAuthorizationDetailsRegistry(e2eDetailsRegistry(t))},
		authserverrunner.WithAuthorizationDetailsRegistry(e2eDetailsRegistry(t)))

	// Sent with whitespace, which the signed form compacts.
	sent := `[ ` + e2eToolCall + `, {"type":"` + e2eNumberType + `", "n": 12345678901234567890123, "note":"a<b>&c"} ]`
	signed := `[` + e2eToolCall + `,{"type":"` + e2eNumberType + `","n":12345678901234567890123,` +
		`"note":"a` + "\\u003cb\\u003e\\u0026" + `c"}]`

	before := time.Now()
	grant := h.as1.idJAGGrant(t, h.as1.broker)
	minted := h.as1.requestIDJAGWith(t, h.as1.broker, grant, h.as2.issuer, withDetails(sent))
	require.Equal(t, http.StatusOK, minted.status, minted.body)
	idJAG := minted.str(t, "access_token")
	assert.Equal(t, signed, string(minted.details(t)), "AS-1 response member")
	assert.Equal(t, signed, string(jwtDetailsClaim(t, idJAG)), "ID-JAG claim")

	redeemed := h.as2.redeem(t, h.as2.client, idJAG)
	require.Equal(t, http.StatusOK, redeemed.status, redeemed.body)
	access := redeemed.str(t, "access_token")
	assert.Equal(t, signed, string(redeemed.details(t)), "AS-2 response member")
	assert.Equal(t, signed, string(jwtDetailsClaim(t, access)), "access token claim")

	// The verified access token decodes the big integer without rounding.
	claims := accessTokenClaims(t, h.as2.embedded, access)
	require.Contains(t, claims, "authorization_details")
	decoder := json.NewDecoder(bytes.NewReader(jwtDetailsClaim(t, access)))
	decoder.UseNumber()
	var entries []map[string]any
	require.NoError(t, decoder.Decode(&entries))
	require.Len(t, entries, 2)
	assert.Equal(t, json.Number("12345678901234567890123"), entries[1]["n"])

	// The entries add no audience, and the token lives no longer than the ID-JAG.
	assert.Equal(t, []any{xrtIDJAGResource}, claims["aud"])
	_, idJAGClaims := decodeIDJAG(t, idJAG)
	expiresIn, ok := redeemed.body["expires_in"].(float64)
	require.True(t, ok, "expires_in is a number: %v", redeemed.body["expires_in"])
	assert.LessOrEqual(t, int64(expiresIn), intClaim(t, idJAGClaims, "exp")-before.Unix()+1)
}

// TestIDJAGDetailsRefusalsAcrossTwoServers proves where each refusal of the
// end-to-end path happens: entries the MCP call type refuses are refused at
// AS-1, which mints nothing; a type AS-1 has no hook for is refused at AS-1;
// a redemption that still sends the parameter is refused at AS-2; and an AS-2
// without the type refuses the ID-JAG AS-1 signed instead of issuing an
// unrestricted token.
func TestIDJAGDetailsRefusalsAcrossTwoServers(t *testing.T) {
	t.Parallel()
	withRegistry := []authserverrunner.EmbeddedAuthServerOption{
		authserverrunner.WithAuthorizationDetailsRegistry(e2eDetailsRegistry(t)),
	}
	h := startTwoServerIDJAGConfigured(t, []string{xrtIDJAGResource}, nil, nil, withRegistry, withRegistry...)
	bare := startTwoServerIDJAGConfigured(t, []string{xrtIDJAGResource}, nil, nil, nil, withRegistry...)
	one := `[` + e2eToolCall + `]`

	// The steps share the servers and run in order: postToken derives the
	// client assertion's jti from the clock.
	for _, tc := range []struct{ name, details, code, desc string }{
		{"two call entries", `[` + e2eToolCall + `,` + e2eToolCall + `]`,
			"invalid_authorization_details", toolcalldetails.ReasonSingleEntry},
		{"an unknown member", `[{"type":"` + toolcalldetails.Type + `","server":"github","method":"tools/list","x":"y"}]`,
			"invalid_authorization_details", toolcalldetails.ReasonMalformedEntry},
		{"an unregistered type", `[{"type":"https://example.com/unknown"}]`,
			"invalid_authorization_details", "unknown_type"},
		{"the empty array", `[]`, "invalid_authorization_details", "empty_grant"},
	} {
		grant := h.as1.idJAGGrant(t, h.as1.broker)
		res := h.as1.requestIDJAGWith(t, h.as1.broker, grant, h.as2.issuer, withDetails(tc.details))
		requireError(t, res, tc.code, tc.desc)
		assert.NotContains(t, res.body, "access_token", tc.name)
	}

	// AS-2 refuses a redemption that still sends the parameter.
	grant := h.as1.idJAGGrant(t, h.as1.broker)
	minted := h.as1.requestIDJAGWith(t, h.as1.broker, grant, h.as2.issuer, withDetails(one))
	require.Equal(t, http.StatusOK, minted.status, minted.body)
	res := postToken(t, h.as2.srv.URL, h.as2.client, url.Values{
		"grant_type":            {oauthproto.GrantTypeJWTBearer},
		"assertion":             {minted.str(t, "access_token")},
		"resource":              {xrtIDJAGResource},
		"authorization_details": {one},
	})
	requireError(t, res, "invalid_authorization_details", "parameter_not_allowed")

	// An AS-2 without the type refuses the ID-JAG AS-1 signed.
	grant = bare.as1.idJAGGrant(t, bare.as1.broker)
	minted = bare.as1.requestIDJAGWith(t, bare.as1.broker, grant, bare.as2.issuer, withDetails(one))
	require.Equal(t, http.StatusOK, minted.status, minted.body)
	res = bare.as2.redeem(t, bare.as2.client, minted.str(t, "access_token"))
	requireError(t, res, "invalid_authorization_details", "unknown_type")
}
