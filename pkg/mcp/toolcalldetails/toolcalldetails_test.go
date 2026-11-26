// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toolcalldetails

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	argsHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	toolCall = `{"type":"` + Type + `","server":"github","tool":"github_pull_request_read","arguments_sha256":"` + argsHash + `","operation_id":"op-1"}`
	listCall = `{"type":"` + Type + `","server":"github","method":"tools/list"}`
)

func newRegistry(t *testing.T) *authorizationdetails.Registry {
	t.Helper()
	types := map[string]authorizationdetails.Type{}
	Register(types)
	registry, err := authorizationdetails.New(types)
	require.NoError(t, err)
	return registry
}

func replace(entry, old, replacement string) string {
	return strings.Replace(entry, old, replacement, 1)
}

func TestRegister_Validation(t *testing.T) {
	t.Parallel()
	registry := newRegistry(t)
	exchange := func(tokenType string) authorizationdetails.Request {
		return authorizationdetails.Request{Exchange: &authorizationdetails.ExchangeContext{RequestedTokenType: tokenType}}
	}
	idJAG := exchange(oauthproto.TokenTypeIDJAG)
	redemption := authorizationdetails.Request{Redemption: &authorizationdetails.RedemptionContext{}}

	tests := []struct {
		name      string
		value     string
		grantType string
		// req is the hook request; on JWT-bearer rows it defaults to an ID-JAG
		// redemption unless bare is set.
		req    authorizationdetails.Request
		bare   bool
		code   authorizationdetails.ErrorCode
		reason string
	}{
		{name: "tool call on JWT-bearer", value: "[" + toolCall + "]"},
		{name: "list call on JWT-bearer", value: "[" + listCall + "]"},
		{
			name:      "tool call on ID-JAG token exchange",
			value:     "[" + toolCall + "]",
			grantType: oauthproto.GrantTypeTokenExchange,
			req:       idJAG,
		},
		{
			name:      "list call on ID-JAG token exchange",
			value:     "[" + listCall + "]",
			grantType: oauthproto.GrantTypeTokenExchange,
			req:       idJAG,
		},
		{
			name:      "token exchange for an access token",
			value:     "[" + toolCall + "]",
			grantType: oauthproto.GrantTypeTokenExchange,
			req:       exchange(oauthproto.TokenTypeAccessToken),
			code:      authorizationdetails.CodeInvalidRequest,
			reason:    ReasonIDJAGRequired,
		},
		{
			name:      "token exchange for a refresh token",
			value:     "[" + listCall + "]",
			grantType: oauthproto.GrantTypeTokenExchange,
			req:       exchange(oauthproto.TokenTypeRefreshToken),
			code:      authorizationdetails.CodeInvalidRequest,
			reason:    ReasonIDJAGRequired,
		},
		{
			name:      "token exchange without requested_token_type",
			value:     "[" + toolCall + "]",
			grantType: oauthproto.GrantTypeTokenExchange,
			req:       exchange(""),
			code:      authorizationdetails.CodeInvalidRequest,
			reason:    ReasonIDJAGRequired,
		},
		{
			name:      "token exchange without its exchange part",
			value:     "[" + toolCall + "]",
			grantType: oauthproto.GrantTypeTokenExchange,
			code:      authorizationdetails.CodeInvalidRequest,
			reason:    ReasonIDJAGRequired,
		},
		{
			name:   "JWT-bearer without its redemption part",
			value:  "[" + toolCall + "]",
			bare:   true,
			code:   authorizationdetails.CodeInvalidRequest,
			reason: ReasonIDJAGRequired,
		},
		{name: "second entry", value: "[" + listCall + "," + listCall + "]", reason: ReasonSingleEntry},
		{
			name:      "second entry on ID-JAG token exchange",
			value:     "[" + toolCall + "," + listCall + "]",
			grantType: oauthproto.GrantTypeTokenExchange,
			req:       idJAG,
			reason:    ReasonSingleEntry,
		},
		{
			name:   "unknown field",
			value:  "[" + replace(listCall, "}", `,"locations":["x"]}`) + "]",
			reason: ReasonMalformedEntry,
		},
		{
			name:   "upper-case hash",
			value:  "[" + replace(toolCall, argsHash, strings.ToUpper(argsHash)) + "]",
			reason: ReasonInvalidArgs,
		},
		{
			name:   "short hash",
			value:  "[" + replace(toolCall, argsHash, argsHash[:63]) + "]",
			reason: ReasonInvalidArgs,
		},
		{
			name:   "mixed shapes",
			value:  "[" + replace(toolCall, "}", `,"method":"tools/list"}`) + "]",
			reason: ReasonMalformedEntry,
		},
		{
			name:   "missing field",
			value:  "[" + replace(toolCall, `,"operation_id":"op-1"`, "") + "]",
			reason: ReasonMalformedEntry,
		},
		{
			name:   "server only",
			value:  `[{"type":"` + Type + `","server":"github"}]`,
			reason: ReasonMalformedEntry,
		},
		{
			name:   "empty string",
			value:  "[" + replace(toolCall, `"op-1"`, `""`) + "]",
			reason: ReasonMalformedEntry,
		},
		{
			name:   "list method outside the exact set",
			value:  "[" + replace(listCall, "tools/list", "tools/call") + "]",
			reason: ReasonInvalidMethod,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			grantType := tt.grantType
			req := tt.req
			if grantType == "" {
				grantType = oauthproto.GrantTypeJWTBearer
				if !tt.bare {
					req = redemption
				}
			}
			pending, err := registry.Parse(tt.value, grantType)
			require.NoError(t, err)
			granted, err := pending.Grant(context.Background(), req)
			if tt.reason == "" {
				require.NoError(t, err)
				assert.JSONEq(t, tt.value, string(granted), "the entry must be granted unchanged")
				return
			}
			code := tt.code
			if code == "" {
				code = authorizationdetails.CodeInvalidAuthorizationDetails
			}
			var refusal authorizationdetails.Error
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, code, refusal.Code)
			assert.Equal(t, tt.reason, refusal.Reason)
		})
	}
}

func TestRegister_GrantTypes(t *testing.T) {
	t.Parallel()
	types := map[string]authorizationdetails.Type{}
	Register(types)
	require.Contains(t, types, Type)
	assert.ElementsMatch(t,
		[]string{oauthproto.GrantTypeTokenExchange, authorizationdetails.IDJAGIssuance, oauthproto.GrantTypeJWTBearer},
		types[Type].GrantTypes)
}

// TestRegister_IDJAGPath proves the registration covers both sides of an
// ID-JAG through the registry, as the authorization server calls it, for a
// tool call and a list call alike: AS-1 parses the parameter with
// ParseIDJAGIssuance and keeps the entry through GrantSubset, and AS-2 parses
// the signed claim for the JWT-bearer grant and keeps it the same way.
func TestRegister_IDJAGPath(t *testing.T) {
	t.Parallel()
	registry := newRegistry(t)
	exchange := authorizationdetails.Request{
		Exchange: &authorizationdetails.ExchangeContext{RequestedTokenType: oauthproto.TokenTypeIDJAG},
	}
	redemption := authorizationdetails.Request{Redemption: &authorizationdetails.RedemptionContext{}}

	for _, entry := range []string{toolCall, listCall} {
		pending, err := registry.ParseIDJAGIssuance("[" + entry + "]")
		require.NoError(t, err)
		minted, err := pending.GrantSubset(context.Background(), exchange)
		require.NoError(t, err)
		assert.Equal(t, "["+entry+"]", string(minted.Entries))

		pending, err = registry.Parse(string(minted.Entries), oauthproto.GrantTypeJWTBearer)
		require.NoError(t, err)
		redeemed, err := pending.GrantSubset(context.Background(), redemption)
		require.NoError(t, err)
		assert.Equal(t, minted.Entries, redeemed.Entries)
	}
}

// TestIsListMethod checks that IsListMethod accepts exactly the methods a
// list-call entry may name.
func TestIsListMethod(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		"tools/list", "prompts/list", "resources/list", "resources/templates/list",
		"tools/call", "Tools/list", "tools/list ", "resources/templates/list/", "completion/complete", "",
	} {
		_, err := ParseEntry(json.RawMessage(replace(listCall, "tools/list", method)))
		assert.Equal(t, err == nil, IsListMethod(method), "%q", method)
	}
}

func TestParseEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    string
		want   Entry
		reason string
	}{
		{
			name: "tool call",
			raw:  toolCall,
			want: Entry{Server: "github", Tool: "github_pull_request_read", ArgumentsSHA256: argsHash, OperationID: "op-1"},
		},
		{
			name: "list call",
			raw:  listCall,
			want: Entry{Server: "github", Method: "tools/list"},
		},
		{
			name: "escaped strings are unescaped",
			raw:  replace(listCall, `"github"`, `"git\u0068ub"`),
			want: Entry{Server: "github", Method: "tools/list"},
		},
		{
			name: "escaped member name",
			raw:  replace(listCall, `"server"`, `"\u0073erver"`),
			want: Entry{Server: "github", Method: "tools/list"},
		},
		{
			name: "whitespace around tokens",
			raw:  " {\n \"type\" : \"" + Type + "\" ,\t\"server\":\"github\", \"method\": \"tools/list\" }\n",
			want: Entry{Server: "github", Method: "tools/list"},
		},
		{
			name: "prompts/list",
			raw:  replace(listCall, "tools/list", "prompts/list"),
			want: Entry{Server: "github", Method: "prompts/list"},
		},
		{
			name: "resources/list",
			raw:  replace(listCall, "tools/list", "resources/list"),
			want: Entry{Server: "github", Method: "resources/list"},
		},
		{
			name: "resources/templates/list",
			raw:  replace(listCall, "tools/list", "resources/templates/list"),
			want: Entry{Server: "github", Method: "resources/templates/list"},
		},
		{name: "method in other letter case", raw: replace(listCall, "tools/list", "Tools/list"), reason: ReasonInvalidMethod},
		{name: "method with trailing space", raw: replace(listCall, "tools/list", "tools/list "), reason: ReasonInvalidMethod},
		{name: "method with trailing slash", raw: replace(listCall, "tools/list", "resources/templates/list/"), reason: ReasonInvalidMethod},
		{name: "non-hex hash", raw: replace(toolCall, argsHash, strings.Repeat("g", 64)), reason: ReasonInvalidArgs},
		{name: "long hash", raw: replace(toolCall, argsHash, argsHash+"0"), reason: ReasonInvalidArgs},
		{name: "list call without server", raw: replace(listCall, `"server":"github",`, ""), reason: ReasonMalformedEntry},
		{name: "tool call without server", raw: replace(toolCall, `"server":"github",`, ""), reason: ReasonMalformedEntry},
		{name: "escaped repeated member", raw: replace(listCall, "}", `,"\u0073erver":"x"}`), reason: ReasonMalformedEntry},
		{name: "unpaired surrogate escape", raw: replace(listCall, "github", `git\ud800hub`), reason: ReasonMalformedEntry},
		{name: "literal replacement character", raw: replace(listCall, "github", "git\uFFFDhub"), reason: ReasonMalformedEntry},
		{name: "number member", raw: replace(listCall, `"github"`, "1"), reason: ReasonMalformedEntry},
		{name: "other type", raw: replace(listCall, Type, "https://example.com/other"), reason: ReasonMalformedEntry},
		{name: "missing type", raw: replace(listCall, `"type":"`+Type+`",`, ""), reason: ReasonMalformedEntry},
		{name: "repeated member", raw: replace(listCall, "}", `,"server":"other"}`), reason: ReasonMalformedEntry},
		{name: "member in other letter case", raw: replace(listCall, `"server"`, `"Server"`), reason: ReasonMalformedEntry},
		{name: "null member", raw: replace(listCall, `"github"`, "null"), reason: ReasonMalformedEntry},
		{name: "object member", raw: replace(listCall, `"github"`, `{"a":"b"}`), reason: ReasonMalformedEntry},
		{name: "trailing data", raw: listCall + `{}`, reason: ReasonMalformedEntry},
		{name: "not an object", raw: `["tools/list"]`, reason: ReasonMalformedEntry},
		{name: "invalid JSON", raw: `{"type":`, reason: ReasonMalformedEntry},
		{name: "member name not a string", raw: `{1:"x"}`, reason: ReasonMalformedEntry},
		{name: "object without closing brace", raw: strings.TrimSuffix(listCall, "}"), reason: ReasonMalformedEntry},
		{name: "object closed by a bracket", raw: strings.TrimSuffix(listCall, "}") + "]", reason: ReasonMalformedEntry},
		{name: "invalid UTF-8", raw: replace(listCall, "github", "git\xffhub"), reason: ReasonMalformedEntry},
		{name: "empty input", raw: ``, reason: ReasonMalformedEntry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseEntry(json.RawMessage(tt.raw))
			if tt.reason == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				assert.Equal(t, tt.want.Method != "", got.IsList())
				return
			}
			var refusal authorizationdetails.Error
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, authorizationdetails.CodeInvalidAuthorizationDetails, refusal.Code)
			assert.Equal(t, tt.reason, refusal.Reason)
			assert.Equal(t, Entry{}, got)
		})
	}
}
