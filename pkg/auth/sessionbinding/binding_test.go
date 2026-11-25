// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionbinding

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

func identity(iss, sub, token string) *auth.Identity {
	return &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": iss, "sub": sub}}, Token: token}
}

func TestBinding(t *testing.T) {
	t.Parallel()
	owner, err := FromIdentity(identity("issuer", "subject", "old"))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, stored string
		caller       *auth.Identity
		valid        bool
	}{
		{"refresh", owner, identity("issuer", "subject", "new"), true},
		{"foreign subject", owner, identity("issuer", "foreign", "token"), false},
		{"foreign issuer", owner, identity("other", "subject", "token"), false},
		{"missing identity", owner, nil, false},
		{"legacy unowned", "", nil, false},
		{"corrupt owner", "issuer\x00sub\x00tail", identity("issuer", "sub", "token"), false},
		{"auth disabled", UnauthenticatedSentinel, nil, true},
		{"no downgrade", UnauthenticatedSentinel, &auth.Identity{}, false},
		{"no upgrade", UnauthenticatedSentinel, identity("issuer", "subject", "token"), false},
		{"synthetic identity", owner, identity("issuer", "subject", ""), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tc.stored, tc.caller)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrNotFound)
			}
		})
	}
	for _, claims := range []map[string]any{
		nil, {"iss": "issuer"}, {"iss": 17, "sub": "subject"}, {"iss": "issuer", "sub": 17},
		{"iss": "", "sub": "subject"}, {"iss": "issuer", "sub": ""},
		{"iss": "issuer\x00tail", "sub": "subject"}, {"iss": "issuer", "sub": "sub\x00tail"},
	} {
		_, err := FromIdentity(&auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: claims}})
		require.ErrorIs(t, err, ErrInvalidBinding)
	}
}

func TestMiddleware(t *testing.T) {
	t.Parallel()
	storageErr := errors.New("private storage detail")
	for _, tc := range []struct {
		name, sid, owner string
		lookupErr        error
		caller           *auth.Identity
		status           int
	}{
		{name: "owner", sid: "id", owner: "issuer\x00subject", caller: identity("issuer", "subject", "fresh"), status: 204},
		{name: "foreign", sid: "id", owner: "issuer\x00subject", caller: identity("other", "subject", "token"), status: 404},
		{name: "unowned", sid: "id", status: 404},
		{name: "unknown", sid: "id", lookupErr: ErrNotFound, status: 404},
		{name: "storage failure", sid: "id", lookupErr: storageErr, status: 503},
		{name: "sessionless", status: 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called, lookedUp := false, false
			middleware, err := NewMiddleware(func(*http.Request) (string, error) { return tc.sid, nil },
				func(context.Context, string) (string, error) { lookedUp = true; return tc.owner, tc.lookupErr },
				func(w http.ResponseWriter, _ *http.Request, err error) { WriteOwnershipError(w, nil, err) })
			require.NoError(t, err)
			next := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(204) }))
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req = req.WithContext(auth.WithIdentity(req.Context(), tc.caller))
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.status == 204, called)
			require.Equal(t, tc.sid != "", lookedUp)
			require.NotContains(t, rec.Body.String(), "private")
			require.NotContains(t, rec.Body.String(), "subject")
		})
	}
	_, err := NewMiddleware(nil, nil, nil)
	require.Error(t, err)
}

func TestRequestID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query string
		headers     []string
		valid       bool
	}{
		{"query", "?sessionId=opaque%2Fid", nil, true},
		{"matching", "?sessionId=id", []string{"id"}, true},
		{"conflicting", "?sessionId=other", []string{"id"}, false},
		{"duplicate query", "?sessionId=id&sessionId=other", nil, false},
		{"duplicate header", "", []string{"id", "other"}, false},
		{"malformed query", "?sessionId=id%ZZ", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/messages"+tc.query, nil)
			req.Header[http.CanonicalHeaderKey("Mcp-Session-Id")] = tc.headers
			_, err := RequestID(req, "sessionId")
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrNotFound)
			}
		})
	}
}
