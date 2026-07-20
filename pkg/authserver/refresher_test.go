// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

func TestUpstreamTokenRefresher_RefreshAndStore(t *testing.T) {
	t.Parallel()

	newExpiry := time.Now().Add(1 * time.Hour)
	sessionBound := time.Now().Add(7 * 24 * time.Hour)

	baseExpired := &storage.UpstreamTokens{
		ProviderID:       "github",
		AccessToken:      "old-access",
		RefreshToken:     "old-refresh",
		IDToken:          "old-id-token",
		ExpiresAt:        time.Now().Add(-1 * time.Hour),
		SessionExpiresAt: sessionBound,
		UserID:           "user-123",
		UpstreamSubject:  "upstream-sub-456",
		ClientID:         "client-abc",
	}

	tests := []struct {
		name           string
		sessionID      string
		expired        *storage.UpstreamTokens
		setupProvider  func(*testing.T, *upstreammocks.MockOAuth2Provider)
		setupStorage   func(*testing.T, *storagemocks.MockUpstreamTokenStorage)
		wantErr        bool
		wantErrContain string
		checkResult    func(*testing.T, *storage.UpstreamTokens)
	}{
		{
			name:      "successful refresh with token rotation",
			sessionID: "session-1",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "new-refresh",
						IDToken:      "new-id-token",
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-1", "github", gomock.Any()).
					DoAndReturn(func(_ context.Context, _, _ string, tokens *storage.UpstreamTokens) error {
						// Verify binding fields are preserved from expired tokens
						assert.Equal(t, "github", tokens.ProviderID)
						assert.Equal(t, "user-123", tokens.UserID)
						assert.Equal(t, "upstream-sub-456", tokens.UpstreamSubject)
						assert.Equal(t, "client-abc", tokens.ClientID)
						// Verify new token values
						assert.Equal(t, "new-access", tokens.AccessToken)
						assert.Equal(t, "new-refresh", tokens.RefreshToken)
						assert.Equal(t, "new-id-token", tokens.IDToken)
						assert.Equal(t, newExpiry, tokens.ExpiresAt)
						return nil
					})
			},
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.Equal(t, "new-access", result.AccessToken)
				assert.Equal(t, "new-refresh", result.RefreshToken)
				assert.Equal(t, "new-id-token", result.IDToken)
				assert.Equal(t, newExpiry, result.ExpiresAt)
				// Binding fields preserved
				assert.Equal(t, "github", result.ProviderID)
				assert.Equal(t, "user-123", result.UserID)
				assert.Equal(t, "upstream-sub-456", result.UpstreamSubject)
				assert.Equal(t, "client-abc", result.ClientID)
			},
		},
		{
			name:      "provider does not rotate refresh token - keeps old one",
			sessionID: "session-2",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "", // Provider did not rotate
						IDToken:      "new-id-token",
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-2", "github", gomock.Any()).
					DoAndReturn(func(_ context.Context, _, _ string, tokens *storage.UpstreamTokens) error {
						assert.Equal(t, "old-refresh", tokens.RefreshToken)
						return nil
					})
			},
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.Equal(t, "new-access", result.AccessToken)
				assert.Equal(t, "old-refresh", result.RefreshToken)
			},
		},
		{
			// Regression for the refresh-path bound. SessionExpiresAt must be carried
			// forward unchanged so a refresh that returns a token with zero ExpiresAt
			// (provider stops asserting expires_in) still has a storage TTL bound.
			// Without this, the row would be stored with no TTL and leak indefinitely.
			name:      "preserves SessionExpiresAt when provider omits expires_in",
			sessionID: "session-bound",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "new-refresh",
						// ExpiresAt intentionally zero — provider omitted expires_in.
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-bound", "github", gomock.Any()).
					DoAndReturn(func(_ context.Context, _, _ string, tokens *storage.UpstreamTokens) error {
						assert.Equal(t, sessionBound, tokens.SessionExpiresAt,
							"refresher must carry SessionExpiresAt forward unchanged")
						assert.True(t, tokens.ExpiresAt.IsZero(),
							"new ExpiresAt should be zero (provider omitted expires_in)")
						return nil
					})
			},
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.Equal(t, sessionBound, result.SessionExpiresAt,
					"returned tokens must also carry SessionExpiresAt forward")
				assert.True(t, result.ExpiresAt.IsZero())
			},
		},
		{
			// Defensive re-anchor for legacy data. Pre-PR Redis rows decode
			// SessionExpiresAt as zero (the field was not persisted). If such a
			// row is refreshed and the upstream rotation also drops expires_in,
			// both bounds are zero — the row would be stored without any TTL,
			// and the Memory backend would retain it indefinitely. The refresher
			// must re-anchor SessionExpiresAt to now+RefreshTokenLifespan so the
			// row carries a storage TTL bound forward.
			name:      "re-anchors SessionExpiresAt when legacy row and provider both omit expiry",
			sessionID: "session-legacy",
			expired: &storage.UpstreamTokens{
				ProviderID:       "github",
				AccessToken:      "old-access",
				RefreshToken:     "old-refresh",
				IDToken:          "old-id-token",
				ExpiresAt:        time.Time{}, // legacy row decoded with zero expiry
				SessionExpiresAt: time.Time{}, // legacy row missing the field entirely
				UserID:           "user-123",
				UpstreamSubject:  "upstream-sub-456",
				ClientID:         "client-abc",
			},
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "new-refresh",
						IDToken:      "new-id-token",
						// ExpiresAt intentionally zero — provider also omitted expires_in.
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-legacy", "github", gomock.Any()).
					DoAndReturn(func(_ context.Context, _, _ string, tokens *storage.UpstreamTokens) error {
						assert.False(t, tokens.SessionExpiresAt.IsZero(),
							"refresher must re-anchor SessionExpiresAt for legacy zero/zero rows")
						assert.True(t, tokens.ExpiresAt.IsZero(),
							"new ExpiresAt should be zero (provider omitted expires_in)")
						// The re-anchor uses the configured lifespan (24h in this test).
						assert.WithinDuration(t, time.Now().Add(24*time.Hour), tokens.SessionExpiresAt, time.Minute,
							"re-anchored SessionExpiresAt should be ~now+RefreshTokenLifespan")
						return nil
					})
			},
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.False(t, result.SessionExpiresAt.IsZero(),
					"returned tokens must also carry the re-anchored SessionExpiresAt")
				assert.True(t, result.ExpiresAt.IsZero())
			},
		},
		{
			name:           "nil expired tokens returns error",
			sessionID:      "session-3",
			expired:        nil,
			setupProvider:  func(_ *testing.T, _ *upstreammocks.MockOAuth2Provider) {},
			setupStorage:   func(_ *testing.T, _ *storagemocks.MockUpstreamTokenStorage) {},
			wantErr:        true,
			wantErrContain: "expired tokens are required",
		},
		{
			name:      "empty refresh token returns error",
			sessionID: "session-4",
			expired: &storage.UpstreamTokens{
				ProviderID:      "github",
				AccessToken:     "old-access",
				RefreshToken:    "",
				UserID:          "user-123",
				UpstreamSubject: "upstream-sub-456",
				ClientID:        "client-abc",
			},
			setupProvider:  func(_ *testing.T, _ *upstreammocks.MockOAuth2Provider) {},
			setupStorage:   func(_ *testing.T, _ *storagemocks.MockUpstreamTokenStorage) {},
			wantErr:        true,
			wantErrContain: "no refresh token available",
		},
		{
			name:      "unknown provider returns error",
			sessionID: "session-unknown",
			expired: &storage.UpstreamTokens{
				ProviderID:      "unknown-provider",
				AccessToken:     "old-access",
				RefreshToken:    "old-refresh",
				UserID:          "user-123",
				UpstreamSubject: "upstream-sub-456",
				ClientID:        "client-abc",
			},
			setupProvider:  func(_ *testing.T, _ *upstreammocks.MockOAuth2Provider) {},
			setupStorage:   func(_ *testing.T, _ *storagemocks.MockUpstreamTokenStorage) {},
			wantErr:        true,
			wantErrContain: "no upstream provider configured",
		},
		{
			name:      "provider refresh fails returns error",
			sessionID: "session-5",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(nil, errors.New("upstream IDP unavailable"))
			},
			setupStorage:   func(_ *testing.T, _ *storagemocks.MockUpstreamTokenStorage) {},
			wantErr:        true,
			wantErrContain: "upstream token refresh failed",
		},
		{
			// Rotated RT + all store attempts fail → best-effort delete + error.
			// Provider returns a new (different) RefreshToken so rotation is detected.
			name:      "rotated RT and storage fails - deletes stale row and returns error",
			sessionID: "session-6",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "new-refresh",
						IDToken:      "new-id-token",
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-6", "github", gomock.Any()).
					Times(3).
					Return(errors.New("redis connection lost"))
				s.EXPECT().DeleteUpstreamTokensForProvider(gomock.Any(), "session-6", "github").
					Return(nil)
			},
			wantErr:        true,
			wantErrContain: "failed to persist rotated upstream refresh token",
		},
		{
			// Not-rotated RT (provider returns empty RefreshToken → backfill) +
			// all store attempts fail → proceed-anyway (old RT still valid in storage).
			name:      "not-rotated RT and storage fails - returns tokens without error",
			sessionID: "session-7",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "", // provider did not rotate
						IDToken:      "new-id-token",
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-7", "github", gomock.Any()).
					Times(3).
					Return(errors.New("redis connection lost"))
				// No DeleteUpstreamTokensForProvider call expected.
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.Equal(t, "new-access", result.AccessToken)
				// The old refresh token must be backfilled (provider returned "").
				assert.Equal(t, "old-refresh", result.RefreshToken)
			},
		},
		{
			// Rotated RT: first store attempt fails, second succeeds → no delete, result returned.
			name:      "rotated RT and store succeeds on retry - no delete",
			sessionID: "session-8",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "new-refresh",
						IDToken:      "new-id-token",
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				gomock.InOrder(
					s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-8", "github", gomock.Any()).
						Return(errors.New("transient error")),
					s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-8", "github", gomock.Any()).
						Return(nil),
				)
				// No DeleteUpstreamTokensForProvider expected.
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.Equal(t, "new-refresh", result.RefreshToken)
			},
		},
		{
			// Regression: when the provider omits id_token on a refresh (common — e.g. Google),
			// the refresher must carry the original login ID token forward into storage rather
			// than overwriting the persisted row with an empty string. StoreUpstreamTokens
			// replaces the whole row, so without the carry-forward the login ID token is
			// permanently lost after the first refresh cycle.
			name:      "provider omits id_token on refresh - keeps login ID token",
			sessionID: "session-9",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "new-refresh",
						IDToken:      "", // provider omitted id_token
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-9", "github", gomock.Any()).
					DoAndReturn(func(_ context.Context, _, _ string, tokens *storage.UpstreamTokens) error {
						assert.Equal(t, "old-id-token", tokens.IDToken,
							"refresher must carry forward the login ID token when provider omits one")
						return nil
					})
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.Equal(t, "new-access", result.AccessToken)
				assert.Equal(t, "new-refresh", result.RefreshToken)
				assert.Equal(t, "old-id-token", result.IDToken,
					"returned tokens must carry the login ID token when provider omits one")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)

			mockProvider := upstreammocks.NewMockOAuth2Provider(ctrl)
			mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)

			tt.setupProvider(t, mockProvider)
			tt.setupStorage(t, mockStorage)

			refresher := &upstreamTokenRefresher{
				providers:            map[string]upstream.OAuth2Provider{"github": mockProvider},
				storage:              mockStorage,
				refreshTokenLifespan: 24 * time.Hour,
			}

			result, err := refresher.RefreshAndStore(context.Background(), tt.sessionID, tt.expired)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContain)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

// waitGroupTimeout blocks until wg is done, failing the test if that takes
// longer than d. Without it, a synchronization regression (or a panicking
// goroutine) would hang the test until the global go test timeout instead of
// failing fast with a clear message.
func waitGroupTimeout(t *testing.T, wg *sync.WaitGroup, d time.Duration, msg string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal(msg)
	}
}

// TestUpstreamTokenRefresher_SingleflightDedup proves that concurrent
// RefreshAndStore calls for the same (session, provider) key result in exactly
// one upstream redemption, and that distinct keys are independent.
//
// The same-key sub-test holds the leader's in-flight call open on a
// test-controlled release channel while the followers join it, then asserts the
// provider was invoked exactly once. singleflight exposes no hook for "a
// follower is now parked in Do", so the test cannot be made fully deterministic;
// it gates release on every goroutine having reached RefreshAndStore plus a
// short settle for the followers to park — the same strategy used by
// golang.org/x/sync/singleflight's own dup-suppression test. The leader holding
// the key open for the whole window (rather than releasing as soon as the
// callers are merely counted) is what keeps it robust.
func TestUpstreamTokenRefresher_SingleflightDedup(t *testing.T) {
	t.Parallel()

	const n = 10
	newExpiry := time.Now().Add(1 * time.Hour)

	expired := &storage.UpstreamTokens{
		ProviderID:       "github",
		AccessToken:      "old-access",
		RefreshToken:     "old-refresh",
		ExpiresAt:        time.Now().Add(-1 * time.Hour),
		SessionExpiresAt: time.Now().Add(7 * 24 * time.Hour),
		UserID:           "user-1",
		UpstreamSubject:  "sub-1",
		ClientID:         "client-1",
	}

	t.Run("same key deduplicated to one redemption", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		mockProvider := upstreammocks.NewMockOAuth2Provider(ctrl)

		// inFlight is closed once the leader (goroutine 0) is inside the provider
		// callback, so the followers start only after the singleflight key is
		// already held in the group's map.
		inFlight := make(chan struct{})

		// release keeps the leader parked inside the callback — and therefore
		// keeps the in-flight call open in the singleflight map — until the TEST
		// closes it. Crucially the leader's exit is gated on the test, not on the
		// followers (the previous version released as soon as all goroutines had
		// merely reached their body, which let the leader clear the map entry
		// before a follower had actually entered Do — a real flake). singleflight
		// gives us no public hook for "follower is parked in Do", so determinism
		// rests on the leader holding the entry open across the followers' join;
		// the same approach golang.org/x/sync/singleflight's own dup-suppression
		// test uses.
		release := make(chan struct{})

		// invokeCount tracks actual provider invocations.
		var invokeCount atomic.Int64

		mockProvider.EXPECT().
			RefreshTokens(gomock.Any(), "old-refresh", "sub-1").
			Times(1).
			DoAndReturn(func(_ context.Context, _, _ string) (*upstream.Tokens, error) {
				invokeCount.Add(1)
				close(inFlight)
				<-release
				return &upstream.Tokens{
					AccessToken:  "new-access",
					RefreshToken: "new-refresh",
					ExpiresAt:    newExpiry,
				}, nil
			})
		mockStorage.EXPECT().
			StoreUpstreamTokens(gomock.Any(), "session-1", "github", gomock.Any()).
			Times(1).
			Return(nil)

		refresher := &upstreamTokenRefresher{
			providers:            map[string]upstream.OAuth2Provider{"github": mockProvider},
			storage:              mockStorage,
			refreshTokenLifespan: 24 * time.Hour,
		}

		results := make([]*storage.UpstreamTokens, n)
		errs := make([]error, n)
		var entered atomic.Int64
		var wg sync.WaitGroup
		wg.Add(n)

		// Leader: enters the provider callback (closing inFlight) and parks on
		// release.
		go func() {
			defer wg.Done()
			entered.Add(1)
			results[0], errs[0] = refresher.RefreshAndStore(
				context.Background(), "session-1", expired,
			)
		}()

		// Start the followers only once the leader holds the singleflight key.
		select {
		case <-inFlight:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for the first refresh to enter the provider")
		}
		for i := 1; i < n; i++ {
			go func(idx int) {
				defer wg.Done()
				entered.Add(1)
				results[idx], errs[idx] = refresher.RefreshAndStore(
					context.Background(), "session-1", expired,
				)
			}(i)
		}

		// Wait for every goroutine to have reached RefreshAndStore, then give the
		// followers a brief settle to park inside sfGroup.Do before releasing the
		// leader. The leader holds the key open the whole time, so once released
		// it returns the single shared result to all joined followers.
		require.Eventually(t, func() bool { return entered.Load() == n }, 5*time.Second, time.Millisecond,
			"all goroutines should reach RefreshAndStore")
		time.Sleep(100 * time.Millisecond)
		close(release)

		waitGroupTimeout(t, &wg, 5*time.Second,
			"timeout waiting for concurrent RefreshAndStore callers to complete")

		for i := range n {
			require.NoError(t, errs[i], "goroutine %d got error", i)
			require.NotNil(t, results[i], "goroutine %d got nil result", i)
			assert.Equal(t, "new-access", results[i].AccessToken,
				"goroutine %d got wrong access token", i)
		}
		assert.Equal(t, int64(1), invokeCount.Load(),
			"upstream provider must be invoked exactly once despite %d concurrent callers", n)
		// mockStorage Times(1) independently enforces exactly one store write.
	})

	t.Run("distinct keys are independent", func(t *testing.T) {
		t.Parallel()

		const numProviders = 3
		ctrl := gomock.NewController(t)
		mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)

		providers := make(map[string]upstream.OAuth2Provider, numProviders)
		for i := range numProviders {
			providerID := "provider-" + string(rune('a'+i))
			mockProvider := upstreammocks.NewMockOAuth2Provider(ctrl)
			mockProvider.EXPECT().
				RefreshTokens(gomock.Any(), "rt-"+providerID, gomock.Any()).
				Times(1).
				Return(&upstream.Tokens{
					AccessToken: "at-" + providerID,
					ExpiresAt:   newExpiry,
				}, nil)
			mockStorage.EXPECT().
				StoreUpstreamTokens(gomock.Any(), "session-x", providerID, gomock.Any()).
				Times(1).
				Return(nil)
			providers[providerID] = mockProvider
		}

		refresher := &upstreamTokenRefresher{
			providers:            providers,
			storage:              mockStorage,
			refreshTokenLifespan: 24 * time.Hour,
		}

		var wg sync.WaitGroup
		for i := range numProviders {
			providerID := "provider-" + string(rune('a'+i))
			tok := &storage.UpstreamTokens{
				ProviderID:       providerID,
				AccessToken:      "old",
				RefreshToken:     "rt-" + providerID,
				ExpiresAt:        time.Now().Add(-1 * time.Hour),
				SessionExpiresAt: time.Now().Add(24 * time.Hour),
				UserID:           "u",
				UpstreamSubject:  "s",
				ClientID:         "c",
			}
			wg.Add(1)
			go func(tok *storage.UpstreamTokens) {
				defer wg.Done()
				result, err := refresher.RefreshAndStore(context.Background(), "session-x", tok)
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}(tok)
		}
		waitGroupTimeout(t, &wg, 5*time.Second,
			"timeout waiting for distinct-key RefreshAndStore callers to complete")
		// gomock Times(1) per mock provider asserts each distinct key ran
		// exactly once through the actual upstream call.
	})
}
