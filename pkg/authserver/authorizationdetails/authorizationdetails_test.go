// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizationdetails

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	grantTypeExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	grantTypeOther    = "client_credentials"
)

func okGrant(context.Context, Request) (Result, error) {
	return Result{Entries: []json.RawMessage{json.RawMessage(`{"type":"a"}`)}}, nil
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		types   map[string]Type
		wantErr string
	}{
		{name: "nil map", types: nil},
		{name: "valid", types: map[string]Type{"a": {GrantTypes: []string{grantTypeExchange}, Grant: okGrant}}},
		{name: "empty name", types: map[string]Type{"": {GrantTypes: []string{grantTypeExchange}, Grant: okGrant}}, wantErr: "name"},
		{name: "whitespace name", types: map[string]Type{" \t": {GrantTypes: []string{grantTypeExchange}, Grant: okGrant}}, wantErr: "name"},
		{name: "nil grant", types: map[string]Type{"a": {GrantTypes: []string{grantTypeExchange}}}, wantErr: "Grant"},
		{name: "nil grant types", types: map[string]Type{"a": {Grant: okGrant}}, wantErr: "grant type"},
		{name: "empty grant types", types: map[string]Type{"a": {GrantTypes: []string{}, Grant: okGrant}}, wantErr: "grant type"},
		{name: "blank grant type", types: map[string]Type{"a": {GrantTypes: []string{" "}, Grant: okGrant}}, wantErr: "grant type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry, err := New(tt.types)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, registry)
		})
	}
}

func TestNew_CopiesInput(t *testing.T) {
	t.Parallel()

	grantTypes := []string{grantTypeExchange}
	types := map[string]Type{"a": {GrantTypes: grantTypes, Grant: okGrant}}
	registry, err := New(types)
	require.NoError(t, err)

	grantTypes[0] = grantTypeOther
	delete(types, "a")

	pending, err := registry.Parse(`[{"type":"a"}]`, grantTypeExchange)
	require.NoError(t, err)
	require.NotNil(t, pending)
}

func TestRegistry_Parse(t *testing.T) {
	t.Parallel()

	registry, err := New(map[string]Type{
		"a":     {GrantTypes: []string{grantTypeExchange}, Grant: okGrant},
		"b":     {GrantTypes: []string{grantTypeExchange, grantTypeOther}, Grant: okGrant},
		"other": {GrantTypes: []string{grantTypeOther}, Grant: okGrant},
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		raw       string
		grantType string
		wantErr   bool
	}{
		{name: "empty array", raw: `[]`},
		{name: "empty array with whitespace", raw: " \n[ ]\t"},
		{name: "single entry", raw: `[{"type":"a","x":1}]`},
		{name: "exactly 64 KiB", raw: paddedEntry(maxSize)},
		{name: "64 KiB plus one byte", raw: paddedEntry(maxSize + 1), wantErr: true},
		{name: "depth 64 through objects", raw: entryWithNested(nestObjects(maxDepth - 2))},
		{name: "depth 65 through objects", raw: entryWithNested(nestObjects(maxDepth - 1)), wantErr: true},
		{name: "depth 64 through mixed nesting", raw: entryWithNested(nestMixed(maxDepth - 2))},
		{name: "depth 65 through mixed nesting", raw: entryWithNested(nestMixed(maxDepth - 1)), wantErr: true},
		{name: "sibling objects reuse a key", raw: `[{"type":"a","x":{"k":1},"y":{"k":2}},{"type":"a","x":{"k":3}}]`},
		{name: "duplicate key in entry", raw: `[{"type":"a","x":1,"x":2}]`, wantErr: true},
		{name: "duplicate type key", raw: `[{"type":"a","type":"a"}]`, wantErr: true},
		{name: "duplicate key in nested object", raw: `[{"type":"a","x":{"k":1,"k":2}}]`, wantErr: true},
		{name: "duplicate key in object inside nested array", raw: `[{"type":"a","x":[{"k":1},{"k":1,"k":2}]}]`, wantErr: true},
		{name: "single escaped type key", raw: `[{"\u0074ype":"a"}]`},
		{name: "escaped-equivalent duplicate", raw: `[{"type":"a","\u0074ype":"a"}]`, wantErr: true},
		{name: "escaped-equivalent duplicate in nested object", raw: `[{"type":"a","x":{"value":1,"\u0076alue":2}}]`, wantErr: true},
		{name: "case-variant duplicate", raw: `[{"type":"a","Locations":1,"locations":2}]`, wantErr: true},
		{name: "long s folds to s", raw: `[{"type":"a","actions":1,"actionſ":2}]`, wantErr: true},
		{name: "final sigma folds to sigma", raw: `[{"type":"a","σ":1,"ς":2}]`, wantErr: true},
		{name: "capital sigma folds to final sigma", raw: `[{"type":"a","Σ":1,"ς":2}]`, wantErr: true},
		{name: "beta symbol folds to beta", raw: `[{"type":"a","β":1,"ϐ":2}]`, wantErr: true},
		{name: "distinct non-ASCII names are not duplicates", raw: `[{"type":"a","σ":1,"β":2}]`},
		{name: "case-variant duplicate of type", raw: `[{"type":"a","TYPE":"a"}]`, wantErr: true},
		{name: "case-variant name alone is not a duplicate", raw: `[{"type":"a","Locations":1}]`},
		{name: "type with only a different case", raw: `[{"Type":"a"}]`, wantErr: true},
		{name: "null", raw: `null`, wantErr: true},
		{name: "number", raw: `1`, wantErr: true},
		{name: "string", raw: `"a"`, wantErr: true},
		{name: "object", raw: `{"type":"a"}`, wantErr: true},
		{name: "trailing data", raw: `[{"type":"a"}] []`, wantErr: true},
		{name: "trailing garbage", raw: `[{"type":"a"}]x`, wantErr: true},
		{name: "empty string", raw: ``, wantErr: true},
		{name: "whitespace only", raw: " \t\n", wantErr: true},
		{name: "truncated", raw: `[{"type":"a"}`, wantErr: true},
		{name: "invalid UTF-8", raw: "[{\"type\":\"a\",\"x\":\"\xff\"}]", wantErr: true},
		{name: "null entry", raw: `[null]`, wantErr: true},
		{name: "scalar entry", raw: `["a"]`, wantErr: true},
		{name: "array entry", raw: `[[{"type":"a"}]]`, wantErr: true},
		{name: "missing type", raw: `[{"x":1}]`, wantErr: true},
		{name: "empty type", raw: `[{"type":""}]`, wantErr: true},
		{name: "non-string type", raw: `[{"type":1}]`, wantErr: true},
		{name: "null type", raw: `[{"type":null}]`, wantErr: true},
		{name: "unknown type", raw: `[{"type":"zzz"}]`, wantErr: true},
		{name: "unknown type after a valid entry", raw: `[{"type":"a"},{"type":"zzz"}]`, wantErr: true},
		{name: "type registered only for another grant type", raw: `[{"type":"other"}]`, wantErr: true},
		{name: "type registered for this and another grant type", raw: `[{"type":"b"}]`, grantType: grantTypeOther},
		{name: "type not registered for the given grant type", raw: `[{"type":"a"}]`, grantType: grantTypeOther, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			grantType := tt.grantType
			if grantType == "" {
				grantType = grantTypeExchange
			}
			pending, err := registry.Parse(tt.raw, grantType)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalid)
				require.Nil(t, pending)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, pending)
		})
	}
}

func TestPending_Grant(t *testing.T) {
	t.Parallel()

	const (
		raw         = `[{"type":"a","n":1},{"type":"b","n":2},{"type":"a","n":3}]`
		hugeEntry   = `{"type":"a","pad":"%s"}`
		wantAllCall = `[{"type":"a","n":3},{"type":"a","n":1},{"type":"b","n":2}]`
	)
	boom := errors.New("backend unavailable: secret-detail")
	reverse := func(req Request) (Result, error) {
		entries := make([]json.RawMessage, len(req.Entries))
		for i, entry := range req.Entries {
			entries[len(entries)-1-i] = entry
		}
		return Result{Entries: entries}, nil
	}
	echo := func(req Request) (Result, error) { return Result{Entries: req.Entries}, nil }
	returns := func(entries ...string) func(Request) (Result, error) {
		return func(Request) (Result, error) {
			out := make([]json.RawMessage, len(entries))
			for i, entry := range entries {
				out[i] = json.RawMessage(entry)
			}
			return Result{Entries: out}, nil
		}
	}
	fails := func(err error) func(Request) (Result, error) {
		return func(Request) (Result, error) { return Result{}, err }
	}

	tests := []struct {
		name      string
		raw       string
		hooks     map[string]func(Request) (Result, error)
		want      string
		wantCalls map[string][]string
		check     func(t *testing.T, err error)
	}{
		{
			name:      "interleaved types keep first-appearance order and hook order",
			raw:       raw,
			hooks:     map[string]func(Request) (Result, error){"a": reverse, "b": echo},
			want:      wantAllCall,
			wantCalls: map[string][]string{"a": {`{"type":"a","n":1}`, `{"type":"a","n":3}`}, "b": {`{"type":"b","n":2}`}},
		},
		{
			name:      "later type first in request is granted first",
			raw:       `[{"type":"b","n":2},{"type":"a","n":1}]`,
			hooks:     map[string]func(Request) (Result, error){"a": echo, "b": echo},
			want:      `[{"type":"b","n":2},{"type":"a","n":1}]`,
			wantCalls: map[string][]string{"a": {`{"type":"a","n":1}`}, "b": {`{"type":"b","n":2}`}},
		},
		{
			name:      "hook may grant more entries than requested",
			raw:       `[{"type":"a","n":1}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`{"type":"a","n":1}`, `{"type":"a","n":2}`)},
			want:      `[{"type":"a","n":1},{"type":"a","n":2}]`,
			wantCalls: map[string][]string{"a": {`{"type":"a","n":1}`}},
		},
		{
			name:  "empty request grants empty array without calling hooks",
			raw:   `[]`,
			hooks: map[string]func(Request) (Result, error){"a": fails(boom)},
			want:  `[]`,
		},
		{
			name:      "result is compacted",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns("{ \"type\" : \"a\" ,\n \"k\": [ 1 ] }")},
			want:      `[{"type":"a","k":[1]}]`,
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
		},
		{
			name:      "wrong type is rejected",
			raw:       raw,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`{"type":"b"}`), "b": echo},
			wantCalls: map[string][]string{"a": {`{"type":"a","n":1}`, `{"type":"a","n":3}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "entry without type is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`{"k":1}`)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "non-object entries are rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`"a"`)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "null entry is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`null`)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "invalid JSON entry is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`{"type":"a"`)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "two values in one entry are rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`{"type":"a"},{"type":"a"}`)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "duplicate key in returned entry is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`{"type":"a","x":1,"x":2}`)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "case-variant key in returned entry is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`{"type":"a","x":1,"X":2}`)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "too deep returned entry is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(`{"type":"a","n":` + nestObjects(maxDepth-1) + `}`)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "oversized entry is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(strings.Replace(hugeEntry, "%s", strings.Repeat("x", maxSize), 1))},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name: "oversized combined result is rejected",
			raw:  `[{"type":"a"}]`,
			hooks: map[string]func(Request) (Result, error){"a": returns(
				strings.Replace(hugeEntry, "%s", strings.Repeat("x", maxSize/2), 1),
				strings.Replace(hugeEntry, "%s", strings.Repeat("x", maxSize/2), 1))},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "invalid UTF-8 in returned entry is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns("{\"type\":\"a\",\"x\":\"\xff\"}")},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "empty result is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns()},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "typed error is passed through and stops later hooks",
			raw:       raw,
			hooks:     map[string]func(Request) (Result, error){"a": fails(Error{Code: CodeInvalidRequest, Reason: "not_allowed_1"}), "b": echo},
			wantCalls: map[string][]string{"a": {`{"type":"a","n":1}`, `{"type":"a","n":3}`}},
			check:     requireTypedError(Error{Code: CodeInvalidRequest, Reason: "not_allowed_1"}),
		},
		{
			name:      "wrapped typed error pointer is passed through",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": fails(errors.Join(boom, &Error{Code: CodeInvalidAuthorizationDetails, Reason: "bad_scope"}))},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireTypedError(Error{Code: CodeInvalidAuthorizationDetails, Reason: "bad_scope"}),
		},
		{
			name:      "typed error with uppercase reason is invalid hook output",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": fails(Error{Code: CodeInvalidRequest, Reason: "Not Allowed"})},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireInvalidTypedError,
		},
		{
			name:      "typed error with unknown code is invalid hook output",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": fails(Error{Code: "access_denied", Reason: "nope"})},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireInvalidTypedError,
		},
		{
			name:      "typed nil error pointer is an untyped failure",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": fails((*Error)(nil))},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check: func(t *testing.T, err error) {
				t.Helper()
				assert.NotErrorIs(t, err, ErrInvalid)
				assert.NotErrorIs(t, err, errInvalidHookResult)
				var typed Error
				assert.False(t, errors.As(err, &typed))
			},
		},
		{
			name:      "combined result of exactly 64 KiB is accepted",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(sizedEntry(maxSize - 2))},
			want:      "[" + sizedEntry(maxSize-2) + "]",
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
		},
		{
			name:      "combined result one byte over 64 KiB is rejected",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": returns(sizedEntry(maxSize - 1))},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check:     requireErrorIs(errInvalidHookResult),
		},
		{
			name:      "untyped error is distinguishable",
			raw:       `[{"type":"a"}]`,
			hooks:     map[string]func(Request) (Result, error){"a": fails(boom)},
			wantCalls: map[string][]string{"a": {`{"type":"a"}`}},
			check: func(t *testing.T, err error) {
				t.Helper()
				require.ErrorIs(t, err, boom)
				assert.NotErrorIs(t, err, ErrInvalid)
				assert.NotErrorIs(t, err, errInvalidHookResult)
				var typed Error
				assert.False(t, errors.As(err, &typed))
			},
		},
	}

	wantReq := Request{
		GrantType:          grantTypeExchange,
		ClientID:           "client",
		Actor:              "actor",
		ActorTokenPresent:  true,
		RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
		RequestedScopes:    []string{"openid"},
		GrantedAudience:    []string{"https://mcp.example.com"},
		Subject: Subject{
			Issuer:   "https://issuer.example.com",
			Subject:  "user",
			ClientID: "front-end",
			JWTID:    "jti",
			Audience: []string{"client"},
			IssuedAt: time.Unix(1000, 0),
			Expiry:   time.Unix(2000, 0),
			External: true,
			Claims:   map[string]any{"groups": []any{"eng"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls := map[string][]string{}
			var gotReqs []Request
			types := map[string]Type{}
			for name, hook := range tt.hooks {
				types[name] = Type{
					GrantTypes: []string{grantTypeExchange},
					Grant: func(_ context.Context, req Request) (Result, error) {
						for _, entry := range req.Entries {
							calls[name] = append(calls[name], string(entry))
						}
						entries := req.Entries
						req.Entries = nil
						gotReqs = append(gotReqs, req)
						return hook(Request{Entries: entries})
					},
				}
			}
			registry, err := New(types)
			require.NoError(t, err)
			pending, err := registry.Parse(tt.raw, grantTypeExchange)
			require.NoError(t, err)

			in := wantReq
			in.GrantType = ""
			got, err := pending.Grant(context.Background(), in)

			assert.Equal(t, tt.wantCalls, nilIfEmpty(calls), "hooks are called once each with all their entries")
			for _, req := range gotReqs {
				assert.Equal(t, wantReq, req, "hook receives the caller's context with the grant type filled in")
			}
			if tt.check != nil {
				require.Error(t, err)
				require.Nil(t, got)
				tt.check(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func requireErrorIs(target error) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		require.ErrorIs(t, err, target)
		var typed Error
		assert.False(t, errors.As(err, &typed))
	}
}

func requireTypedError(want Error) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		var got Error
		require.True(t, errors.As(err, &got))
		assert.Equal(t, want, got)
		assert.NotErrorIs(t, err, errInvalidHookResult)
	}
}

func requireInvalidTypedError(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, errInvalidHookResult)
	var typed Error
	assert.False(t, errors.As(err, &typed), "an invalid typed error must not reach the client")
}

func nilIfEmpty(calls map[string][]string) map[string][]string {
	if len(calls) == 0 {
		return nil
	}
	return calls
}

// sizedEntry returns one entry object of exactly total bytes.
func sizedEntry(total int) string {
	const base = `{"type":"a","pad":""}`
	return `{"type":"a","pad":"` + strings.Repeat("x", total-len(base)) + `"}`
}

// paddedEntry returns a one-entry array of exactly total bytes.
func paddedEntry(total int) string {
	const base = `[{"type":"a","pad":""}]`
	return `[{"type":"a","pad":"` + strings.Repeat("x", total-len(base)) + `"}]`
}

// entryWithNested embeds nested as a member of the single entry. The root
// array and the entry object add two levels of depth.
func entryWithNested(nested string) string {
	return `[{"type":"a","n":` + nested + `}]`
}

func nestObjects(levels int) string {
	return strings.Repeat(`{"k":`, levels) + `0` + strings.Repeat(`}`, levels)
}

func nestMixed(levels int) string {
	var open, closing strings.Builder
	closers := make([]string, levels)
	for i := range levels {
		if i%2 == 0 {
			open.WriteString(`[`)
			closers[i] = `]`
		} else {
			open.WriteString(`{"k":`)
			closers[i] = `}`
		}
	}
	for i := levels - 1; i >= 0; i-- {
		closing.WriteString(closers[i])
	}
	return open.String() + `0` + closing.String()
}
