// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
)

// recordingRefreshHandler is an embedder token endpoint handler that claims
// every request and records what it was handed, so tests can assert the
// refreshGrantOnlyHandler gate rather than the handler's own filtering.
type recordingRefreshHandler struct {
	mu        sync.Mutex
	handled   []fosite.Arguments
	populated int
	sessions  []fosite.Session
	err       error
}

func (h *recordingRefreshHandler) setErr(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
}

func (h *recordingRefreshHandler) handledGrants() []fosite.Arguments {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]fosite.Arguments(nil), h.handled...)
}

func (*recordingRefreshHandler) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return true
}

func (*recordingRefreshHandler) CanHandleTokenEndpointRequest(context.Context, fosite.AccessRequester) bool {
	return true
}

func (h *recordingRefreshHandler) HandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handled = append(h.handled, requester.GetGrantTypes())
	h.sessions = append(h.sessions, requester.GetSession())
	return h.err
}

func (h *recordingRefreshHandler) PopulateTokenEndpointResponse(
	context.Context, fosite.AccessRequester, fosite.AccessResponder,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.populated++
	return nil
}

func TestRefreshGrantOnlyHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		grantTypes fosite.Arguments
		wantRun    bool
	}{
		{name: "refresh_token runs", grantTypes: fosite.Arguments{"refresh_token"}, wantRun: true},
		{name: "authorization_code skipped", grantTypes: fosite.Arguments{"authorization_code"}},
		{name: "device_code skipped", grantTypes: fosite.Arguments{"urn:ietf:params:oauth:grant-type:device_code"}},
		{name: "token exchange skipped", grantTypes: fosite.Arguments{"urn:ietf:params:oauth:grant-type:token-exchange"}},
		{name: "jwt-bearer skipped", grantTypes: fosite.Arguments{string(fosite.GrantTypeJWTBearer)}},
		{name: "refresh_token alongside another grant skipped", grantTypes: fosite.Arguments{"refresh_token", "authorization_code"}},
		{name: "no grant type skipped", grantTypes: fosite.Arguments{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			inner := &recordingRefreshHandler{}
			h := &refreshGrantOnlyHandler{inner: inner}
			req := fosite.NewAccessRequest(nil)
			req.GrantTypes = tt.grantTypes

			assert.False(t, h.CanSkipClientAuth(ctx, req), "client auth must never be skipped")
			assert.Equal(t, tt.wantRun, h.CanHandleTokenEndpointRequest(ctx, req))

			handleErr := h.HandleTokenEndpointRequest(ctx, req)
			populateErr := h.PopulateTokenEndpointResponse(ctx, req, fosite.NewAccessResponse())
			if tt.wantRun {
				require.NoError(t, handleErr)
				require.NoError(t, populateErr)
				assert.Len(t, inner.handledGrants(), 1)
				assert.Equal(t, 1, inner.populated)
				return
			}
			require.ErrorIs(t, handleErr, fosite.ErrUnknownRequest)
			require.ErrorIs(t, populateErr, fosite.ErrUnknownRequest)
			assert.Empty(t, inner.handledGrants())
			assert.Zero(t, inner.populated)
		})
	}

	t.Run("registered only as a token endpoint handler", func(t *testing.T) {
		t.Parallel()
		// An inner handler that also implements other fosite handler interfaces
		// must still not be registered for revocation, introspection or the
		// authorize endpoint: NewAuthorizationServer dispatches on the wrapper.
		var wrapped any = &refreshGrantOnlyHandler{inner: &recordingRefreshHandler{}}
		_, isRevocation := wrapped.(fosite.RevocationHandler)
		_, isIntrospector := wrapped.(fosite.TokenIntrospector)
		_, isAuthorize := wrapped.(fosite.AuthorizeEndpointHandler)
		_, isPAR := wrapped.(fosite.PushedAuthorizeEndpointHandler)
		assert.False(t, isRevocation)
		assert.False(t, isIntrospector)
		assert.False(t, isAuthorize)
		assert.False(t, isPAR)
	})

	t.Run("inner declining is honored", func(t *testing.T) {
		t.Parallel()
		h := &refreshGrantOnlyHandler{inner: decliningHandler{&recordingRefreshHandler{}}}
		req := fosite.NewAccessRequest(nil)
		req.GrantTypes = fosite.Arguments{"refresh_token"}
		assert.False(t, h.CanHandleTokenEndpointRequest(context.Background(), req))
		require.ErrorIs(t, h.HandleTokenEndpointRequest(context.Background(), req), fosite.ErrUnknownRequest)
	})
}

// decliningHandler never claims a request.
type decliningHandler struct{ *recordingRefreshHandler }

func (decliningHandler) CanHandleTokenEndpointRequest(context.Context, fosite.AccessRequester) bool {
	return false
}

// TestIntegration_RefreshGrantHandlers drives the token endpoint end to end:
// an embedder handler sees only the refresh_token grant, after the built-in
// refresh handler loaded the session, and refusing it answers invalid_grant
// without issuing tokens or rotating the refresh token.
func TestIntegration_RefreshGrantHandlers(t *testing.T) {
	t.Parallel()

	handler := &recordingRefreshHandler{}
	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m, withRefreshGrantHandlers(handler))

	verifier := servercrypto.GeneratePKCEVerifier()
	authCode, _ := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
		ClientID:     testClientID,
		RedirectURI:  testRedirectURI,
		State:        "refresh-grant-handler-state",
		Challenge:    servercrypto.ComputePKCEChallenge(verifier),
		Scope:        "openid profile offline_access",
		ResponseType: "code",
	})
	tokenData := exchangeCodeForTokens(t, ts.Server.URL, authCode, verifier, testAudience)
	refreshToken, ok := tokenData["refresh_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, refreshToken)

	assert.Empty(t, handler.handledGrants(), "handler must not run on the authorization_code grant")

	refresh := func() (int, map[string]interface{}) {
		resp := makeTokenRequest(t, ts.Server.URL, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {refreshToken},
			"client_id":     {testClientID},
		})
		defer resp.Body.Close()
		return resp.StatusCode, parseTokenResponse(t, resp)
	}

	// Refused: 400 invalid_grant, and no tokens.
	handler.setErr(fosite.ErrInvalidGrant.WithHint("upstream session is gone"))
	status, body := refresh()
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.NotContains(t, body, "access_token")
	assert.NotContains(t, body, "refresh_token")

	handler.mu.Lock()
	require.Len(t, handler.handled, 1)
	assert.Equal(t, fosite.Arguments{"refresh_token"}, handler.handled[0])
	require.NotNil(t, handler.sessions[0], "the built-in refresh handler must have loaded the session first")
	assert.Zero(t, handler.populated, "a refused grant must not reach the populate phase")
	handler.mu.Unlock()

	// Accepted: the same refresh token still works, so the refused attempt
	// did not rotate it.
	handler.setErr(nil)
	status, body = refresh()
	require.Equal(t, http.StatusOK, status, "refresh token must not have been rotated by the refused grant: %v", body)
	assert.NotEmpty(t, body["access_token"])
	assert.Len(t, handler.handledGrants(), 2)
}
