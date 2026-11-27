// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestNewValidates(t *testing.T) {
	t.Parallel()
	_, err := New(Config{MaxCallDuration: -time.Second})
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig)
	_, err = New(Config{Clients: []string{"a", ""}})
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig)

	_, err = New(Config{ExpiredTokenAdmission: []ExpiredTokenAdmission{{Issuer: "i", ClientID: "c"}}})
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig, "an invalid admission list")
	_, err = New(Config{MaxCallDuration: time.Minute, ExpiredTokenAdmission: []ExpiredTokenAdmission{
		{Issuer: "i", ClientID: "c", Grace: 2 * time.Minute},
	}})
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig, "a grace beyond the configured maximum call duration")

	b, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, DefaultMaxCallDuration, b.MaxCallDuration())
	assert.False(t, b.AdmitsExpiredTokens())
	assert.NotNil(t, b.Record())
	assert.False(t, b.RecordShared())
	assert.False(t, b.ClientsListed())

	record := NewMemoryRecord()
	b, err = New(Config{Clients: []string{"mecatl"}, Record: record, MaxCallDuration: time.Hour})
	require.NoError(t, err)
	assert.Same(t, record, b.Record())
	assert.True(t, b.RecordShared())
	assert.True(t, b.ClientsListed())
	assert.Equal(t, time.Hour, b.MaxCallDuration())
}

func TestReturnedCalls(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_900_000_000, 0)
	var calls returnedCalls
	assert.False(t, calls.has("a", now))
	calls.add("a", now.Add(time.Minute), now)
	assert.True(t, calls.has("a", now))
	assert.False(t, calls.has("a", now.Add(time.Minute)), "an entry ends at its expiry")
	calls.add("b", now.Add(2*time.Minute), now.Add(time.Minute))
	assert.Len(t, calls.expires, 1, "an add drops expired entries")
}

// failingLookupRecord fails every Lookup, with an open call, so a caller that
// ignored the error would admit it.
type failingLookupRecord struct{ Record }

func (failingLookupRecord) Lookup(context.Context, string) (Call, bool, error) {
	return Call{SessionID: "session-1", RequestID: "7", Started: time.Unix(1_800_000_000, 0)}, true,
		errors.New("record unavailable")
}

func TestAdmitExpiredToken(t *testing.T) {
	t.Parallel()
	const (
		key     = "https://as.example.com\x00jti-1"
		session = "session-1"
	)
	started := time.Unix(1_800_000_000, 0)
	const grace = 59 * time.Minute
	listed := []ExpiredTokenAdmission{{Issuer: "https://as.example.com", ClientID: "mecatl", Grace: grace}}
	azpOnly := testClaims([]any{testEntry()})
	azpOnly["azp"] = azpOnly["client_id"]
	delete(azpOnly, "client_id")
	listEntry := map[string]any{"type": toolcalldetails.Type, "server": "github", "method": "tools/list"}
	request := func(method, sessionID string) *http.Request {
		r := httptest.NewRequest(method, "/mcp", nil)
		if sessionID != "" {
			r.Header.Set("Mcp-Session-Id", sessionID)
		}
		return r
	}

	tests := []struct {
		name     string
		claims   map[string]any
		call     *Call // recorded under key when set
		ended    bool
		r        *http.Request
		now      time.Time
		returned bool
		record   Record
		// admission is the binding's ExpiredTokenAdmission list; nil means
		// listed, and strict means none.
		admission []ExpiredTokenAdmission
		strict    bool
		// expiredAt is the token's exp; zero means started.
		expiredAt time.Time
		want      bool
	}{
		{name: "cancellation or response on the call's session", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodPost, session),
			now: started.Add(time.Minute), want: true},
		{name: "GET on the call's session", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodGet, session),
			now: started.Add(time.Minute), want: true},
		{name: "DELETE on the call's session", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodDelete, session),
			now: started.Add(time.Minute), want: true},
		{name: "token without an entry", claims: testClaims(nil),
			call: &Call{SessionID: session, Started: started}, r: request(http.MethodPost, session),
			now: started.Add(time.Minute)},
		{name: "malformed entry", claims: testClaims([]any{"not an object"}),
			call: &Call{SessionID: session, Started: started}, r: request(http.MethodPost, session),
			now: started.Add(time.Minute)},
		{name: "another HTTP method", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, Started: started}, r: request(http.MethodPut, session),
			now: started.Add(time.Minute)},
		{name: "no call recorded", claims: testClaims([]any{testEntry()}),
			r: request(http.MethodPost, session), now: started.Add(time.Minute)},
		{name: "ended call", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, Started: started}, ended: true, r: request(http.MethodPost, session),
			now: started.Add(time.Minute)},
		{name: "call that returned on this replica", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, Started: started}, returned: true, r: request(http.MethodPost, session),
			now: started.Add(time.Minute)},
		{name: "call at the maximum call duration, inside the grace", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, Started: started}, r: request(http.MethodPost, session),
			expiredAt: started.Add(30 * time.Minute), now: started.Add(time.Hour)},
		{name: "just inside the entry's grace", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodPost, session),
			now: started.Add(grace - time.Second), want: true},
		{name: "at the entry's grace", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodPost, session),
			now: started.Add(grace)},
		{name: "no ExpiredTokenAdmission list", claims: testClaims([]any{testEntry()}), strict: true,
			call: &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodPost, session),
			now: started.Add(time.Minute)},
		{name: "issuer not listed", claims: testClaims([]any{testEntry()}),
			admission: []ExpiredTokenAdmission{{Issuer: "https://other.example.com", ClientID: "mecatl", Grace: grace}},
			call:      &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodPost, session),
			now: started.Add(time.Minute)},
		{name: "client not listed", claims: testClaims([]any{testEntry()}),
			admission: []ExpiredTokenAdmission{{Issuer: "https://as.example.com", ClientID: "other", Grace: grace}},
			call:      &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodPost, session),
			now: started.Add(time.Minute)},
		{name: "client named only in azp", claims: azpOnly,
			call: &Call{SessionID: session, RequestID: "7", Started: started}, r: request(http.MethodPost, session),
			now: started.Add(time.Minute)},
		{name: "Modern call, without a session", claims: testClaims([]any{testEntry()}),
			call: &Call{Started: started}, r: request(http.MethodPost, ""), now: started.Add(time.Minute)},
		{name: "another session", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, Started: started}, r: request(http.MethodPost, "session-2"),
			now: started.Add(time.Minute)},
		{name: "no session", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, Started: started}, r: request(http.MethodGet, ""),
			now: started.Add(time.Minute)},
		{name: "list call, recorded ended", claims: testClaims([]any{listEntry}),
			call: &Call{SessionID: session, RequestID: "2", Started: started}, ended: true,
			r: request(http.MethodGet, session), now: started.Add(time.Minute)},
		{name: "record failure", claims: testClaims([]any{testEntry()}),
			r: request(http.MethodPost, session), now: started.Add(time.Minute),
			record: failingLookupRecord{NewMemoryRecord()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			admission := tc.admission
			switch {
			case tc.strict:
				admission = nil
			case admission == nil:
				admission = listed
			}
			b, err := New(Config{MaxCallDuration: time.Hour, Record: tc.record, ExpiredTokenAdmission: admission})
			require.NoError(t, err)
			ctx := context.Background()
			if tc.call != nil {
				ok, err := b.Record().Start(ctx, key, *tc.call, started.Add(24*time.Hour))
				require.NoError(t, err)
				require.True(t, ok)
				if tc.ended {
					require.NoError(t, b.Record().End(ctx, key))
				}
			}
			if tc.returned {
				b.returned.add(key, started.Add(24*time.Hour), started)
			}
			expiredAt := tc.expiredAt
			if expiredAt.IsZero() {
				expiredAt = started
			}
			expired := auth.ExpiredToken{Claims: tc.claims, ExpiredAt: expiredAt}
			assert.Equal(t, tc.want, b.AdmitExpiredToken(tc.r, expired, tc.now))
			assert.Equal(t, !tc.strict, b.AdmitsExpiredTokens())
		})
	}
}

func TestAdmitExpiredTokenZeroBinding(t *testing.T) {
	t.Parallel()
	var b Binding
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Mcp-Session-Id", "session-1")
	assert.False(t, b.AdmitExpiredToken(r, auth.ExpiredToken{Claims: testClaims([]any{testEntry()})}, time.Now()))
}

// TestEndCallDefersToDispatch checks that a call whose request returns while
// its dispatch runs stays open until the last dispatch is done, and that a
// dispatch that begins after the call ended does not reopen it.
func TestEndCallDefersToDispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now()
	token := &Token{Key: "https://as.example.com\x00jti-1", JTI: "jti-1"}
	newOpenCall := func(t *testing.T) *Binding {
		t.Helper()
		b, err := New(Config{})
		require.NoError(t, err)
		ok, err := b.Record().Start(ctx, token.Key, Call{SessionID: "s", RequestID: "7", Started: now}, now.Add(time.Hour))
		require.NoError(t, err)
		require.True(t, ok)
		return b
	}
	isOpen := func(t *testing.T, b *Binding) bool {
		t.Helper()
		_, open, err := b.OpenCall(ctx, token.Key, now)
		require.NoError(t, err)
		return open
	}

	t.Run("no dispatch: the request's return ends the call", func(t *testing.T) {
		t.Parallel()
		b := newOpenCall(t)
		b.EndCall(ctx, token, now.Add(time.Hour))
		assert.False(t, isOpen(t, b))
	})
	t.Run("the last of two dispatches ends the call", func(t *testing.T) {
		t.Parallel()
		b := newOpenCall(t)
		first, second := b.BeginDispatch(token), b.BeginDispatch(token)
		b.EndCall(ctx, token, now.Add(time.Hour))
		assert.True(t, isOpen(t, b), "a dispatch still runs")
		first()
		first() // idempotent
		assert.True(t, isOpen(t, b), "the second dispatch still runs")
		second()
		assert.False(t, isOpen(t, b))
		assert.Empty(t, b.dispatches.calls, "no entry outlives its dispatches")
	})
	t.Run("a dispatch done before the request returns leaves the end to it", func(t *testing.T) {
		t.Parallel()
		b := newOpenCall(t)
		b.BeginDispatch(token)()
		assert.True(t, isOpen(t, b))
		b.EndCall(ctx, token, now.Add(time.Hour))
		assert.False(t, isOpen(t, b))
	})
	t.Run("a dispatch after the call ended does not reopen it", func(t *testing.T) {
		t.Parallel()
		b := newOpenCall(t)
		b.EndCall(ctx, token, now.Add(time.Hour))
		done := b.BeginDispatch(token)
		assert.False(t, isOpen(t, b))
		done()
		assert.False(t, isOpen(t, b))
	})
}

func TestValidateExpiredTokenAdmission(t *testing.T) {
	t.Parallel()
	entry := func(issuer, clientID string, grace time.Duration) ExpiredTokenAdmission {
		return ExpiredTokenAdmission{Issuer: issuer, ClientID: clientID, Grace: grace}
	}
	tests := []struct {
		name    string
		entries []ExpiredTokenAdmission
		max     time.Duration
		wantErr string
	}{
		{name: "empty list"},
		{name: "valid entries", entries: []ExpiredTokenAdmission{
			entry("https://as.example.com", "a", time.Minute),
			entry("https://as.example.com", "b", DefaultMaxCallDuration),
			entry("https://other.example.com", "a", time.Second),
		}},
		{name: "empty issuer", entries: []ExpiredTokenAdmission{entry("", "a", time.Minute)},
			wantErr: "issuer must not be empty"},
		{name: "blank issuer", entries: []ExpiredTokenAdmission{entry("  ", "a", time.Minute)},
			wantErr: "issuer must not be empty"},
		{name: "empty client", entries: []ExpiredTokenAdmission{entry("i", "", time.Minute)},
			wantErr: "clientId must not be empty"},
		{name: "zero grace", entries: []ExpiredTokenAdmission{entry("i", "a", 0)},
			wantErr: "grace must be positive"},
		{name: "negative grace", entries: []ExpiredTokenAdmission{entry("i", "a", -time.Second)},
			wantErr: "grace must be positive"},
		{name: "grace beyond the default maximum call duration",
			entries: []ExpiredTokenAdmission{entry("i", "a", DefaultMaxCallDuration+time.Second)},
			wantErr: "exceeds the maximum call duration"},
		{name: "grace beyond a configured maximum call duration", max: time.Minute,
			entries: []ExpiredTokenAdmission{entry("i", "a", 2*time.Minute)},
			wantErr: "exceeds the maximum call duration"},
		{name: "duplicate issuer and client", entries: []ExpiredTokenAdmission{
			entry("i", "a", time.Minute), entry("i", "a", 2*time.Minute),
		}, wantErr: "already listed by entry 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateExpiredTokenAdmission(tc.entries, tc.max)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestWithinExpiredTokenGrace(t *testing.T) {
	t.Parallel()
	exp := time.Unix(1_800_000_000, 0)
	b, err := New(Config{ExpiredTokenAdmission: []ExpiredTokenAdmission{
		{Issuer: "https://as.example.com", ClientID: "mecatl", Grace: time.Minute},
	}})
	require.NoError(t, err)
	claims := testClaims([]any{testEntry()})
	assert.True(t, b.WithinExpiredTokenGrace(claims, exp, exp))
	assert.True(t, b.WithinExpiredTokenGrace(claims, exp, exp.Add(time.Minute-time.Nanosecond)))
	assert.False(t, b.WithinExpiredTokenGrace(claims, exp, exp.Add(time.Minute)))

	azpOnly := testClaims([]any{testEntry()})
	azpOnly["azp"] = azpOnly["client_id"]
	delete(azpOnly, "client_id")
	assert.False(t, b.WithinExpiredTokenGrace(azpOnly, exp, exp), "azp is not read")
	assert.False(t, b.WithinExpiredTokenGrace(map[string]any{"client_id": "mecatl"}, exp, exp), "no iss")

	strict, err := New(Config{})
	require.NoError(t, err)
	assert.False(t, strict.WithinExpiredTokenGrace(claims, exp, exp), "no list means strict exp")
}
