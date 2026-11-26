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

	b, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, DefaultMaxCallDuration, b.MaxCallDuration())
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

// failingLookupRecord fails every Lookup.
type failingLookupRecord struct{ Record }

func (failingLookupRecord) Lookup(context.Context, string) (Call, bool, error) {
	return Call{}, false, errors.New("record unavailable")
}

func TestAdmitExpiredToken(t *testing.T) {
	t.Parallel()
	const (
		key     = "https://as.example.com\x00jti-1"
		session = "session-1"
	)
	started := time.Unix(1_800_000_000, 0)
	expired := func(claims map[string]any) auth.ExpiredToken {
		return auth.ExpiredToken{Claims: claims, ExpiredAt: started}
	}
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
		want     bool
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
		{name: "call at the maximum call duration", claims: testClaims([]any{testEntry()}),
			call: &Call{SessionID: session, Started: started}, r: request(http.MethodPost, session),
			now: started.Add(time.Hour)},
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
			b, err := New(Config{MaxCallDuration: time.Hour, Record: tc.record})
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
				b.MarkReturned(key, started.Add(24*time.Hour), started)
			}
			assert.Equal(t, tc.want, b.AdmitExpiredToken(tc.r, expired(tc.claims), tc.now))
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
