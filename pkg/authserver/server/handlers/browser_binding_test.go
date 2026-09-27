// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestBrowserBindingCookieName(t *testing.T) {
	t.Parallel()

	plain := browserBindingCookieName("state-a", false)
	assert.True(t, strings.HasPrefix(plain, browserBindingCookieBase), "plain shape keeps the bare stem")
	assert.Len(t, plain, len(browserBindingCookieBase)+browserBindingNameHashLen)
	assert.NotContains(t, plain, "state-a", "the state itself is not in the name")

	assert.Equal(t, hostCookiePrefix+plain, browserBindingCookieName("state-a", true),
		"secure shape is the plain name behind the __Host- prefix")
	assert.Equal(t, plain, browserBindingCookieName("state-a", false), "name is stable per state")
	assert.NotEqual(t, plain, browserBindingCookieName("state-b", false), "each state gets its own cookie")
}

func TestBrowserBindingSecure_FollowsAuthorizeBaseScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		issuer        string
		authorizeBase string
		want          bool
	}{
		{name: "http issuer, no override", issuer: "http://localhost:8080", want: false},
		{name: "https issuer, no override", issuer: "https://auth.example.com", want: true},
		{name: "http issuer, https browser-facing base", issuer: "http://vmcp.ns.svc:4483", authorizeBase: "https://mcp.example.com", want: true},
		{name: "https issuer, http browser-facing base", issuer: "https://auth.example.com", authorizeBase: "http://localhost:18080", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, _, _ := handlerTestSetup(t)
			handler.config.AccessTokenIssuer = tt.issuer
			handler.config.AuthorizationEndpointBaseURL = tt.authorizeBase

			assert.Equal(t, tt.want, handler.browserBindingSecure())

			rec := httptest.NewRecorder()
			handler.setBrowserBindingCookie(rec, "some-state", "value")
			cookies := rec.Result().Cookies()
			require.Len(t, cookies, 1)
			assert.Equal(t, tt.want, cookies[0].Secure)
			assert.Equal(t, tt.want, strings.HasPrefix(cookies[0].Name, hostCookiePrefix))
			assert.Equal(t, "/", cookies[0].Path, "__Host- requires Path=/ and the plain shape keeps it for prefix-stripping proxies")
			assert.Empty(t, cookies[0].Domain, "cookie must stay host-only")
			assert.True(t, cookies[0].HttpOnly)
			assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
			assert.Equal(t, int(storage.DefaultPendingAuthorizationTTL.Seconds()), cookies[0].MaxAge)
		})
	}
}

func TestVerifyBrowserBinding(t *testing.T) {
	t.Parallel()

	const state = "verify-state"
	binding := newBrowserBinding()
	cookieName := browserBindingCookieName(state, testBindingSecure())

	tests := []struct {
		name       string
		storedHash string
		cookies    []*http.Cookie
		wantErr    error
	}{
		{
			name:       "matching cookie",
			storedHash: binding.hash,
			cookies:    []*http.Cookie{{Name: cookieName, Value: binding.value}},
		},
		{
			name:       "no cookie",
			storedHash: binding.hash,
			cookies:    nil,
			wantErr:    errBrowserBindingCookieMissing,
		},
		{
			name:       "cookie for a different state",
			storedHash: binding.hash,
			cookies:    []*http.Cookie{{Name: browserBindingCookieName("other-state", testBindingSecure()), Value: binding.value}},
			wantErr:    errBrowserBindingCookieMissing,
		},
		{
			name:       "wrong value",
			storedHash: binding.hash,
			cookies:    []*http.Cookie{{Name: cookieName, Value: newBrowserBinding().value}},
			wantErr:    errBrowserBindingMismatch,
		},
		{
			name:       "planted duplicate does not shadow the real cookie",
			storedHash: binding.hash,
			cookies:    []*http.Cookie{{Name: cookieName, Value: "planted"}, {Name: cookieName, Value: binding.value}},
		},
		{
			name:       "record without a binding is rejected even with a cookie",
			storedHash: "",
			cookies:    []*http.Cookie{{Name: cookieName, Value: binding.value}},
			wantErr:    errBrowserBindingUnbound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, _, _ := handlerTestSetup(t)
			req := newCallbackRequest("code=c&state="+state, tt.cookies...)

			err := handler.verifyBrowserBinding(req, state, tt.storedHash)

			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}
