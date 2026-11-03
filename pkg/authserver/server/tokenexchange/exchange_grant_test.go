// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	testIDJAGCap   = 24 * time.Hour
	testRefreshCap = 72 * time.Hour
)

var exchangeGrantNow = time.Unix(1_800_000_000, 0)

func exchangeGrantPolicy() ConfiguredClientPolicy {
	return ConfiguredClientPolicy{ExchangeRefreshTokens: map[string]time.Duration{
		oauthproto.TokenTypeIDJAG:        testIDJAGCap,
		oauthproto.TokenTypeRefreshToken: testRefreshCap,
	}}
}

func idJAGOnlyPolicy() ConfiguredClientPolicy {
	return ConfiguredClientPolicy{ExchangeRefreshTokens: map[string]time.Duration{
		oauthproto.TokenTypeIDJAG: testIDJAGCap,
	}}
}

// subSecondCapPolicy caps the id-jag lifetime below one second, which leaves no
// usable lifetime.
func subSecondCapPolicy() ConfiguredClientPolicy {
	return ConfiguredClientPolicy{ExchangeRefreshTokens: map[string]time.Duration{
		oauthproto.TokenTypeIDJAG: 500 * time.Millisecond,
	}}
}

func entryJSON(tokenType string, expiresAt string) string {
	if expiresAt == "" {
		return fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q]}`, exchangeGrantType, tokenType)
	}
	return fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q],"expires_at":%s}`, exchangeGrantType, tokenType, expiresAt)
}

func secs(t time.Time) string { return fmt.Sprint(t.Unix()) }

func requireReason(t *testing.T, err error, reason string) {
	t.Helper()
	var typed authorizationdetails.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, authorizationdetails.CodeInvalidAuthorizationDetails, typed.Code)
	assert.Equal(t, reason, typed.Reason)
}

// locationsMemberEntry is the representative unknown-member entry used by the
// table tests. Every decode path refuses it with reasonMalformedEntry.
var locationsMemberEntry = fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q],"locations":["https://a"]}`, exchangeGrantType, oauthproto.TokenTypeIDJAG)

// malformedEntries are refused with the same reason by every decode path.
var malformedEntries = []struct {
	name   string
	raw    string
	reason string
}{
	{"unknown member", fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q],"x":1}`, exchangeGrantType, oauthproto.TokenTypeIDJAG), reasonMalformedEntry},
	{"locations member", locationsMemberEntry, reasonMalformedEntry},
	{"case-variant member", fmt.Sprintf(`{"type":%q,"Issuable_Token_Types":[%q]}`, exchangeGrantType, oauthproto.TokenTypeIDJAG), reasonMalformedEntry},
	{"wrong type", fmt.Sprintf(`{"type":"https://example.com/other","issuable_token_types":[%q]}`, oauthproto.TokenTypeIDJAG), reasonMalformedEntry},
	{"type not a string", fmt.Sprintf(`{"type":1,"issuable_token_types":[%q]}`, oauthproto.TokenTypeIDJAG), reasonMalformedEntry},
	{"not an object", `"x"`, reasonMalformedEntry},
	{"null entry", `null`, reasonMalformedEntry},
	{"types missing", fmt.Sprintf(`{"type":%q}`, exchangeGrantType), reasonIssuableTypes},
	{"types empty", fmt.Sprintf(`{"type":%q,"issuable_token_types":[]}`, exchangeGrantType), reasonIssuableTypes},
	{"types null", fmt.Sprintf(`{"type":%q,"issuable_token_types":null}`, exchangeGrantType), reasonIssuableTypes},
	{"types a string", fmt.Sprintf(`{"type":%q,"issuable_token_types":%q}`, exchangeGrantType, oauthproto.TokenTypeIDJAG), reasonIssuableTypes},
	{"types element not a string", fmt.Sprintf(`{"type":%q,"issuable_token_types":[1]}`, exchangeGrantType), reasonIssuableTypes},
	{"two types", fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q,%q]}`, exchangeGrantType, oauthproto.TokenTypeIDJAG, oauthproto.TokenTypeRefreshToken), reasonIssuableTypes},
	{"duplicate type", fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q,%q]}`, exchangeGrantType, oauthproto.TokenTypeIDJAG, oauthproto.TokenTypeIDJAG), reasonIssuableTypes},
	{"unknown URN", fmt.Sprintf(`{"type":%q,"issuable_token_types":["urn:example:x"]}`, exchangeGrantType), reasonIssuableTypes},
	{"expires_at string", entryJSON(oauthproto.TokenTypeIDJAG, `"1900000000"`), reasonExpiresAt},
	{"expires_at null", entryJSON(oauthproto.TokenTypeIDJAG, `null`), reasonExpiresAt},
	{"expires_at bool", entryJSON(oauthproto.TokenTypeIDJAG, `true`), reasonExpiresAt},
	{"expires_at fractional", entryJSON(oauthproto.TokenTypeIDJAG, `1900000000.5`), reasonExpiresAt},
	{"expires_at 1.0", entryJSON(oauthproto.TokenTypeIDJAG, `1900000000.0`), reasonExpiresAt},
	{"expires_at exponent", entryJSON(oauthproto.TokenTypeIDJAG, `1.9e9`), reasonExpiresAt},
	{"expires_at int64 overflow", entryJSON(oauthproto.TokenTypeIDJAG, `99999999999999999999`), reasonExpiresAt},
	{"Expires_At case-variant member", fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q],"Expires_At":1900000000}`, exchangeGrantType, oauthproto.TokenTypeIDJAG), reasonMalformedEntry},
	{"expires_at array", entryJSON(oauthproto.TokenTypeIDJAG, `[1]`), reasonExpiresAt},
}

func TestDecodeExchangeGrantEntry_Malformed(t *testing.T) {
	t.Parallel()
	for _, tc := range malformedEntries {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeExchangeGrantEntry(json.RawMessage(tc.raw))
			requireReason(t, err, tc.reason)
		})
	}
}

func TestValidateTopLevelEntry(t *testing.T) {
	t.Parallel()
	now := exchangeGrantNow

	tests := []struct {
		name     string
		now      time.Time
		raw      string
		policy   ConfiguredClientPolicy
		wantErr  string
		wantType string
		wantExp  time.Time
	}{
		{
			name:     "absent expires_at filled with id-jag cap",
			raw:      entryJSON(oauthproto.TokenTypeIDJAG, ""),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeIDJAG,
			wantExp:  now.Add(testIDJAGCap),
		},
		{
			name:     "absent expires_at filled with refresh cap",
			raw:      entryJSON(oauthproto.TokenTypeRefreshToken, ""),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeRefreshToken,
			wantExp:  now.Add(testRefreshCap),
		},
		{
			name:     "shorter than cap kept",
			raw:      entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(time.Hour))),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeIDJAG,
			wantExp:  now.Add(time.Hour),
		},
		{
			name:     "exactly the cap is accepted uncapped",
			raw:      entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(testIDJAGCap))),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeIDJAG,
			wantExp:  now.Add(testIDJAGCap),
		},
		{
			name:     "one second over the cap is capped",
			raw:      entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(testIDJAGCap+time.Second))),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeIDJAG,
			wantExp:  now.Add(testIDJAGCap),
		},
		{
			name:     "far above the cap is capped",
			raw:      entryJSON(oauthproto.TokenTypeRefreshToken, "9000000000000000000"),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeRefreshToken,
			wantExp:  now.Add(testRefreshCap),
		},
		{
			name:     "cap is per requested type",
			raw:      entryJSON(oauthproto.TokenTypeRefreshToken, secs(now.Add(testIDJAGCap+time.Hour))),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeRefreshToken,
			wantExp:  now.Add(testIDJAGCap + time.Hour),
		},
		{
			name:    "expires_at in the past",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(-time.Second))),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "expires_at equal to now",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now)),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "expires_at zero",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, "0"),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "expires_at minus zero",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, "-0"),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:     "expires_at with surrounding whitespace",
			raw:      fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q],"expires_at" : %d }`, exchangeGrantType, oauthproto.TokenTypeIDJAG, now.Add(time.Hour).Unix()),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeIDJAG,
			wantExp:  now.Add(time.Hour),
		},
		{
			name:    "expires_at negative",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, "-5"),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "type not configured",
			raw:     entryJSON(oauthproto.TokenTypeRefreshToken, ""),
			policy:  idJAGOnlyPolicy(),
			wantErr: reasonTokenTypeNotPermitted,
		},
		{
			name:    "nil policy map refuses everything",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, ""),
			policy:  ConfiguredClientPolicy{},
			wantErr: reasonTokenTypeNotPermitted,
		},
		{
			name:     "max int64 is capped, not wrapped",
			raw:      entryJSON(oauthproto.TokenTypeIDJAG, fmt.Sprint(math.MaxInt64)),
			policy:   exchangeGrantPolicy(),
			wantType: oauthproto.TokenTypeIDJAG,
			wantExp:  now.Add(testIDJAGCap),
		},
		{
			name:    "min int64 refused",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, fmt.Sprint(math.MinInt64)),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "cap under one second leaves no lifetime",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, ""),
			policy:  subSecondCapPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "malformed: locations member is refused",
			raw:     locationsMemberEntry,
			policy:  exchangeGrantPolicy(),
			wantErr: reasonMalformedEntry,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runNow := tc.now
			if runNow.IsZero() {
				runNow = now
			}
			granted, exp, err := validateTopLevelEntry(json.RawMessage(tc.raw), tc.policy, runNow)
			if tc.wantErr != "" {
				requireReason(t, err, tc.wantErr)
				assert.Nil(t, granted)
				return
			}
			require.NoError(t, err)
			assert.True(t, tc.wantExp.Equal(exp), "ExpiresAt %v, want %v", exp, tc.wantExp)
			assert.Zero(t, exp.Nanosecond(), "ExpiresAt is whole seconds")
			assertEcho(t, granted, tc.wantType, exp)
		})
	}
}

func TestValidateChildEntry(t *testing.T) {
	t.Parallel()
	now := exchangeGrantNow

	parentUntil := func(d time.Duration) *storage.ExchangeGrant {
		return &storage.ExchangeGrant{
			IssuableTokenTypes: []string{oauthproto.TokenTypeRefreshToken},
			ExpiresAt:          exchangeGrantNow.Add(d),
		}
	}

	tests := []struct {
		name    string
		now     time.Time
		raw     string
		parent  *storage.ExchangeGrant
		policy  ConfiguredClientPolicy
		wantErr string
		wantExp time.Time
	}{
		{
			name:    "absent expires_at gets own-type cap",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, ""),
			parent:  parentUntil(testRefreshCap),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(testIDJAGCap),
		},
		{
			name:    "absent expires_at gets parent remaining when sooner",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, ""),
			parent:  parentUntil(time.Hour),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(time.Hour),
		},
		{
			name:    "requested above parent is capped to parent",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(10*time.Hour))),
			parent:  parentUntil(time.Hour),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(time.Hour),
		},
		{
			name:    "requested above own cap is capped to own cap",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(testIDJAGCap+time.Hour))),
			parent:  parentUntil(testRefreshCap),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(testIDJAGCap),
		},
		{
			name:    "requested below both kept",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(time.Minute))),
			parent:  parentUntil(time.Hour),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(time.Minute),
		},
		{
			name:    "exactly the parent is accepted uncapped",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(time.Hour))),
			parent:  parentUntil(time.Hour),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(time.Hour),
		},
		{
			name:    "one second over parent is capped",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(time.Hour+time.Second))),
			parent:  parentUntil(time.Hour),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(time.Hour),
		},
		{
			name:    "child lists refresh_token",
			raw:     entryJSON(oauthproto.TokenTypeRefreshToken, ""),
			parent:  parentUntil(testRefreshCap),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonTokenTypeNotPermitted,
		},
		{
			name:    "child type not configured",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, ""),
			parent:  parentUntil(testRefreshCap),
			policy:  ConfiguredClientPolicy{ExchangeRefreshTokens: map[string]time.Duration{oauthproto.TokenTypeRefreshToken: testRefreshCap}},
			wantErr: reasonTokenTypeNotPermitted,
		},
		{
			name:    "expires_at in the past",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now.Add(-time.Second))),
			parent:  parentUntil(time.Hour),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "expires_at equal to now",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, secs(now)),
			parent:  parentUntil(time.Hour),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "parent expires exactly at own cap is accepted",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, ""),
			parent:  parentUntil(testIDJAGCap),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(testIDJAGCap),
		},
		{
			name:    "max int64 is capped to own cap, not wrapped",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, fmt.Sprint(math.MaxInt64)),
			parent:  parentUntil(testRefreshCap),
			policy:  exchangeGrantPolicy(),
			wantExp: now.Add(testIDJAGCap),
		},
		{
			name:    "min int64 refused",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, fmt.Sprint(math.MinInt64)),
			parent:  parentUntil(testRefreshCap),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "cap under one second leaves no lifetime",
			raw:     entryJSON(oauthproto.TokenTypeIDJAG, ""),
			parent:  parentUntil(time.Hour),
			policy:  subSecondCapPolicy(),
			wantErr: reasonExpiresAt,
		},
		{
			name:    "malformed: locations member is refused",
			raw:     locationsMemberEntry,
			parent:  parentUntil(time.Hour),
			policy:  exchangeGrantPolicy(),
			wantErr: reasonMalformedEntry,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runNow := tc.now
			if runNow.IsZero() {
				runNow = now
			}
			granted, exp, err := validateChildEntry(json.RawMessage(tc.raw), tc.parent, tc.policy, runNow)
			if tc.wantErr != "" {
				requireReason(t, err, tc.wantErr)
				assert.Nil(t, granted)
				return
			}
			require.NoError(t, err)
			assert.True(t, tc.wantExp.Equal(exp), "ExpiresAt %v, want %v", exp, tc.wantExp)
			assert.Zero(t, exp.Nanosecond(), "ExpiresAt is whole seconds")
			assertEcho(t, granted, oauthproto.TokenTypeIDJAG, exp)
		})
	}
}

// assertEcho checks the granted entry: exactly the three members, and an
// expires_at equal to the grant's ExpiresAt in integer seconds.
func assertEcho(t *testing.T, granted json.RawMessage, tokenType string, expiresAt time.Time) {
	t.Helper()
	want := fmt.Sprintf(`{"type":%q,"issuable_token_types":[%q],"expires_at":%d}`, exchangeGrantType, tokenType, expiresAt.Unix())
	assert.JSONEq(t, want, string(granted))
	assert.Contains(t, string(granted), fmt.Sprintf(`"expires_at":%d}`, expiresAt.Unix()), "expires_at is an integer literal")
}

func TestExchangeGrantRegistry(t *testing.T) {
	t.Parallel()
	registry, err := newExchangeGrantRegistry()
	require.NoError(t, err)

	grant := func(t *testing.T, raw string) (json.RawMessage, error) {
		t.Helper()
		pending, err := registry.Parse(raw, oauthproto.GrantTypeTokenExchange)
		if err != nil {
			return nil, err
		}
		return pending.Grant(context.Background(), authorizationdetails.Request{ClientID: "c"})
	}

	t.Run("one valid entry passes shape check unchanged", func(t *testing.T) {
		t.Parallel()
		out, err := grant(t, "["+entryJSON(oauthproto.TokenTypeIDJAG, "1900000000")+"]")
		require.NoError(t, err)
		assert.JSONEq(t, "["+entryJSON(oauthproto.TokenTypeIDJAG, "1900000000")+"]", string(out))
	})

	t.Run("two entries are refused by the hook", func(t *testing.T) {
		t.Parallel()
		e := entryJSON(oauthproto.TokenTypeIDJAG, "")
		_, err := grant(t, "["+e+","+e+"]")
		requireReason(t, err, reasonSingleEntry)
	})

	t.Run("empty array grants nothing, so the handler must check the count", func(t *testing.T) {
		t.Parallel()
		out, err := grant(t, "[]")
		require.NoError(t, err)
		assert.Equal(t, "[]", string(out))
	})

	t.Run("unknown member is refused by the hook", func(t *testing.T) {
		t.Parallel()
		out, err := grant(t, "["+locationsMemberEntry+"]")
		require.Error(t, err, "granted %s", out)
		requireReason(t, err, reasonMalformedEntry)
	})

	t.Run("wrong type is refused by Registry.Parse before the hook", func(t *testing.T) {
		t.Parallel()
		wrongType := fmt.Sprintf(`{"type":"https://example.com/other","issuable_token_types":[%q]}`, oauthproto.TokenTypeIDJAG)
		out, err := grant(t, "["+wrongType+"]")
		require.Error(t, err, "granted %s", out)
		assert.ErrorIs(t, err, authorizationdetails.ErrInvalid)
	})

	t.Run("other types and other grant types are refused", func(t *testing.T) {
		t.Parallel()
		_, err := registry.Parse(`[{"type":"https://example.com/other"}]`, oauthproto.GrantTypeTokenExchange)
		assert.ErrorIs(t, err, authorizationdetails.ErrInvalid)
		_, err = registry.Parse("["+entryJSON(oauthproto.TokenTypeIDJAG, "")+"]", "authorization_code")
		assert.ErrorIs(t, err, authorizationdetails.ErrInvalid)
	})

	t.Run("hook returns its input entries unchanged", func(t *testing.T) {
		t.Parallel()
		// Member order is non-canonical, so any re-encoding would be visible.
		input := fmt.Sprintf(`{"issuable_token_types":[%q],"type":%q}`, oauthproto.TokenTypeIDJAG, exchangeGrantType)
		raw := []json.RawMessage{json.RawMessage(input)}
		before := make([]json.RawMessage, len(raw))
		for i := range raw {
			before[i] = append(json.RawMessage(nil), raw[i]...)
		}

		out, err := grantExchangeGrantShape(context.Background(), authorizationdetails.Request{Entries: raw})
		require.NoError(t, err)
		assert.Equal(t, before, raw, "hook changed its input entries")
		assert.Equal(t, before, out.Entries, "hook output differs from its input")
	})
}

func TestExchangeGrantExpiresIn(t *testing.T) {
	t.Parallel()
	whole := exchangeGrantNow

	tests := []struct {
		name      string
		now       time.Time
		expiresAt time.Time
		want      time.Duration
	}{
		{
			name:      "whole-second now",
			now:       whole,
			expiresAt: whole.Add(90 * time.Second),
			want:      90 * time.Second,
		},
		{
			name:      "fraction of now is dropped",
			now:       whole.Add(300 * time.Millisecond),
			expiresAt: whole.Add(90 * time.Second),
			want:      90 * time.Second,
		},
		{
			name:      "remaining lifetime under a second is the one-second minimum",
			now:       whole.Add(900 * time.Millisecond),
			expiresAt: whole.Add(time.Second),
			want:      time.Second,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, exchangeGrantExpiresIn(tc.expiresAt, tc.now))
		})
	}
}

func TestParseExchangeGrantDetails(t *testing.T) {
	t.Parallel()
	registry, err := newExchangeGrantRegistry()
	require.NoError(t, err)
	valid := entryJSON(oauthproto.TokenTypeIDJAG, "")
	two := "[" + valid + "," + valid + "]"

	tests := []struct {
		name       string
		form       url.Values
		wantFosite bool
		wantCode   string
		wantDesc   string
		wantEntry  bool
	}{
		{
			name:       "absent",
			form:       url.Values{},
			wantFosite: true,
			wantCode:   "invalid_request",
			wantDesc:   "The authorization_details parameter is required.",
		},
		{
			name:       "empty string",
			form:       url.Values{authorizationDetailsParam: {""}},
			wantFosite: true,
			wantCode:   "invalid_request",
			wantDesc:   "The authorization_details parameter is required.",
		},
		{
			name:       "repeated",
			form:       url.Values{authorizationDetailsParam: {"[" + valid + "]", "[" + valid + "]"}},
			wantFosite: true,
			wantCode:   "invalid_request",
			wantDesc:   "The authorization_details parameter must not be repeated.",
		},
		{
			name:       "entry with no type",
			form:       url.Values{authorizationDetailsParam: {`[{"issuable_token_types":["x"]}]`}},
			wantFosite: true,
			wantCode:   "invalid_authorization_details",
			wantDesc:   "The authorization_details parameter is invalid.",
		},
		{
			name:     "empty array is parsed, singleGrantedEntry refuses it",
			form:     url.Values{authorizationDetailsParam: {"[]"}},
			wantCode: "invalid_authorization_details",
			wantDesc: reasonSingleEntry,
		},
		{
			name:     "two entries",
			form:     url.Values{authorizationDetailsParam: {two}},
			wantCode: "invalid_authorization_details",
			wantDesc: reasonSingleEntry,
		},
		{
			name:      "valid single entry",
			form:      url.Values{authorizationDetailsParam: {"[" + valid + "]"}},
			wantEntry: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entry, err := func() (json.RawMessage, error) {
				pending, err := parseExchangeGrantDetails(registry, tc.form)
				if err != nil {
					return nil, err
				}
				granted, err := pending.Grant(context.Background(), authorizationdetails.Request{ClientID: "c"})
				if err != nil {
					return nil, err
				}
				return singleGrantedEntry(granted)
			}()
			if tc.wantEntry {
				require.NoError(t, err)
				assert.JSONEq(t, valid, string(entry))
				return
			}
			require.Error(t, err)
			if tc.wantFosite {
				var rfc *fosite.RFC6749Error
				require.ErrorAs(t, err, &rfc)
				assert.Equal(t, tc.wantCode, rfc.ErrorField)
				assert.Equal(t, tc.wantDesc, rfc.DescriptionField)
				return
			}
			var typed authorizationdetails.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.wantCode, string(typed.Code))
			assert.Equal(t, tc.wantDesc, typed.Reason)
		})
	}
}

func TestSingleGrantedEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		granted string
		want    string
		wantErr bool
	}{
		{name: "empty array is refused", granted: "[]", wantErr: true},
		{name: "one entry is returned", granted: `[{"a":1}]`, want: `{"a":1}`},
		{name: "two entries are refused", granted: `[{"a":1},{"a":2}]`, wantErr: true},
		{name: "null is refused", granted: "null", wantErr: true},
		{name: "not an array is refused", granted: "{}", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := singleGrantedEntry(json.RawMessage(tc.granted))
			if tc.wantErr {
				requireReason(t, err, reasonSingleEntry)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}
