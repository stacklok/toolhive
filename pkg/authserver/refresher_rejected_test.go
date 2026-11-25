// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto/oautherr"
)

// countingIdP is a token endpoint that counts its calls and answers each with
// the configured status and body.
type countingIdP struct {
	server *httptest.Server
	calls  atomic.Int32
	status atomic.Int32
	body   atomic.Value
	// lastRefreshToken is the refresh_token form value of the latest request.
	lastRefreshToken atomic.Value
	// entered and release, when set before the first request, make each
	// request signal entered and then wait for release before it answers.
	entered chan struct{}
	release chan struct{}
}

func newCountingIdP(t *testing.T, status int, body string) *countingIdP {
	t.Helper()
	idp := &countingIdP{}
	idp.set(status, body)
	idp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idp.calls.Add(1)
		_ = r.ParseForm()
		idp.lastRefreshToken.Store(r.PostForm.Get("refresh_token"))
		if idp.entered != nil {
			select {
			case idp.entered <- struct{}{}:
			case <-time.After(5 * time.Second):
				http.Error(w, "test timeout signalling entered", http.StatusInternalServerError)
				return
			}
			select {
			case <-idp.release:
			case <-time.After(5 * time.Second):
				http.Error(w, "test timeout waiting for release", http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(idp.status.Load()))
		_, _ = w.Write([]byte(idp.body.Load().(string)))
	}))
	t.Cleanup(idp.server.Close)
	return idp
}

func (i *countingIdP) set(status int, body string) {
	i.status.Store(int32(status))
	i.body.Store(body)
}

func (i *countingIdP) provider(t *testing.T) upstream.OAuth2Provider {
	t.Helper()
	p, err := upstream.NewOAuth2Provider(&upstream.OAuth2Config{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:    "client",
			RedirectURI: "http://localhost/callback",
		},
		AuthorizationEndpoint: i.server.URL + "/authorize",
		TokenEndpoint:         i.server.URL + "/token",
	})
	require.NoError(t, err)
	return p
}

const (
	invalidGrantBody = `{"error":"invalid_grant","error_description":"refresh token revoked"}`
	refreshOKBody    = `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`
)

type rejectedRefreshEnv struct {
	idp       *countingIdP
	stor      *storage.MemoryStorage
	refresher *upstreamTokenRefresher
	clock     *fakeClock
}

func newRejectedRefreshEnv(t *testing.T, status int, body string) *rejectedRefreshEnv {
	t.Helper()
	idp := newCountingIdP(t, status, body)
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	clock := newFakeClock()
	memory, err := newRejectedRefreshes(rejectedRefreshesMaxEntries, rejectedRefreshesTTL, clock.Now)
	require.NoError(t, err)
	return &rejectedRefreshEnv{
		idp:   idp,
		stor:  stor,
		clock: clock,
		refresher: &upstreamTokenRefresher{
			providers:            map[string]upstream.OAuth2Provider{"idp": idp.provider(t)},
			storage:              stor,
			refreshTokenLifespan: time.Hour,
			rejected:             memory,
		},
	}
}

func rejectedRefreshRow(accessExpiry time.Time, refreshToken string) *storage.UpstreamTokens {
	return &storage.UpstreamTokens{
		ProviderID:       "idp",
		AccessToken:      "old-access",
		RefreshToken:     refreshToken,
		ExpiresAt:        accessExpiry,
		SessionExpiresAt: time.Now().Add(time.Hour),
		UserID:           "user",
		UpstreamSubject:  "subject",
		ClientID:         testClientID,
	}
}

func (e *rejectedRefreshEnv) store(t *testing.T, session string, row *storage.UpstreamTokens) {
	t.Helper()
	require.NoError(t, e.stor.StoreUpstreamTokens(context.Background(), session, "idp", row))
}

func TestUpstreamTokenRefresher_RemembersRejectedRefresh(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	t.Run("second refresh does not call the IdP and reports the remembered rejection", func(t *testing.T) {
		t.Parallel()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		row := rejectedRefreshRow(past, "dead-rt")
		env.store(t, "session", row)

		_, first := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, first, upstream.ErrGrantRejected)
		callsBefore := env.idp.calls.Load()
		require.Positive(t, callsBefore)

		_, second := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, second, upstream.ErrGrantRejected)
		assert.Equal(t, callsBefore, env.idp.calls.Load(), "a remembered rejection must not reach the IdP")
		assert.Contains(t, second.Error(), "remembered")
		assertRetrieveErrorInChain(t, second, http.StatusBadRequest)
	})

	t.Run("a rejection does not write the row", func(t *testing.T) {
		t.Parallel()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		row := rejectedRefreshRow(past, "dead-rt")
		env.store(t, "session", row)

		_, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)

		stored, err := env.stor.GetUpstreamTokens(ctx, "session", "idp")
		require.ErrorIs(t, err, storage.ErrExpired)
		require.NotNil(t, stored)
		assert.Equal(t, "dead-rt", stored.RefreshToken)
		assert.Equal(t, "old-access", stored.AccessToken)
	})

	t.Run("a successor refresh token is refreshed normally", func(t *testing.T) {
		t.Parallel()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		row := rejectedRefreshRow(past, "dead-rt")
		env.store(t, "session", row)
		_, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		callsBefore := env.idp.calls.Load()

		env.store(t, "session", rejectedRefreshRow(past, "successor-rt"))
		env.idp.set(http.StatusOK, refreshOKBody)

		got, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.NoError(t, err)
		assert.Equal(t, "new-access", got.AccessToken)
		assert.Greater(t, env.idp.calls.Load(), callsBefore)
		assert.Equal(t, "successor-rt", env.idp.lastRefreshToken.Load())
	})

	t.Run("the IdP is called again after the TTL", func(t *testing.T) {
		t.Parallel()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		row := rejectedRefreshRow(past, "dead-rt")
		env.store(t, "session", row)
		_, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)

		callsBefore := env.idp.calls.Load()
		env.clock.Advance(rejectedRefreshesTTL - time.Second)
		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		require.Equal(t, callsBefore, env.idp.calls.Load())

		env.clock.Advance(time.Second)
		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		assert.Greater(t, env.idp.calls.Load(), callsBefore)
		assertRetrieveErrorInChain(t, err, http.StatusBadRequest)
	})

	t.Run("rows with the same refresh token do not share an entry", func(t *testing.T) {
		t.Parallel()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		row := rejectedRefreshRow(past, "shared-rt")
		env.store(t, "session-a", row)
		env.store(t, "session-b", row)

		_, err := env.refresher.RefreshAndStore(ctx, "session-a", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		callsBefore := env.idp.calls.Load()

		_, err = env.refresher.RefreshAndStore(ctx, "session-b", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		assert.Greater(t, env.idp.calls.Load(), callsBefore)
	})

	t.Run("the row-read-OK path uses the memory", func(t *testing.T) {
		t.Parallel()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		// An unexpired row identical to the caller's is a forced refresh (for
		// example an expired ID token), so the re-read does not short-circuit.
		row := rejectedRefreshRow(time.Now().Add(time.Hour), "dead-rt")
		env.store(t, "session", row)

		_, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		callsBefore := env.idp.calls.Load()
		require.Positive(t, callsBefore)
		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		assert.Equal(t, callsBefore, env.idp.calls.Load())
	})

	t.Run("the ErrExpired path uses the memory", func(t *testing.T) {
		t.Parallel()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		row := rejectedRefreshRow(past, "dead-rt")
		env.store(t, "session", row)
		_, err := env.stor.GetUpstreamTokens(ctx, "session", "idp")
		require.ErrorIs(t, err, storage.ErrExpired, "the fixture must exercise the ErrExpired path")

		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		callsBefore := env.idp.calls.Load()
		require.Positive(t, callsBefore)
		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		assert.Equal(t, callsBefore, env.idp.calls.Load())
	})
}

func TestUpstreamTokenRefresher_RememberedRejectionsEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	switchTo := upstream.ClientCredentials{ClientID: "replacement"}

	newSwitchEnv := func(t *testing.T) (*rejectedRefreshEnv, *server) {
		t.Helper()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		srv := &server{
			upstreams:             []handlers.NamedUpstream{{Name: "idp", Provider: env.refresher.providers["idp"]}},
			upstreamRefresher:     env.refresher,
			upstreamRefresherImpl: env.refresher,
		}
		return env, srv
	}

	t.Run("a client registration switch through the server", func(t *testing.T) {
		t.Parallel()
		env, srv := newSwitchEnv(t)
		row := rejectedRefreshRow(past, "dead-rt")
		env.store(t, "session", row)
		_, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		callsBefore := env.idp.calls.Load()
		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		require.Equal(t, callsBefore, env.idp.calls.Load())

		require.NoError(t, srv.UpdateUpstreamClientCredentials("idp", switchTo))

		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		assert.Greater(t, env.idp.calls.Load(), callsBefore)
	})

	t.Run("a failed switch keeps the memory", func(t *testing.T) {
		t.Parallel()
		env, srv := newSwitchEnv(t)
		row := rejectedRefreshRow(past, "dead-rt")
		env.store(t, "session", row)
		_, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		callsBefore := env.idp.calls.Load()

		require.Error(t, srv.UpdateUpstreamClientCredentials("idp", upstream.ClientCredentials{}))
		require.Error(t, srv.UpdateUpstreamClientCredentials("missing", switchTo))
		assert.Zero(t, env.refresher.clientGeneration.Load())

		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		assert.Equal(t, callsBefore, env.idp.calls.Load())
	})

	t.Run("a switch while the IdP call is in flight", func(t *testing.T) {
		t.Parallel()
		env, srv := newSwitchEnv(t)
		env.idp.entered = make(chan struct{})
		env.idp.release = make(chan struct{})
		row := rejectedRefreshRow(past, "dead-rt")
		env.store(t, "session", row)

		done := make(chan error, 1)
		go func() {
			_, err := env.refresher.RefreshAndStore(ctx, "session", row)
			done <- err
		}()
		select {
		case <-env.idp.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for the IdP call to start")
		}
		require.NoError(t, srv.UpdateUpstreamClientCredentials("idp", switchTo))
		select {
		case env.idp.release <- struct{}{}:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout releasing the IdP call")
		}
		select {
		case err := <-done:
			require.ErrorIs(t, err, upstream.ErrGrantRejected)
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for the refresh to finish")
		}
		callsBefore := env.idp.calls.Load()

		// The rejection was filed under the generation read before the call.
		env.idp.entered = nil
		_, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		assert.Greater(t, env.idp.calls.Load(), callsBefore)
	})

	t.Run("a non-rotating refresh elsewhere stores a new access token", func(t *testing.T) {
		t.Parallel()
		env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
		row := rejectedRefreshRow(past, "same-rt")
		env.store(t, "session", row)
		_, err := env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		callsBefore := env.idp.calls.Load()
		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		require.Equal(t, callsBefore, env.idp.calls.Load())

		// Another replica refreshed without rotating: same refresh token, a
		// new access token that has already expired.
		other := rejectedRefreshRow(past, "same-rt")
		other.AccessToken = "other-replica-access"
		env.store(t, "session", other)

		_, err = env.refresher.RefreshAndStore(ctx, "session", row)
		require.ErrorIs(t, err, upstream.ErrGrantRejected)
		assert.Greater(t, env.idp.calls.Load(), callsBefore)
	})
}

func TestUpstreamTokenRefresher_RememberedErrorIsFresh(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
	row := rejectedRefreshRow(time.Now().Add(-time.Minute), "dead-rt")
	env.store(t, "session", row)
	_, fresh := env.refresher.RefreshAndStore(ctx, "session", row)
	require.ErrorIs(t, fresh, upstream.ErrGrantRejected)
	assert.NotContains(t, fresh.Error(), "remembered")

	_, first := env.refresher.RefreshAndStore(ctx, "session", row)
	_, second := env.refresher.RefreshAndStore(ctx, "session", row)
	require.ErrorIs(t, first, upstream.ErrGrantRejected)
	assert.Contains(t, first.Error(), "remembered")
	assert.True(t, oautherr.IsRejectedRefreshGrant(first))
	assert.NotSame(t, first, second)
	assert.NotContains(t, first.Error(), "refresh token revoked", "the provider's text is not kept")
}

func TestUpstreamTokenRefresher_DoesNotRememberOtherFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "5xx", status: http.StatusServiceUnavailable, body: `{"error":"invalid_grant"}`},
		{name: "429", status: http.StatusTooManyRequests, body: ""},
		{name: "invalid_client", status: http.StatusUnauthorized, body: `{"error":"invalid_client"}`},
		{name: "invalid_scope", status: http.StatusBadRequest, body: `{"error":"invalid_scope"}`},
		{name: "interaction_required", status: http.StatusBadRequest, body: `{"error":"interaction_required"}`},
		{name: "non-OAuth error page", status: http.StatusForbidden, body: `<html>blocked</html>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newRejectedRefreshEnv(t, tt.status, tt.body)
			row := rejectedRefreshRow(time.Now().Add(-time.Minute), "rt")
			env.store(t, "session", row)

			_, err := env.refresher.RefreshAndStore(ctx, "session", row)
			require.Error(t, err)
			callsBefore := env.idp.calls.Load()
			_, err = env.refresher.RefreshAndStore(ctx, "session", row)
			require.Error(t, err)
			assert.Greater(t, env.idp.calls.Load(), callsBefore, "the IdP must be called every time")
		})
	}
}

func TestUpstreamTokenRefresher_DoesNotRememberNetworkFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
	env.idp.server.Close()
	row := rejectedRefreshRow(time.Now().Add(-time.Minute), "rt")
	env.store(t, "session", row)

	_, err := env.refresher.RefreshAndStore(ctx, "session", row)
	require.Error(t, err)
	require.NotErrorIs(t, err, upstream.ErrGrantRejected)
	// The server is closed, so nothing is counted; the second call must fail
	// the same way rather than return a remembered error.
	_, err = env.refresher.RefreshAndStore(ctx, "session", row)
	require.Error(t, err)
	assert.NotErrorIs(t, err, upstream.ErrGrantRejected)
}

func TestUpstreamRefreshRejection_ServiceUsesMemory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
	env.store(t, "session", rejectedRefreshRow(time.Now().Add(-time.Minute), "dead-rt"))
	svc := upstreamtoken.NewInProcessService(env.stor, env.refresher)

	var callsBefore int32
	for i := range 2 {
		creds, failed, err := svc.GetAllUpstreamCredentials(ctx, "session")
		require.NoError(t, err)
		assert.Empty(t, creds)
		require.Contains(t, failed, "idp", "call %d", i)
		require.ErrorIs(t, failed["idp"], upstream.ErrGrantRejected)
		if i == 0 {
			callsBefore = env.idp.calls.Load()
			require.Positive(t, callsBefore)
		}
	}
	assert.Equal(t, callsBefore, env.idp.calls.Load())
}

func TestUpstreamRefreshGuard_RefusesOnRememberedRejection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
	row := guardTestRow(time.Now().Add(-time.Minute), "dead-rt")
	require.NoError(t, env.stor.StoreUpstreamTokens(ctx, guardTestTSID, "idp", row))

	// A bearer request refreshes first and gets the rejection.
	_, err := env.refresher.RefreshAndStore(ctx, guardTestTSID, row)
	require.ErrorIs(t, err, upstream.ErrGrantRejected)
	callsBefore := env.idp.calls.Load()
	require.Positive(t, callsBefore)

	guard, err := newUpstreamRefreshGuard(env.stor, env.refresher, []string{"idp"})
	require.NoError(t, err)
	guard.recheckDelay = 0

	err = guard.HandleTokenEndpointRequest(ctx, guardTestRequest(guardTestTSID, "refresh_token"))
	require.ErrorIs(t, err, fosite.ErrInvalidGrant)
	assert.Equal(t, callsBefore, env.idp.calls.Load(), "the guard must be answered from the memory")

	_, err = env.stor.GetUpstreamTokens(ctx, guardTestTSID, "idp")
	assert.ErrorIs(t, err, storage.ErrNotFound, "the confirmed rejection drops the session's rows")
}

func TestUpstreamRefreshGuard_GrantsOnNonRotatingRefreshDuringRecheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// winner is what another replica's non-rotating refresh stores after
		// the guard's refresh was answered from the memory.
		winner func() *storage.UpstreamTokens
	}{
		{
			name: "new access token",
			winner: func() *storage.UpstreamTokens {
				w := guardTestRow(time.Now().Add(time.Hour), "same-rt")
				w.AccessToken = "other-replica-access"
				return w
			},
		},
		{
			name: "same access token with a new expiry",
			winner: func() *storage.UpstreamTokens {
				return guardTestRow(time.Now().Add(time.Hour), "same-rt")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
			row := guardTestRow(time.Now().Add(-time.Minute), "same-rt")
			require.NoError(t, env.stor.StoreUpstreamTokens(ctx, guardTestTSID, "idp", row))
			_, err := env.refresher.RefreshAndStore(ctx, guardTestTSID, row)
			require.ErrorIs(t, err, upstream.ErrGrantRejected)
			callsBefore := env.idp.calls.Load()
			require.Positive(t, callsBefore)

			// The winner's row lands after the refresher answered from the memory
			// and before the guard's recheck read, with no timing involved.
			refresher := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
			refresher.EXPECT().RefreshAndStore(gomock.Any(), guardTestTSID, gomock.Any()).DoAndReturn(
				func(ctx context.Context, sessionID string, expired *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
					tokens, err := env.refresher.RefreshAndStore(ctx, sessionID, expired)
					require.ErrorIs(t, err, upstream.ErrGrantRejected)
					require.NoError(t, env.stor.StoreUpstreamTokens(ctx, guardTestTSID, "idp", tt.winner()))
					return tokens, err
				})

			guard, err := newUpstreamRefreshGuard(env.stor, refresher, []string{"idp"})
			require.NoError(t, err)
			guard.recheckDelay = 0

			require.NoError(t, guard.HandleTokenEndpointRequest(ctx, guardTestRequest(guardTestTSID, "refresh_token")))

			assert.Equal(t, callsBefore, env.idp.calls.Load(), "the guard must be answered from the memory")
			got, err := env.stor.GetUpstreamTokens(ctx, guardTestTSID, "idp")
			require.NoError(t, err, "the winner's row must not be deleted")
			assert.Equal(t, tt.winner().AccessToken, got.AccessToken)
			assert.True(t, got.ExpiresAt.After(time.Now()))
		})
	}
}

func TestUpstreamTokenRefresher_RememberedRejectionEndsOnNewExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	env := newRejectedRefreshEnv(t, http.StatusBadRequest, invalidGrantBody)
	row := rejectedRefreshRow(time.Now().Add(-time.Minute), "same-rt")
	env.store(t, "session", row)
	_, err := env.refresher.RefreshAndStore(ctx, "session", row)
	require.ErrorIs(t, err, upstream.ErrGrantRejected)
	callsBefore := env.idp.calls.Load()
	require.Positive(t, callsBefore)

	// A non-rotating refresh elsewhere re-issued the same access token string
	// with a different expiry that is still in the past.
	overwritten := rejectedRefreshRow(time.Now().Add(-30*time.Second), "same-rt")
	env.store(t, "session", overwritten)

	_, err = env.refresher.RefreshAndStore(ctx, "session", overwritten)
	require.ErrorIs(t, err, upstream.ErrGrantRejected)
	assert.Greater(t, env.idp.calls.Load(), callsBefore, "a new expiry must end the remembered rejection")
}

func TestNewUpstreamTokenRefresher_RemembersRejections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	idp := newCountingIdP(t, http.StatusBadRequest, invalidGrantBody)
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	row := rejectedRefreshRow(time.Now().Add(-time.Minute), "dead-rt")
	require.NoError(t, stor.StoreUpstreamTokens(ctx, "session", "idp", row))

	refresher := newUpstreamTokenRefresher(
		[]handlers.NamedUpstream{{Name: "idp", Provider: idp.provider(t)}}, stor, time.Hour)
	_, err := refresher.RefreshAndStore(ctx, "session", row)
	require.ErrorIs(t, err, upstream.ErrGrantRejected)
	callsBefore := idp.calls.Load()
	_, err = refresher.RefreshAndStore(ctx, "session", row)
	require.ErrorIs(t, err, upstream.ErrGrantRejected)
	assert.Equal(t, callsBefore, idp.calls.Load())
}
