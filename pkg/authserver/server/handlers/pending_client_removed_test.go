// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// A pending authorization whose client is no longer served must stop at consent
// and at the upstream callback, before anything is exchanged upstream.
func TestPendingAuthorization_ClientNoLongerServed(t *testing.T) {
	t.Parallel()

	t.Run("consent", func(t *testing.T) {
		t.Parallel()
		hh, state, upstream, handle, cookie, _ := startConsentTest(t)
		delete(state.clients, testAuthClientID)

		rec := httptest.NewRecorder()
		hh.ConsentHandler(rec, consentRequest(handle, "approve", testAuthIssuer, cookie))

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "invalid_client")
		assert.Empty(t, rec.Header().Get("Location"))
		assert.Empty(t, upstream.capturedState)
	})

	tests := []struct {
		name       string
		query      string
		lookupErr  error
		wantStatus int
	}{
		{"callback", "code=upstream-code", nil, http.StatusBadRequest},
		{"upstream error", "error=access_denied", nil, http.StatusBadRequest},
		{"backend failure", "code=upstream-code", errors.New("storage unavailable"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, state, upstream := handlerTestSetup(t)
			cookie := bindPending(t, state, testInternalState, &storage.PendingAuthorization{
				ClientID: testAuthClientID, RedirectURI: testAuthRedirectURI, State: "client-state",
				PKCEChallenge: "challenge123", PKCEMethod: "S256", Scopes: []string{"openid"},
				InternalState: testInternalState, SessionID: "session-removed",
				UpstreamProviderName: "test-upstream", CreatedAt: time.Now(),
			})
			if tt.lookupErr != nil {
				state.clientErr = tt.lookupErr
			} else {
				delete(state.clients, testAuthClientID)
			}

			rec := httptest.NewRecorder()
			h.CallbackHandler(rec, newCallbackRequest(tt.query+"&state="+testInternalState, cookie))

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus == http.StatusBadRequest {
				assert.Contains(t, rec.Body.String(), "invalid_client")
			}
			assert.Empty(t, rec.Header().Get("Location"))
			assert.Empty(t, upstream.capturedCode, "no upstream code exchange")
			assert.Empty(t, state.upstreamTokens)
			assert.Empty(t, state.users)
		})
	}
}
