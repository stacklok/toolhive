// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
)

// recordingRefreshHandler is an embedder token endpoint handler that claims
// every request (unless decline is set) and records what it was handed, so
// tests can assert the refreshGrantValidators gate rather than the handler's
// own filtering.
type recordingRefreshHandler struct {
	mu        sync.Mutex
	decline   bool
	handled   []fosite.Arguments
	tsids     []string
	populated int
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

func (h *recordingRefreshHandler) seenTSIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.tsids...)
}

func (h *recordingRefreshHandler) populateCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.populated
}

func (*recordingRefreshHandler) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return true
}

func (h *recordingRefreshHandler) CanHandleTokenEndpointRequest(context.Context, fosite.AccessRequester) bool {
	return !h.decline
}

func (h *recordingRefreshHandler) HandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handled = append(h.handled, requester.GetGrantTypes())
	// Only the built-in refresh handler replaces the token endpoint's
	// placeholder session with the stored refresh session carrying the tsid.
	if sess, ok := requester.GetSession().(*session.Session); ok {
		h.tsids = append(h.tsids, sess.GetIDPSessionID())
	}
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

func TestRefreshGrantValidators_GrantTypes(t *testing.T) {
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
			v := &refreshGrantValidators{handlers: []fosite.TokenEndpointHandler{inner}}
			req := fosite.NewAccessRequest(nil)
			req.GrantTypes = tt.grantTypes

			assert.False(t, v.CanSkipClientAuth(ctx, req), "client auth must never be skipped")
			assert.Equal(t, tt.wantRun, v.CanHandleTokenEndpointRequest(ctx, req))

			handleErr := v.HandleTokenEndpointRequest(ctx, req)
			// The response phase is never delegated, whatever the grant.
			require.ErrorIs(t, v.PopulateTokenEndpointResponse(ctx, req, fosite.NewAccessResponse()), fosite.ErrUnknownRequest)
			assert.Zero(t, inner.populateCalls())
			if tt.wantRun {
				require.NoError(t, handleErr)
				assert.Len(t, inner.handledGrants(), 1)
				return
			}
			require.ErrorIs(t, handleErr, fosite.ErrUnknownRequest)
			assert.Empty(t, inner.handledGrants())
		})
	}
}

func TestRefreshGrantValidators_HandlerChain(t *testing.T) {
	t.Parallel()

	refusal := fosite.ErrInvalidGrant.WithHint("refused")
	tests := []struct {
		name     string
		handlers []*recordingRefreshHandler
		wantErr  error
		wantRuns []int
	}{
		{
			name:     "all accept, all run",
			handlers: []*recordingRefreshHandler{{}, {}},
			wantRuns: []int{1, 1},
		},
		{
			name:     "second refuses",
			handlers: []*recordingRefreshHandler{{}, {err: refusal}},
			wantErr:  fosite.ErrInvalidGrant,
			wantRuns: []int{1, 1},
		},
		{
			name:     "first refuses, later handlers do not run",
			handlers: []*recordingRefreshHandler{{err: refusal}, {}},
			wantErr:  fosite.ErrInvalidGrant,
			wantRuns: []int{1, 0},
		},
		{
			name:     "declining handler is skipped",
			handlers: []*recordingRefreshHandler{{decline: true, err: refusal}, {}},
			wantRuns: []int{0, 1},
		},
		{
			name:     "ErrUnknownRequest is no objection",
			handlers: []*recordingRefreshHandler{{err: fosite.ErrUnknownRequest}, {}},
			wantRuns: []int{1, 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handlers := make([]fosite.TokenEndpointHandler, len(tt.handlers))
			for i, h := range tt.handlers {
				handlers[i] = h
			}
			v := &refreshGrantValidators{handlers: handlers}
			req := fosite.NewAccessRequest(nil)
			req.GrantTypes = fosite.Arguments{"refresh_token"}

			err := v.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			for i, h := range tt.handlers {
				assert.Len(t, h.handledGrants(), tt.wantRuns[i], "handler %d", i)
			}
		})
	}

	t.Run("registered only as a token endpoint handler", func(t *testing.T) {
		t.Parallel()
		// The handlers must not be registered for revocation, introspection or
		// the authorize endpoint: NewAuthorizationServer dispatches on this type.
		var wrapped any = &refreshGrantValidators{}
		_, isRevocation := wrapped.(fosite.RevocationHandler)
		_, isIntrospector := wrapped.(fosite.TokenIntrospector)
		_, isAuthorize := wrapped.(fosite.AuthorizeEndpointHandler)
		_, isPAR := wrapped.(fosite.PushedAuthorizeEndpointHandler)
		assert.False(t, isRevocation)
		assert.False(t, isIntrospector)
		assert.False(t, isAuthorize)
		assert.False(t, isPAR)
	})
}

// TestIntegration_RefreshGrantHandlers drives the token endpoint end to end
// with two handlers: both see only the refresh_token grant, after the
// built-in refresh handler loaded the session, and a refusal from the second
// answers invalid_grant (or 500 for a plain error) without issuing tokens or
// rotating the refresh token.
func TestIntegration_RefreshGrantHandlers(t *testing.T) {
	t.Parallel()

	first := &recordingRefreshHandler{}
	second := &recordingRefreshHandler{}
	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m, withRefreshGrantHandlers(first, second))

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
	accessToken, ok := tokenData["access_token"].(string)
	require.True(t, ok)
	tsid := extractTSID(t, accessToken, ts.PrivateKey.Public())

	assert.Empty(t, first.handledGrants(), "handlers must not run on the authorization_code grant")
	assert.Empty(t, second.handledGrants(), "handlers must not run on the authorization_code grant")

	refresh := func() (int, map[string]interface{}) {
		resp := makeTokenRequest(t, ts.Server.URL, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {refreshToken},
			"client_id":     {testClientID},
		})
		defer resp.Body.Close()
		return resp.StatusCode, parseTokenResponse(t, resp)
	}

	// Refused by the second handler with invalid_grant: 400 and no tokens.
	second.setErr(fosite.ErrInvalidGrant.WithHint("upstream session is gone"))
	status, body := refresh()
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.NotContains(t, body, "access_token")
	assert.NotContains(t, body, "refresh_token")
	assert.Equal(t, []fosite.Arguments{{"refresh_token"}}, second.handledGrants(),
		"the second handler must be registered and run")
	assert.Equal(t, []string{tsid}, first.seenTSIDs(),
		"handlers must run after the built-in refresh handler loaded the stored session")

	// Refused with a plain error: 500, not invalid_grant, and no tokens.
	second.setErr(errors.New("boom"))
	status, body = refresh()
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.NotEqual(t, "invalid_grant", body["error"])
	assert.NotContains(t, body, "access_token")

	// Accepted: the same refresh token still works, so neither refusal
	// rotated it.
	second.setErr(nil)
	status, body = refresh()
	require.Equal(t, http.StatusOK, status, "refresh token must not have been rotated by a refused grant: %v", body)
	assert.NotEmpty(t, body["access_token"])
	assert.Len(t, first.handledGrants(), 3)
	assert.Len(t, second.handledGrants(), 3)
	assert.Zero(t, first.populateCalls(), "the seam must never reach a handler's response phase")
	assert.Zero(t, second.populateCalls(), "the seam must never reach a handler's response phase")
}
