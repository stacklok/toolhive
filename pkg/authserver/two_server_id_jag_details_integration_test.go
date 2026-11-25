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
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// The authorization server knows no type of its own, so these tests register
// test types. The MCP call type's registration is tested with the type, in
// pkg/mcp/toolcalldetails.
const (
	// e2eCallType is a test type whose hook keeps exactly one entry, like a
	// call-bound type.
	e2eCallType = "https://example.com/e2e-call"
	// e2eSingleEntry is the reason e2eCallType's hook refuses more than one
	// entry with.
	e2eSingleEntry = "single_entry_required"
	// e2eNumberType is a test type whose hook keeps every entry.
	e2eNumberType = "https://example.com/e2e-number"
	// e2eExchangeOnlyType is registered for token exchange and the JWT-bearer
	// grant but not opted into ID-JAG issuance.
	e2eExchangeOnlyType = "https://example.com/e2e-exchange-only"
	// e2eCall is one e2eCallType entry.
	e2eCall = `{"type":"` + e2eCallType + `","server":"github","tool":"github_pull_request_read",` +
		`"operation_id":"op-1"}`
)

// e2eDetailsRegistry registers e2eCallType and e2eNumberType, both opted into
// ID-JAG issuance and registered for the JWT-bearer grant, and
// e2eExchangeOnlyType without the opt-in.
func e2eDetailsRegistry(t *testing.T) *authorizationdetails.Registry {
	t.Helper()
	keep := func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
		return authorizationdetails.Result{Entries: req.Entries}, nil
	}
	single := func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
		if len(req.Entries) != 1 {
			return authorizationdetails.Result{}, authorizationdetails.Error{
				Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: e2eSingleEntry,
			}
		}
		return authorizationdetails.Result{Entries: req.Entries}, nil
	}
	idJAG := []string{authorizationdetails.IDJAGIssuance, oauthproto.GrantTypeJWTBearer}
	registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		e2eCallType:   {GrantTypes: idJAG, Grant: single},
		e2eNumberType: {GrantTypes: idJAG, Grant: keep},
		e2eExchangeOnlyType: {
			GrantTypes: []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer},
			Grant:      keep,
		},
	})
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
// claim, AS-2 redeems it with no parameter and keeps every entry, and the
// access token carries the signed bytes. One entry holds an integer above
// 2^53, which a float64 would round, and characters encoding/json escapes.
// The four copies (AS-1's response member, the ID-JAG claim, AS-2's response
// member and the access token's claim) are all the same bytes.
func TestIDJAGDetailsAcrossTwoServers(t *testing.T) {
	t.Parallel()
	h := startTwoServerIDJAGConfigured(t, []string{xrtIDJAGResource}, nil, nil,
		[]authserverrunner.EmbeddedAuthServerOption{authserverrunner.WithAuthorizationDetailsRegistry(e2eDetailsRegistry(t))},
		authserverrunner.WithAuthorizationDetailsRegistry(e2eDetailsRegistry(t)))

	// Sent with whitespace, which the signed form compacts.
	sent := `[ ` + e2eCall + `, {"type":"` + e2eNumberType + `", "n": 12345678901234567890123, ` +
		`"note":"a<b>&c` + "\u2028\u2029" + `"} ]`
	signed := `[` + e2eCall + `,{"type":"` + e2eNumberType + `","n":12345678901234567890123,` +
		`"note":"a` + "\\u003cb\\u003e\\u0026c\\u2028\\u2029" + `"}]`

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
// end-to-end path happens: entries a hook refuses are refused at AS-1, which
// mints nothing; a type AS-1 has no hook for is refused at AS-1;
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
	one := `[` + e2eCall + `]`

	// The steps share the servers and run in order: postToken derives the
	// client assertion's jti from the clock.
	for _, tc := range []struct{ name, details, code, desc string }{
		{"two call entries", `[` + e2eCall + `,` + e2eCall + `]`,
			"invalid_authorization_details", e2eSingleEntry},
		{"not a JSON array of objects", `[1]`, "invalid_authorization_details", "malformed_entry"},
		{"an unregistered type", `[{"type":"https://example.com/unknown"}]`,
			"invalid_authorization_details", "unknown_type"},
		{"a type not opted into ID-JAG issuance", `[{"type":"` + e2eExchangeOnlyType + `"}]`,
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

// TestIDJAGDetailsSizeAcrossTwoServers proves the size limit on the signed
// claim. encoding/json writes "<" as a six-byte escape, so entries of about
// 5.5 KiB in the request sign to the 32 KiB limit: AS-1 mints them and AS-2
// redeems the ID-JAG within its 64 KiB request body. One byte more and AS-1
// refuses with malformed_entry before minting.
func TestIDJAGDetailsSizeAcrossTwoServers(t *testing.T) {
	t.Parallel()
	withRegistry := []authserverrunner.EmbeddedAuthServerOption{
		authserverrunner.WithAuthorizationDetailsRegistry(e2eDetailsRegistry(t)),
	}
	h := startTwoServerIDJAGConfigured(t, []string{xrtIDJAGResource}, nil, nil, withRegistry, withRegistry...)

	const limit = 32 * 1024
	// entries returns an e2eNumberType entry whose signed form is size bytes,
	// mostly "<", and the request value, which is far smaller.
	entries := func(size int) (sent, signed string) {
		prefix, suffix := `[{"type":"`+e2eNumberType+`","note":"`, `"}]`
		room := size - len(prefix) - len(suffix)
		escaped, plain := room/6, room%6
		sent = prefix + strings.Repeat("<", escaped) + strings.Repeat("x", plain) + suffix
		signed = prefix + strings.Repeat(`\u003c`, escaped) + strings.Repeat("x", plain) + suffix
		require.Len(t, signed, size)
		return sent, signed
	}

	sent, signed := entries(limit)
	require.Less(t, len(sent), limit/5, "the request value is well within the parameter's 64 KiB")
	grant := h.as1.idJAGGrant(t, h.as1.broker)
	minted := h.as1.requestIDJAGWith(t, h.as1.broker, grant, h.as2.issuer, withDetails(sent))
	require.Equal(t, http.StatusOK, minted.status, minted.body)
	assert.Equal(t, signed, string(jwtDetailsClaim(t, minted.str(t, "access_token"))))
	redeemed := h.as2.redeem(t, h.as2.client, minted.str(t, "access_token"))
	require.Equal(t, http.StatusOK, redeemed.status, redeemed.body)
	assert.Equal(t, signed, string(jwtDetailsClaim(t, redeemed.str(t, "access_token"))))

	sent, _ = entries(limit + 1)
	grant = h.as1.idJAGGrant(t, h.as1.broker)
	res := h.as1.requestIDJAGWith(t, h.as1.broker, grant, h.as2.issuer, withDetails(sent))
	requireError(t, res, "invalid_authorization_details", "malformed_entry")
	assert.NotContains(t, res.body, "access_token")
}
