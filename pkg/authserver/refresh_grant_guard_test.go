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
		// onRefresh, when set, is stored as the row after the refresher runs, as
		// a replica winning a refresh race would write it.
		onRefresh *storage.UpstreamTokens
		// deleteOnRefresh deletes the row after the refresher runs.
		deleteOnRefresh bool
		// cancelled runs the request on a cancelled context, with a recheck
		// delay long enough that only the cancellation can end the wait.
		cancelled  bool
		wantRefuse bool
	}{
		{
			name: "session-less grant is not checked",
			tsid: "",
		},
		{
			name:       "no upstream rows is refused",
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
			name:       "expired row without a refresh token is refused",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "")},
			wantRefuse: true,
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
			name:       "upstream invalid_grant is refused",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr: rejected,
			refreshes:  1,
			wantRefuse: true,
		},
		{
			name:            "upstream invalid_grant with the row deleted is refused",
			tsid:            guardTestTSID,
			rows:            map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr:      rejected,
			refreshes:       1,
			deleteOnRefresh: true,
			wantRefuse:      true,
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
			name:       "upstream invalid_grant with the row rotated but still expired is refused",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr: rejected,
			refreshes:  1,
			onRefresh:  guardTestRow(past, "rotated-rt"),
			wantRefuse: true,
		},
		{
			name:       "upstream invalid_grant with the recheck cancelled is not refused",
			tsid:       guardTestTSID,
			rows:       map[string]*storage.UpstreamTokens{"idp": guardTestRow(past, "rt")},
			refreshErr: rejected,
			refreshes:  1,
			cancelled:  true,
		},
		{
			name: "one dead provider among live ones is refused",
			tsid: guardTestTSID,
			rows: map[string]*storage.UpstreamTokens{
				"a-live": guardTestRow(future, "rt-a"),
				"b-dead": guardTestRow(past, ""),
			},
			wantRefuse: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })
			for name, row := range tt.rows {
				row.ProviderID = name
				require.NoError(t, stor.StoreUpstreamTokens(ctx, guardTestTSID, name, row))
			}
			// The grant's own tokens, which a refusal must revoke.
			req := guardTestRequest(tt.tsid, "refresh_token")
			require.NoError(t, stor.CreateRefreshTokenSession(ctx, "rt-sig", "at-sig", req))
			require.NoError(t, stor.CreateAccessTokenSession(ctx, "at-sig", req))

			refresher := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
			refresher.EXPECT().RefreshAndStore(gomock.Any(), guardTestTSID, gomock.Any()).Times(tt.refreshes).DoAndReturn(
				func(_ context.Context, sessionID string, expired *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
					if tt.deleteOnRefresh {
						require.NoError(t, stor.DeleteUpstreamTokensForProvider(ctx, sessionID, expired.ProviderID))
					}
					if tt.onRefresh != nil {
						winner := *tt.onRefresh
						winner.ProviderID = expired.ProviderID
						require.NoError(t, stor.StoreUpstreamTokens(ctx, sessionID, expired.ProviderID, &winner))
					}
					if tt.refreshErr != nil {
						return nil, tt.refreshErr
					}
					refreshed := *expired
					refreshed.ExpiresAt = future
					return &refreshed, nil
				})
			guard := newUpstreamRefreshGuard(false, stor, refresher)
			require.NotNil(t, guard)
			guard.recheckDelay = 0

			reqCtx := ctx
			if tt.cancelled {
				guard.recheckDelay = time.Minute
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				reqCtx = cancelled
			}
			err := guard.HandleTokenEndpointRequest(reqCtx, req)

			_, refreshErr := stor.GetRefreshTokenSession(ctx, "rt-sig", nil)
			_, accessErr := stor.GetAccessTokenSession(ctx, "at-sig", nil)
			if tt.wantRefuse {
				require.ErrorIs(t, err, fosite.ErrInvalidGrant)
				var rfcErr *fosite.RFC6749Error
				require.ErrorAs(t, err, &rfcErr)
				assert.Equal(t, http.StatusBadRequest, rfcErr.CodeField)
				assert.Error(t, refreshErr, "refused refresh token must be revoked")
				assert.Error(t, accessErr, "the refused grant's access token must be revoked")
				return
			}
			require.NoError(t, err)
			assert.NoError(t, refreshErr, "a granted refresh must not revoke the refresh token")
			assert.NoError(t, accessErr, "a granted refresh must not revoke the access token")
		})
	}
}

func TestUpstreamRefreshGuard_Gates(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	refresher := mocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))

	t.Run("not built when disabled or without a refresher", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, newUpstreamRefreshGuard(true, stor, refresher))
		assert.Nil(t, newUpstreamRefreshGuard(false, stor, nil))
	})

	t.Run("runs only on the refresh_token grant and never skips client auth", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		guard := newUpstreamRefreshGuard(false, stor, refresher)
		require.NotNil(t, guard)

		req := guardTestRequest(guardTestTSID, "authorization_code")
		assert.False(t, guard.CanHandleTokenEndpointRequest(ctx, req))
		require.ErrorIs(t, guard.HandleTokenEndpointRequest(ctx, req), fosite.ErrUnknownRequest)
		assert.False(t, guard.CanSkipClientAuth(ctx, req))
		require.ErrorIs(t, guard.PopulateTokenEndpointResponse(ctx, req, fosite.NewAccessResponse()), fosite.ErrUnknownRequest)
		assert.True(t, guard.CanHandleTokenEndpointRequest(ctx, guardTestRequest(guardTestTSID, "refresh_token")))
	})
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
			guard := newUpstreamRefreshGuard(false, stor, refresher)
			require.NotNil(t, guard)
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

// TestIntegration_UpstreamRefreshGuard drives the token endpoint end to end:
// a refresh grant whose upstream credential is live, or expired but
// refreshable, is granted; one whose upstream row is absent or whose upstream
// refresh the IdP rejects is refused with invalid_grant and its refresh token
// revoked. With the guard disabled, the absent case is granted.
func TestIntegration_UpstreamRefreshGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// mutate changes the session's upstream rows before the refresh grant.
		mutate     func(t *testing.T, ts *testServerWithUpstream, tsid string)
		disable    bool
		wantRefuse bool
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
		{
			name: "absent upstream row is refused",
			mutate: func(t *testing.T, ts *testServerWithUpstream, tsid string) {
				t.Helper()
				require.NoError(t, ts.storage.DeleteUpstreamTokens(context.Background(), tsid))
			},
			wantRefuse: true,
		},
		{
			name: "upstream refresh rejected with invalid_grant is refused",
			mutate: func(t *testing.T, ts *testServerWithUpstream, tsid string) {
				t.Helper()
				// A refresh token mockoidc signed but holds no session for:
				// mockoidc answers its refresh with invalid_grant.
				unknown, err := ts.mockOIDC.Keypair.SignJWT(jwt.MapClaims{
					"jti": "no-such-session",
					"exp": time.Now().Add(time.Hour).Unix(),
				})
				require.NoError(t, err)
				row := storedUpstreamRow(t, ts, tsid)
				row.ExpiresAt = time.Now().Add(-time.Minute)
				row.RefreshToken = unknown
				require.NoError(t, ts.storage.StoreUpstreamTokens(context.Background(), tsid, "default", row))
			},
			wantRefuse: true,
		},
		{
			name: "absent upstream row is granted with the guard disabled",
			mutate: func(t *testing.T, ts *testServerWithUpstream, tsid string) {
				t.Helper()
				require.NoError(t, ts.storage.DeleteUpstreamTokens(context.Background(), tsid))
			},
			disable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedder := &recordingRefreshHandler{}
			opts := []testServerOption{withRefreshGrantHandlers(embedder)}
			if tt.disable {
				opts = append(opts, withoutUpstreamRefreshGuard())
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

			// Restore a live upstream row: the refused refresh token was
			// revoked, so it still does not work.
			require.NoError(t, ts.storage.StoreUpstreamTokens(context.Background(), tsid, "default", original))
			status, body = refresh()
			assert.NotEqual(t, http.StatusOK, status, "refused refresh token must be revoked: %v", body)
			assert.NotContains(t, body, "access_token")
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
