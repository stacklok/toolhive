// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

const (
	guardTestTSID      = "guard-tsid"
	guardTestRequestID = "grant-request-id"
)

func guardTestRow(expiresAt time.Time, refreshToken string) *storage.UpstreamTokens {
	return &storage.UpstreamTokens{
		ProviderID:       "idp",
		AccessToken:      "upstream-access",
		RefreshToken:     refreshToken,
		ExpiresAt:        expiresAt,
		SessionExpiresAt: time.Now().Add(time.Hour),
		UserID:           "user",
		UpstreamSubject:  "subject",
		ClientID:         testClientID,
	}
}

func guardTestRequest(tsid string, grantTypes ...string) *fosite.AccessRequest {
	req := fosite.NewAccessRequest(session.New("user", tsid, testClientID, session.UserClaims{}))
	req.GrantTypes = grantTypes
	req.Client = &fosite.DefaultClient{ID: testClientID}
	req.SetID(guardTestRequestID)
	return req
}

func TestUpstreamRefreshGuard(t *testing.T) {
	t.Parallel()

	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	rejected := fmt.Errorf("upstream token refresh failed: %w", upstream.ErrGrantRejected)

	tests := []struct {
		name string
		tsid string
		rows map[string]*storage.UpstreamTokens
		// refreshErr, when refreshes is non-zero, is what the refresher returns;
		// nil means the refresh succeeds.
		refreshErr error
		refreshes  int
		// onRefresh, when set, is stored as the row before the refresher
		// returns, as a replica winning a refresh race would write it.
		onRefresh *storage.UpstreamTokens
		// winnerAfter, when set, is stored as the row 50ms after the refresher
		// returns, while the guard waits out recheckDelay.
		winnerAfter *storage.UpstreamTokens
		// deleteOnRefresh deletes the row after the refresher runs.
		deleteOnRefresh bool
		// unconfigured names a row's provider that is not configured.
		unconfigured string
		// cancelDuringRecheck cancels the request 50ms after the refresher
		// returns, while the guard waits out recheckDelay.
		cancelDuringRecheck bool
		// cancelled runs the request on an already cancelled context.
		cancelled bool
		// hang makes the refresher block until the test ends, so only the
		// guard's check timeout can end the wait.
		hang         bool
		recheckDelay time.Duration
		wantRefuse   bool
		wantRevoke   bool
		// wantDropped expects every upstream row of the session deleted, and a
		// replay of the refresh token refused without another refresh.
		wantDropped bool
	}{
		{
			name: "session-less grant is not checked",
			tsid: "",
		},
		{
			name:       "no upstream rows is refused without revoking",
			tsid:       guardTestTSID,
			wantRefuse: true,
		},
		{
			name: "unexpired row is granted without a refresh",
			tsid: guardTestTSID,
			rows: map[string]*storage.UpstreamTokens{"idp": guardTestRow(future, "rt")},
		},
		{
			name: "non-expiring row is granted without a refresh",
			tsid: guardTestTSID,
			rows: map[string]*storage.UpstreamTokens{"idp": guardTestRow(time.Time{}, "")},
		},
		{
			name:       "expired row without a refresh token is refused and revoked",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "")},
			wantRefuse: true,
			wantRevoke: true,
		},
		{
			name:      "expired row whose upstream refresh succeeds is granted",
			tsid:      guardTestTSID,
			rows:      map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshes: 1,
		},
		{
			name:       "transient upstream failure is granted",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr: errors.New("upstream token refresh failed: 503 Service Unavailable"),
			refreshes:  1,
		},
		{
			name:        "upstream invalid_grant is refused without revoking and drops the rows",
			tsid:        guardTestTSID,
			rows:        map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr:  rejected,
			refreshes:   1,
			wantRefuse:  true,
			wantDropped: true,
		},
		{
			name: "upstream invalid_grant drops the live sibling providers' rows too",
			tsid: guardTestTSID,
			rows: map[string]*storage.UpstreamTokens{
				"a-rejected": guardTestRow(past, "rt-a"),
				"b-live":     guardTestRow(future, "rt-b"),
			},
			refreshErr:  rejected,
			refreshes:   1,
			wantRefuse:  true,
			wantDropped: true,
		},
		{
			name:            "upstream invalid_grant with the row deleted is refused without revoking",
			tsid:            guardTestTSID,
			rows:            map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr:      rejected,
			refreshes:       1,
			deleteOnRefresh: true,
			wantRefuse:      true,
		},
		{
			name:       "row deleted during the refresh is refused without revoking",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr: fmt.Errorf("failed to read upstream tokens: %w", storage.ErrNotFound),
			refreshes:  1,
			wantRefuse: true,
		},
		{
			name:       "upstream invalid_grant after another replica rotated the row is granted",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr: rejected,
			refreshes:  1,
			onRefresh:  guardTestRow(future, "rotated-rt"),
		},
		{
			name:       "upstream invalid_grant with the row rotated but already expired is granted",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr: rejected,
			refreshes:  1,
			onRefresh:  guardTestRow(past, "rotated-rt"),
		},
		{
			name:         "upstream invalid_grant whose winner lands during the recheck delay is granted",
			tsid:         guardTestTSID,
			rows:         map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr:   rejected,
			refreshes:    1,
			winnerAfter:  guardTestRow(future, "rotated-rt"),
			recheckDelay: 200 * time.Millisecond,
		},
		{
			name:                "upstream invalid_grant with the request cancelled during the recheck is granted",
			tsid:                guardTestTSID,
			rows:                map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr:          rejected,
			refreshes:           1,
			cancelDuringRecheck: true,
			recheckDelay:        time.Minute,
		},
		{
			name:      "cancelled request skips the upstream refresh and is granted",
			tsid:      guardTestTSID,
			rows:      map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			cancelled: true,
		},
		{
			name:      "upstream refresh outliving the check timeout is granted",
			tsid:      guardTestTSID,
			rows:      map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshes: 1,
			hang:      true,
		},
		{
			name:         "expired row for an unconfigured provider is refused without revoking",
			tsid:         guardTestTSID,
			rows:         map[string]*storage.UpstreamTokens{"removed": guardTestRow(past, "rt")},
			unconfigured: "removed",
			wantRefuse:   true,
		},
		{
			name:         "unexpired row for an unconfigured provider is granted",
			tsid:         guardTestTSID,
			rows:         map[string]*storage.UpstreamTokens{"removed": guardTestRow(future, "rt")},
			unconfigured: "removed",
		},
		{
			name: "rows without an access token are refused and revoked",
			tsid: guardTestTSID,
			rows: map[string]*storage.UpstreamTokens{"idp": func() *storage.UpstreamTokens {
				row := guardTestRow(future, "rt")
				row.AccessToken = ""
				return row
			}()},
			wantRefuse: true,
			wantRevoke: true,
		},
		{
			name: "one dead provider among live ones is refused and revoked",
			tsid: guardTestTSID,
			rows: map[string]*storage.UpstreamTokens{
				"a-live": guardTestRow(future, "rt-a"),
				"b-dead": guardTestRow(past, ""),
			},
			wantRefuse: true,
			wantRevoke: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })
			var configured []string
			for name, row := range tt.rows {
				row.ProviderID = name
				require.NoError(t, stor.StoreUpstreamTokens(ctx, guardTestTSID, name, row))
				if name != tt.unconfigured {
					configured = append(configured, name)
				}
			}
			// The grant's own tokens, which a revoking refusal must revoke.
			req := guardTestRequest(tt.tsid, "refresh_token")
			require.NoError(t, stor.CreateRefreshTokenSession(ctx, "rt-sig", "at-sig", req))
			require.NoError(t, stor.CreateAccessTokenSession(ctx, "at-sig", req))

			reqCtx, cancelReq := context.WithCancel(ctx)
			t.Cleanup(cancelReq)
			// Registered after the storage cleanup, so it runs first and a hung
			// refresher returns before the storage closes.
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })

			refresher := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
			refresher.EXPECT().RefreshAndStore(gomock.Any(), guardTestTSID, gomock.Any()).Times(tt.refreshes).DoAndReturn(
				func(_ context.Context, sessionID string, expired *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
					if tt.hang {
						<-release
					}
					if tt.deleteOnRefresh {
						require.NoError(t, stor.DeleteUpstreamTokensForProvider(ctx, sessionID, expired.ProviderID))
					}
					if tt.onRefresh != nil {
						winner := *tt.onRefresh
						winner.ProviderID = expired.ProviderID
						require.NoError(t, stor.StoreUpstreamTokens(ctx, sessionID, expired.ProviderID, &winner))
					}
					if tt.winnerAfter != nil {
						winner := *tt.winnerAfter
						winner.ProviderID = expired.ProviderID
						time.AfterFunc(50*time.Millisecond, func() {
							assert.NoError(t, stor.StoreUpstreamTokens(ctx, sessionID, expired.ProviderID, &winner))
						})
					}
					if tt.cancelDuringRecheck {
						time.AfterFunc(50*time.Millisecond, cancelReq)
					}
					if tt.refreshErr != nil {
						return nil, tt.refreshErr
					}
					refreshed := *expired
					refreshed.ExpiresAt = future
					return &refreshed, nil
				})
			guard, err := newUpstreamRefreshGuard(stor, refresher, configured)
			require.NoError(t, err)
			guard.recheckDelay = tt.recheckDelay
			if tt.hang {
				guard.checkTimeout = 50 * time.Millisecond
			}

			if tt.cancelled {
				cancelReq()
			}
			err = guard.HandleTokenEndpointRequest(reqCtx, req)

			_, refreshErr := stor.GetRefreshTokenSession(ctx, "rt-sig", nil)
			_, accessErr := stor.GetAccessTokenSession(ctx, "at-sig", nil)
			if tt.wantRefuse {
				require.ErrorIs(t, err, fosite.ErrInvalidGrant)
				var rfcErr *fosite.RFC6749Error
				require.ErrorAs(t, err, &rfcErr)
				assert.Equal(t, http.StatusBadRequest, rfcErr.CodeField)
			} else {
				require.NoError(t, err)
			}
			rows, rowsErr := stor.GetAllUpstreamTokens(ctx, guardTestTSID)
			require.NoError(t, rowsErr)
			if tt.wantDropped {
				assert.Empty(t, rows, "a confirmed rejection must drop every upstream row of the session")
				// The refresher mock allows no further call, so a replay that
				// reached the IdP again fails the test.
				require.ErrorIs(t, guard.HandleTokenEndpointRequest(ctx, req), fosite.ErrInvalidGrant)
			} else if len(tt.rows) > 0 && !tt.deleteOnRefresh {
				assert.NotEmpty(t, rows, "only a confirmed rejection drops the upstream rows")
			}
			if tt.wantRevoke {
				assert.Error(t, refreshErr, "refused refresh token must be revoked")
				assert.Error(t, accessErr, "the refused grant's access token must be revoked")
				return
			}
			assert.NoError(t, refreshErr, "the refresh token must not be revoked")
			assert.NoError(t, accessErr, "the access token must not be revoked")
		})
	}
}

func TestUpstreamRefreshGuard_Gates(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	refresher := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))

	t.Run("constructor requires storage and a refresher and wires the delays", func(t *testing.T) {
		t.Parallel()
		_, err := newUpstreamRefreshGuard(nil, refresher, nil)
		require.Error(t, err)
		_, err = newUpstreamRefreshGuard(stor, nil, nil)
		require.Error(t, err)

		guard, err := newUpstreamRefreshGuard(stor, refresher, []string{"idp"})
		require.NoError(t, err)
		assert.Equal(t, rejectionRecheckDelay, guard.recheckDelay)
		assert.Equal(t, guardCheckTimeout, guard.checkTimeout)
	})

	t.Run("runs only on the refresh_token grant and never skips client auth", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		guard, err := newUpstreamRefreshGuard(stor, refresher, []string{"idp"})
		require.NoError(t, err)

		req := guardTestRequest(guardTestTSID, "authorization_code")
		assert.False(t, guard.CanHandleTokenEndpointRequest(ctx, req))
		require.ErrorIs(t, guard.HandleTokenEndpointRequest(ctx, req), fosite.ErrUnknownRequest)
		assert.False(t, guard.CanSkipClientAuth(ctx, req))
		require.ErrorIs(t, guard.PopulateTokenEndpointResponse(ctx, req, fosite.NewAccessResponse()), fosite.ErrUnknownRequest)
		assert.True(t, guard.CanHandleTokenEndpointRequest(ctx, guardTestRequest(guardTestTSID, "refresh_token")))
	})
}

func TestRefreshGrantValidatorChain(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	refresher := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
	upstreams := []handlers.NamedUpstream{{Name: "first"}, {Name: "second"}}

	tests := []struct {
		name      string
		disabled  bool
		refresher storage.UpstreamTokenRefresher
		wantGuard bool
	}{
		{name: "guard runs before the embedder handlers", refresher: refresher, wantGuard: true},
		{name: "disabled guard leaves only the embedder handlers", disabled: true, refresher: refresher},
		{name: "no refresher (no upstreams) leaves only the embedder handlers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, second := &recordingRefreshHandler{}, &recordingRefreshHandler{}
			// Spare capacity, so appending to the embedder's slice would write
			// into its backing array.
			embedders := make([]fosite.TokenEndpointHandler, 2, 4)
			embedders[0], embedders[1] = first, second
			cfg := Config{RefreshGrantHandlers: embedders, DisableUpstreamRefreshGuard: tt.disabled}

			chain, err := refreshGrantValidatorChain(cfg, stor, tt.refresher, upstreams)
			require.NoError(t, err)

			assert.Len(t, cfg.RefreshGrantHandlers, 2)
			assert.Nil(t, embedders[:3][2], "the embedder's slice must not be written")
			if !tt.wantGuard {
				assert.Equal(t, []fosite.TokenEndpointHandler{first, second}, chain)
				return
			}
			require.Len(t, chain, 3)
			guard, ok := chain[0].(*upstreamRefreshGuard)
			require.True(t, ok, "the guard must run first")
			assert.Equal(t, map[string]struct{}{"first": {}, "second": {}}, guard.providers)
			assert.Same(t, first, chain[1])
			assert.Same(t, second, chain[2])
		})
	}
}

// TestUpstreamRefreshGuard_StorageFailures pins the fail-open storage paths: a
// storage fault never refuses a grant on its own, a failed revocation still
// refuses one already found dead, and revocation outlives a cancelled request.
func TestUpstreamRefreshGuard_StorageFailures(t *testing.T) {
	t.Parallel()

	storageErr := errors.New("storage unavailable")
	rejected := fmt.Errorf("upstream token refresh failed: %w", upstream.ErrGrantRejected)
	expired := guardTestRow(time.Now().Add(-time.Minute), "rt")
	unrefreshable := guardTestRow(time.Now().Add(-time.Minute), "")
	// assertLiveRevokeCtx asserts revocation runs on a live context with a
	// deadline, even when the request context is cancelled.
	assertLiveRevokeCtx := func(ctx context.Context, requestID string) {
		assert.Equal(t, guardTestRequestID, requestID)
		assert.NoError(t, ctx.Err(), "revocation must not run on the cancelled request context")
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline, "revocation must be bounded")
	}

	tests := []struct {
		name       string
		setup      func(stor *mocks.MockStorage, refresher *mocks.MockUpstreamTokenRefresher)
		cancelled  bool
		wantRefuse bool
	}{
		{
			name: "upstream token read failure grants",
			setup: func(stor *mocks.MockStorage, _ *mocks.MockUpstreamTokenRefresher) {
				stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), guardTestTSID).Return(nil, storageErr)
			},
		},
		{
			name: "recheck read failure after a rejection grants",
			setup: func(stor *mocks.MockStorage, refresher *mocks.MockUpstreamTokenRefresher) {
				stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), guardTestTSID).
					Return(map[string]*storage.UpstreamTokens{"idp": expired}, nil)
				refresher.EXPECT().RefreshAndStore(gomock.Any(), guardTestTSID, gomock.Any()).Return(nil, rejected)
				stor.EXPECT().GetUpstreamTokens(gomock.Any(), guardTestTSID, "idp").Return(nil, storageErr)
			},
		},
		{
			name: "row deletion failure after a confirmed rejection still refuses",
			setup: func(stor *mocks.MockStorage, refresher *mocks.MockUpstreamTokenRefresher) {
				stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), guardTestTSID).
					Return(map[string]*storage.UpstreamTokens{"idp": expired}, nil)
				refresher.EXPECT().RefreshAndStore(gomock.Any(), guardTestTSID, gomock.Any()).Return(nil, rejected)
				stor.EXPECT().GetUpstreamTokens(gomock.Any(), guardTestTSID, "idp").Return(expired, nil)
				stor.EXPECT().DeleteUpstreamTokens(gomock.Any(), guardTestTSID).
					Do(func(ctx context.Context, _ string) {
						assert.NoError(t, ctx.Err(), "deletion must not run on the cancelled request context")
					}).Return(storageErr)
			},
			wantRefuse: true,
		},
		{
			name: "revocation failure still refuses",
			setup: func(stor *mocks.MockStorage, _ *mocks.MockUpstreamTokenRefresher) {
				stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), guardTestTSID).
					Return(map[string]*storage.UpstreamTokens{"idp": unrefreshable}, nil)
				stor.EXPECT().RevokeRefreshToken(gomock.Any(), guardTestRequestID).Return(storageErr)
				stor.EXPECT().RevokeAccessToken(gomock.Any(), guardTestRequestID).Return(storageErr)
			},
			wantRefuse: true,
		},
		{
			name: "revocation runs detached from a cancelled request",
			setup: func(stor *mocks.MockStorage, _ *mocks.MockUpstreamTokenRefresher) {
				stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), guardTestTSID).
					Return(map[string]*storage.UpstreamTokens{"idp": unrefreshable}, nil)
				stor.EXPECT().RevokeRefreshToken(gomock.Any(), guardTestRequestID).Do(assertLiveRevokeCtx).Return(nil)
				stor.EXPECT().RevokeAccessToken(gomock.Any(), guardTestRequestID).Do(assertLiveRevokeCtx).Return(nil)
			},
			cancelled:  true,
			wantRefuse: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			stor := mocks.NewMockStorage(ctrl)
			refresher := mocks.NewMockUpstreamTokenRefresher(ctrl)
			tt.setup(stor, refresher)
			guard, err := newUpstreamRefreshGuard(stor, refresher, []string{"idp"})
			require.NoError(t, err)
			guard.recheckDelay = 0

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			err = guard.HandleTokenEndpointRequest(ctx, guardTestRequest(guardTestTSID, "refresh_token"))
			if tt.wantRefuse {
				require.ErrorIs(t, err, fosite.ErrInvalidGrant)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestUpstreamRefreshGuard_RealIdPRejection drives the guard with the real
// provider and refresher against a fake IdP, so the IdP's invalid_grant
// reaches the guard through the same chain as in production, while a 5xx does
// not refuse the grant.
func TestUpstreamRefreshGuard_RealIdPRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		body       string
		wantRefuse bool
	}{
		{name: "invalid_grant refuses", status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`, wantRefuse: true},
		{name: "5xx grants", status: http.StatusServiceUnavailable, body: `{"error":"invalid_grant"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(idp.Close)
			provider, err := upstream.NewOAuth2Provider(&upstream.OAuth2Config{
				CommonOAuthConfig: upstream.CommonOAuthConfig{
					ClientID:    "client",
					RedirectURI: "http://localhost/callback",
				},
				AuthorizationEndpoint: idp.URL + "/authorize",
				TokenEndpoint:         idp.URL + "/token",
			})
			require.NoError(t, err)

			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })
			require.NoError(t, stor.StoreUpstreamTokens(ctx, guardTestTSID, "idp", guardTestRow(time.Now().Add(-time.Minute), "rt")))
			refresher := newUpstreamTokenRefresher(
				[]handlers.NamedUpstream{{Name: "idp", Provider: provider}}, stor, time.Hour)
			guard, err := newUpstreamRefreshGuard(stor, refresher, []string{"idp"})
			require.NoError(t, err)
			guard.recheckDelay = 0

			err = guard.HandleTokenEndpointRequest(ctx, guardTestRequest(guardTestTSID, "refresh_token"))
			if tt.wantRefuse {
				require.ErrorIs(t, err, fosite.ErrInvalidGrant)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestIntegration_UpstreamRefreshGuard drives the token endpoint end to end,
// on memory and Redis storage: a refresh grant whose upstream credential is
// live, or expired but refreshable, is granted. One whose upstream row is
// absent or unreadable, or whose upstream refresh the IdP rejects, is refused
// with invalid_grant and its refresh token kept; an IdP rejection also drops
// the session's upstream rows. One whose expired row has no
// refresh token is refused and its refresh token revoked. With the guard
// disabled, the absent case is granted.
func TestIntegration_UpstreamRefreshGuard(t *testing.T) {
	t.Parallel()

	deleteRows := func(t *testing.T, ts *testServerWithUpstream, tsid string) {
		t.Helper()
		require.NoError(t, ts.storage.DeleteUpstreamTokens(context.Background(), tsid))
	}
	// rejectRefresh swaps in a refresh token mockoidc signed but holds no
	// session for, so mockoidc answers its refresh with invalid_grant.
	rejectRefresh := func(t *testing.T, ts *testServerWithUpstream, tsid string) {
		t.Helper()
		unknown, err := ts.mockOIDC.Keypair.SignJWT(jwt.MapClaims{
			"jti": "no-such-session",
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		require.NoError(t, err)
		row := storedUpstreamRow(t, ts, tsid)
		row.ExpiresAt = time.Now().Add(-time.Minute)
		row.RefreshToken = unknown
		require.NoError(t, ts.storage.StoreUpstreamTokens(context.Background(), tsid, "default", row))
	}
	dropRefreshToken := func(t *testing.T, ts *testServerWithUpstream, tsid string) {
		t.Helper()
		row := storedUpstreamRow(t, ts, tsid)
		row.ExpiresAt = time.Now().Add(-time.Minute)
		row.RefreshToken = ""
		require.NoError(t, ts.storage.StoreUpstreamTokens(context.Background(), tsid, "default", row))
	}

	tests := []struct {
		name string
		// mutate changes the session's upstream rows before the refresh grant.
		mutate     func(t *testing.T, ts *testServerWithUpstream, tsid string)
		disable    bool
		redis      bool
		wantRefuse bool
		wantRevoke bool
		// wantDropped asserts the refusal deleted the session's upstream rows.
		wantDropped bool
		// wantRowRefreshed asserts the grant refreshed the expired upstream row.
		wantRowRefreshed bool
	}{
		{
			name:   "live upstream is granted",
			mutate: func(*testing.T, *testServerWithUpstream, string) {},
		},
		{
			name: "idle session whose upstream refresh succeeds is granted",
			mutate: func(t *testing.T, ts *testServerWithUpstream, tsid string) {
				t.Helper()
				row := storedUpstreamRow(t, ts, tsid)
				row.ExpiresAt = time.Now().Add(-time.Minute)
				require.NoError(t, ts.storage.StoreUpstreamTokens(context.Background(), tsid, "default", row))
			},
			wantRowRefreshed: true,
		},
		{name: "absent upstream row is refused", mutate: deleteRows, wantRefuse: true},
		{name: "absent upstream row is refused on Redis", mutate: deleteRows, redis: true, wantRefuse: true},
		{
			name: "unreadable upstream row on Redis is refused",
			mutate: func(t *testing.T, ts *testServerWithUpstream, tsid string) {
				t.Helper()
				key := "test:auth:{ns:test}:" + storage.KeyTypeUpstream + ":" + tsid + ":default"
				mr := ts.Miniredis(t)
				require.True(t, mr.Exists(key), "upstream row key must exist")
				require.NoError(t, mr.Set(key, "{not json"))
			},
			redis:      true,
			wantRefuse: true,
		},
		{
			name:        "upstream refresh rejected with invalid_grant is refused",
			mutate:      rejectRefresh,
			wantRefuse:  true,
			wantDropped: true,
		},
		{
			name:        "upstream refresh rejected with invalid_grant is refused on Redis",
			mutate:      rejectRefresh,
			redis:       true,
			wantRefuse:  true,
			wantDropped: true,
		},
		{
			name:       "expired upstream row without a refresh token is refused and revoked",
			mutate:     dropRefreshToken,
			wantRefuse: true,
			wantRevoke: true,
		},
		{
			name:       "expired upstream row without a refresh token is refused and revoked on Redis",
			mutate:     dropRefreshToken,
			redis:      true,
			wantRefuse: true,
			wantRevoke: true,
		},
		{name: "absent upstream row is granted with the guard disabled", mutate: deleteRows, disable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedder := &recordingRefreshHandler{}
			opts := []testServerOption{withRefreshGrantHandlers(embedder)}
			if tt.disable {
				opts = append(opts, withoutUpstreamRefreshGuard())
			}
			if tt.redis {
				opts = append(opts, withRedisBackedStorage())
			}
			ts := setupTestServerWithMockOIDC(t, startMockOIDC(t), opts...)

			verifier := servercrypto.GeneratePKCEVerifier()
			authCode, _ := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
				ClientID:     testClientID,
				RedirectURI:  testRedirectURI,
				State:        "upstream-refresh-guard-state",
				Challenge:    servercrypto.ComputePKCEChallenge(verifier),
				Scope:        "openid profile offline_access",
				ResponseType: "code",
			})
			tokenData := exchangeCodeForTokens(t, ts.Server.URL, authCode, verifier, testAudience)
			refreshToken, ok := tokenData["refresh_token"].(string)
			require.True(t, ok)
			accessToken, ok := tokenData["access_token"].(string)
			require.True(t, ok)
			tsid := extractTSID(t, accessToken, ts.PrivateKey.Public())
			original := storedUpstreamRow(t, ts, tsid)

			tt.mutate(t, ts, tsid)

			refresh := func() (int, map[string]interface{}) {
				resp := makeTokenRequest(t, ts.Server.URL, url.Values{
					"grant_type":    {"refresh_token"},
					"refresh_token": {refreshToken},
					"client_id":     {testClientID},
				})
				defer resp.Body.Close()
				return resp.StatusCode, parseTokenResponse(t, resp)
			}

			status, body := refresh()
			if !tt.wantRefuse {
				require.Equal(t, http.StatusOK, status, "refresh grant must be granted: %v", body)
				assert.NotEmpty(t, body["access_token"])
				assert.Len(t, embedder.handledGrants(), 1)
				if tt.wantRowRefreshed {
					assert.False(t, storedUpstreamRow(t, ts, tsid).IsExpired(time.Now()),
						"the guard must refresh the expired upstream row before granting")
				}
				return
			}

			assert.Equal(t, http.StatusBadRequest, status)
			assert.Equal(t, "invalid_grant", body["error"])
			assert.NotContains(t, body, "access_token")
			assert.Empty(t, embedder.handledGrants(), "embedder handlers must not run after the guard refuses")
			if tt.wantDropped {
				rows, err := ts.storage.GetAllUpstreamTokens(context.Background(), tsid)
				require.NoError(t, err)
				assert.Empty(t, rows, "a confirmed IdP rejection must drop the session's upstream rows")
			}

			// Restore a live upstream row: a revoked refresh token still does
			// not work, and one only refused works again.
			require.NoError(t, ts.storage.StoreUpstreamTokens(context.Background(), tsid, "default", original))
			status, body = refresh()
			if tt.wantRevoke {
				assert.NotEqual(t, http.StatusOK, status, "refused refresh token must be revoked: %v", body)
				assert.NotContains(t, body, "access_token")
				return
			}
			assert.Equal(t, http.StatusOK, status, "refused refresh token must not be revoked: %v", body)
		})
	}
}

// storedUpstreamRow returns the session's stored row for the "default"
// upstream, whatever its expiry.
func storedUpstreamRow(t *testing.T, ts *testServerWithUpstream, tsid string) *storage.UpstreamTokens {
	t.Helper()
	rows, err := ts.storage.GetAllUpstreamTokens(context.Background(), tsid)
	require.NoError(t, err)
	row, ok := rows["default"]
	require.True(t, ok, "session must have a row for the default upstream")
	rowCopy := *row
	return &rowCopy
}
