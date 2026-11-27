// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

const (
	readerUser     = "user-a"
	readerIntruder = "user-b"
	readerProvider = "github"
)

// userRowSession is the session ID under which the user-keyed wrapper stores a
// user's rows in the store beneath it. The wrapper cannot write a foreign or
// placeholder row, so tests plant those through the base store.
func userRowSession() string { return "user:" + url.QueryEscape(readerUser) }

func readerRow(userID string, expiresAt time.Time) *storage.UpstreamTokens {
	return &storage.UpstreamTokens{
		ProviderID:       readerProvider,
		AccessToken:      "access-" + userID,
		RefreshToken:     "refresh-" + userID,
		IDToken:          "id-" + userID,
		UserID:           userID,
		UpstreamSubject:  "sub-" + userID,
		ExpiresAt:        expiresAt,
		SessionExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}
}

// userKeyedStores returns the user-keyed wrapper over the memory and miniredis
// stores, with the store beneath each for planting rows.
func userKeyedStores(t *testing.T) map[string]func(t *testing.T) (storage.UpstreamTokenStorage, storage.Storage) {
	t.Helper()
	return map[string]func(t *testing.T) (storage.UpstreamTokenStorage, storage.Storage){
		"memory": func(t *testing.T) (storage.UpstreamTokenStorage, storage.Storage) {
			t.Helper()
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = base.Close() })
			wrapper, err := storage.NewUserKeyedUpstreamStorage(base)
			require.NoError(t, err)
			return wrapper, base
		},
		"redis": func(t *testing.T) (storage.UpstreamTokenStorage, storage.Storage) {
			t.Helper()
			mr := miniredis.RunT(t)
			base := storage.NewRedisStorageWithClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "test:auth:")
			t.Cleanup(func() { _ = base.Close() })
			wrapper, err := storage.NewUserKeyedUpstreamStorage(base)
			require.NoError(t, err)
			return wrapper, base
		},
	}
}

func TestGetValidTokensForUser_UserKeyedStorage(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	bg := context.Background()

	for storeName, newStore := range userKeyedStores(t) {
		t.Run(storeName, func(t *testing.T) {
			t.Parallel()

			t.Run("live row is returned without a refresh", func(t *testing.T) {
				t.Parallel()
				stor, _ := newStore(t)
				require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(bg, readerUser), "", readerProvider,
					readerRow(readerUser, future)))
				refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))

				cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

				require.NoError(t, err)
				assert.Equal(t, &UpstreamCredential{AccessToken: "access-" + readerUser, IDToken: "id-" + readerUser}, cred)
			})

			t.Run("a context carrying another user does not change whose row is read", func(t *testing.T) {
				t.Parallel()
				stor, _ := newStore(t)
				require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(bg, readerUser), "", readerProvider,
					readerRow(readerUser, future)))
				ctx := identity.WithCanonicalUser(bg, readerIntruder)

				cred, err := NewInProcessService(stor, nil).GetValidTokensForUser(ctx, readerUser, readerProvider)

				require.NoError(t, err)
				assert.Equal(t, "access-"+readerUser, cred.AccessToken)
			})

			t.Run("expired row is refreshed for the user", func(t *testing.T) {
				t.Parallel()
				stor, _ := newStore(t)
				expired := readerRow(readerUser, past)
				require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(bg, readerUser), "", readerProvider, expired))
				refreshed := readerRow(readerUser, future)
				refreshed.AccessToken = "new-access"
				refreshed.IDToken = ""
				refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
				refresher.EXPECT().RefreshAndStore(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ string, in *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
						user, ok := identity.CanonicalUserFromContext(ctx)
						assert.True(t, ok)
						assert.Equal(t, readerUser, user)
						assert.Equal(t, "refresh-"+readerUser, in.RefreshToken)
						return refreshed, nil
					})

				cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

				require.NoError(t, err)
				assert.Equal(t, &UpstreamCredential{AccessToken: "new-access", IDToken: "id-" + readerUser}, cred)
			})

			t.Run("no row needs authorization", func(t *testing.T) {
				t.Parallel()
				stor, _ := newStore(t)

				cred, err := NewInProcessService(stor, nil).GetValidTokensForUser(bg, readerUser, readerProvider)

				assert.Nil(t, cred)
				assert.ErrorIs(t, err, ErrNeedsAuthorization)
				assert.ErrorIs(t, err, ErrSessionNotFound)
				assert.ErrorIs(t, err, storage.ErrNotFound)
			})

			t.Run("placeholder row needs authorization", func(t *testing.T) {
				t.Parallel()
				stor, base := newStore(t)
				require.NoError(t, base.StoreUpstreamTokens(bg, userRowSession(), readerProvider, nil))

				cred, err := NewInProcessService(stor, nil).GetValidTokensForUser(bg, readerUser, readerProvider)

				assert.Nil(t, cred)
				assert.ErrorIs(t, err, ErrNeedsAuthorization)
				assert.ErrorIs(t, err, ErrSessionNotFound)
			})

			t.Run("live row with an empty access token needs authorization", func(t *testing.T) {
				t.Parallel()
				stor, _ := newStore(t)
				row := readerRow(readerUser, future)
				row.AccessToken = ""
				require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(bg, readerUser), "", readerProvider, row))
				refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))

				cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

				assert.Nil(t, cred)
				assert.ErrorIs(t, err, ErrNeedsAuthorization)
				assert.ErrorIs(t, err, ErrSessionNotFound)
			})

			t.Run("expired row without a refresher needs authorization", func(t *testing.T) {
				t.Parallel()
				stor, _ := newStore(t)
				require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(bg, readerUser), "", readerProvider,
					readerRow(readerUser, past)))

				_, err := NewInProcessService(stor, nil).GetValidTokensForUser(bg, readerUser, readerProvider)

				assert.ErrorIs(t, err, ErrNeedsAuthorization)
				assert.ErrorIs(t, err, ErrNoRefreshToken)
			})

			t.Run("invalid_grant over the wrapper needs authorization", func(t *testing.T) {
				t.Parallel()
				stor, _ := newStore(t)
				require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(bg, readerUser), "", readerProvider,
					readerRow(readerUser, past)))
				refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
				refresher.EXPECT().RefreshAndStore(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, fmt.Errorf("upstream token refresh failed: %w", upstream.ErrGrantRejected))

				_, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

				assert.ErrorIs(t, err, ErrNeedsAuthorization)
				assert.ErrorIs(t, err, upstream.ErrGrantRejected)
			})

			t.Run("expired row without a refresh token needs authorization", func(t *testing.T) {
				t.Parallel()
				stor, _ := newStore(t)
				row := readerRow(readerUser, past)
				row.RefreshToken = ""
				require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(bg, readerUser), "", readerProvider, row))
				refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))

				_, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

				assert.ErrorIs(t, err, ErrNeedsAuthorization)
				assert.ErrorIs(t, err, ErrNoRefreshToken)
			})

			t.Run("foreign row is a binding error and is never refreshed", func(t *testing.T) {
				t.Parallel()
				for name, expiresAt := range map[string]time.Time{"live": future, "expired": past} {
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						stor, base := newStore(t)
						require.NoError(t, base.StoreUpstreamTokens(bg, userRowSession(), readerProvider,
							readerRow(readerIntruder, expiresAt)))
						refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))

						cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

						assert.Nil(t, cred)
						assert.ErrorIs(t, err, ErrInvalidBinding)
						assert.NotErrorIs(t, err, ErrNeedsAuthorization)
					})
				}
			})

			t.Run("row without an owner is a binding error", func(t *testing.T) {
				t.Parallel()
				stor, base := newStore(t)
				require.NoError(t, base.StoreUpstreamTokens(bg, userRowSession(), readerProvider,
					readerRow("", future)))

				_, err := NewInProcessService(stor, nil).GetValidTokensForUser(bg, readerUser, readerProvider)

				assert.ErrorIs(t, err, ErrInvalidBinding)
			})
		})
	}
}

func TestGetValidTokensForUser_CancelDuringRefresh(t *testing.T) {
	t.Parallel()

	// The bubble also fails if the refresh goroutine blocks sending its result
	// after the reader has returned.
	synctest.Test(t, func(t *testing.T) {
		stor, _ := userKeyedStores(t)["memory"](t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		rowKey := userRowKey(readerUser)
		expired := readerRow(readerUser, time.Now().Add(-time.Hour))
		require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(ctx, readerUser), rowKey, readerProvider, expired))

		started := make(chan struct{})
		release := make(chan struct{})
		refreshed := readerRow(readerUser, time.Now().Add(time.Hour))
		refreshed.AccessToken = "new-access"
		refreshed.RefreshToken = "rotated-refresh"
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, expired).
			DoAndReturn(func(refreshCtx context.Context, key string, _ *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
				assert.Nil(t, refreshCtx.Done(), "the worker runs the refresher detached from cancellation")
				user, ok := identity.CanonicalUserFromContext(refreshCtx)
				assert.True(t, ok)
				assert.Equal(t, readerUser, user, "detaching keeps the canonical user")
				close(started)
				<-release
				return refreshed, stor.StoreUpstreamTokens(context.WithoutCancel(refreshCtx), key, readerProvider, refreshed)
			})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		svc := NewInProcessService(stor, refresher)
		type result struct {
			cred *UpstreamCredential
			err  error
		}
		done := make(chan result, 1)
		go func() {
			cred, err := svc.GetValidTokensForUser(ctx, readerUser, readerProvider)
			done <- result{cred, err}
		}()

		<-started
		cancel()
		synctest.Wait()
		select {
		case got := <-done:
			assert.Nil(t, got.cred)
			assert.ErrorIs(t, got.err, ErrRefreshFailed)
			assert.ErrorIs(t, got.err, context.Canceled)
		default:
			t.Error("per-user read did not return while refresh was blocked")
		}

		unblock()
		synctest.Wait()
		stored, err := stor.GetUpstreamTokens(identity.WithCanonicalUser(t.Context(), readerUser), rowKey, readerProvider)
		require.NoError(t, err)
		assert.Equal(t, refreshed, stored)
	})
}

func TestGetValidTokensForUser_CancelDuringRefreshSuppressesExpiredIDToken(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		stor, _ := userKeyedStores(t)["memory"](t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		rowKey := userRowKey(readerUser)
		expiredIDToken := signedIDToken(t, time.Now().Add(-time.Hour))
		expired := readerRow(readerUser, time.Now().Add(-time.Hour))
		expired.IDToken = expiredIDToken
		require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(ctx, readerUser), rowKey, readerProvider, expired))

		started := make(chan struct{})
		release := make(chan struct{})
		// The provider omitted id_token, so the refresher carries the expired one forward.
		refreshed := readerRow(readerUser, time.Now().Add(time.Hour))
		refreshed.AccessToken = "new-access"
		refreshed.IDToken = expiredIDToken
		// Count calls instead of using Times(1): the refresher runs on the
		// service's worker goroutine, where a gomock failure would end that
		// goroutine and leave the read waiting forever.
		var calls atomic.Int32
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).AnyTimes().
			DoAndReturn(func(refreshCtx context.Context, key string, _ *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
				if calls.Add(1) > 1 {
					return refreshed, nil
				}
				close(started)
				<-release
				return refreshed, stor.StoreUpstreamTokens(context.WithoutCancel(refreshCtx), key, readerProvider, refreshed)
			})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		svc := NewInProcessService(stor, refresher)

		type result struct {
			cred *UpstreamCredential
			err  error
		}
		done := make(chan result, 1)
		go func() {
			cred, err := svc.GetValidTokensForUser(ctx, readerUser, readerProvider)
			done <- result{cred, err}
		}()

		<-started
		cancel()
		synctest.Wait()
		got := <-done
		assert.Nil(t, got.cred)
		assert.ErrorIs(t, got.err, ErrRefreshFailed)
		assert.ErrorIs(t, got.err, context.Canceled)

		unblock()
		synctest.Wait()

		cred, err := svc.GetValidTokensForUser(t.Context(), readerUser, readerProvider)
		require.NoError(t, err)
		assert.Equal(t, &UpstreamCredential{AccessToken: "new-access", IDToken: expiredIDToken}, cred)
		assert.Equal(t, int32(1), calls.Load(), "the second read must not call the refresher again")
	})
}

// liveAccessExpiredIDRow is the ID-token-only shape: the access token is live,
// the ID token has expired, and a refresh token exists.
func liveAccessExpiredIDRow(t *testing.T) *storage.UpstreamTokens {
	t.Helper()
	row := readerRow(readerUser, time.Now().Add(time.Hour))
	row.IDToken = signedIDToken(t, time.Now().Add(-time.Hour))
	return row
}

// readerResult carries one read's outcome out of the goroutine that ran it.
type readerResult struct {
	cred *UpstreamCredential
	err  error
}

// A refresh that fails after its caller left must back the ID-token trigger
// off and log the failure once, as for a failure the caller saw.
//
//nolint:paralleltest // Replaces the process-wide default logger.
func TestGetValidTokensForUser_CancelDuringIDTokenRefreshThenFailure(t *testing.T) {
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	synctest.Test(t, func(t *testing.T) {
		stor, _ := userKeyedStores(t)["memory"](t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		rowKey := userRowKey(readerUser)
		row := liveAccessExpiredIDRow(t)
		require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(ctx, readerUser), rowKey, readerProvider, row))

		started := make(chan struct{})
		release := make(chan struct{})
		var calls atomic.Int32
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).AnyTimes().
			DoAndReturn(func(context.Context, string, *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
				}
				return nil, errors.New("idp unavailable")
			})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		svc := NewInProcessService(stor, refresher)

		done := make(chan readerResult, 1)
		go func() {
			cred, err := svc.GetValidTokensForUser(ctx, readerUser, readerProvider)
			done <- readerResult{cred, err}
		}()
		<-started
		cancel()
		synctest.Wait()
		first := <-done
		require.NoError(t, first.err)
		assert.Equal(t, &UpstreamCredential{AccessToken: "access-" + readerUser, IDToken: row.IDToken}, first.cred)

		unblock()
		synctest.Wait()

		cred, err := svc.GetValidTokensForUser(t.Context(), readerUser, readerProvider)
		require.NoError(t, err)
		assert.Equal(t, first.cred, cred)
		assert.Equal(t, int32(1), calls.Load(), "the second read must not call the refresher again")
	})

	handler.mu.Lock()
	defer handler.mu.Unlock()
	warns := 0
	for _, level := range handler.levels {
		if level == slog.LevelWarn {
			warns++
		}
	}
	assert.Equal(t, 1, warns)
}

// Pins a documented trade-off: when the caller leaves during an ID-token-only
// refresh and the refresher then clears the row, that read still returns the
// stored credential and only the next read reports needs-authorization.
func TestGetValidTokensForUser_CancelDuringIDTokenRefreshThenRowCleared(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		stor, _ := userKeyedStores(t)["memory"](t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		rowKey := userRowKey(readerUser)
		row := liveAccessExpiredIDRow(t)
		require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(ctx, readerUser), rowKey, readerProvider, row))

		started := make(chan struct{})
		release := make(chan struct{})
		var calls atomic.Int32
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).AnyTimes().
			DoAndReturn(func(refreshCtx context.Context, key string, _ *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
				}
				cleared := *row
				cleared.RefreshToken = ""
				cleared.ExpiresAt = time.Now().Add(-time.Minute)
				if err := stor.StoreUpstreamTokens(refreshCtx, key, readerProvider, &cleared); err != nil {
					return nil, err
				}
				return nil, errors.Join(errors.New("rotation not saved"), storage.ErrNotFound)
			})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		svc := NewInProcessService(stor, refresher)

		done := make(chan readerResult, 1)
		go func() {
			cred, err := svc.GetValidTokensForUser(ctx, readerUser, readerProvider)
			done <- readerResult{cred, err}
		}()
		<-started
		cancel()
		synctest.Wait()
		first := <-done
		require.NoError(t, first.err)
		assert.Equal(t, &UpstreamCredential{AccessToken: "access-" + readerUser, IDToken: row.IDToken}, first.cred)

		unblock()
		synctest.Wait()

		cred, err := svc.GetValidTokensForUser(t.Context(), readerUser, readerProvider)
		assert.Nil(t, cred)
		assert.ErrorIs(t, err, ErrNeedsAuthorization)
	})
}

func TestGetValidTokensForUser_SkipsRefreshWhenContextAlreadyDone(t *testing.T) {
	t.Parallel()

	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if cause == context.DeadlineExceeded {
				deadlineCtx, cancelDeadline := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				t.Cleanup(cancelDeadline)
				ctx = deadlineCtx
			}
			ctrl := gomock.NewController(t)
			stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			// Some stores still return the row when the caller's context ends.
			stor.EXPECT().GetUpstreamTokens(gomock.Any(), userRowKey(readerUser), readerProvider).
				Return(readerRow(readerUser, time.Now().Add(-time.Hour)), storage.ErrExpired)
			refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl) // no refresh expected

			cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(ctx, readerUser, readerProvider)

			assert.Nil(t, cred)
			assert.ErrorIs(t, err, ErrRefreshFailed)
			assert.ErrorIs(t, err, cause)
		})
	}
}

func TestGetValidTokensForUser_RefreshFailures(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	rowKey := userRowKey(readerUser)

	tests := []struct {
		name          string
		refreshErr    error
		wantAuth      bool
		wantBinding   bool
		wantReachable []error
	}{
		{
			name:          "invalid_grant needs authorization",
			refreshErr:    fmt.Errorf("upstream token refresh failed: %w", upstream.ErrGrantRejected),
			wantAuth:      true,
			wantReachable: []error{ErrRefreshFailed, upstream.ErrGrantRejected},
		},
		{
			name:          "row gone after a lost refresh race needs authorization",
			refreshErr:    fmt.Errorf("re-read: %w", storage.ErrNotFound),
			wantAuth:      true,
			wantReachable: []error{storage.ErrNotFound},
		},
		{
			name: "rotated refresh token not persisted and row deleted needs authorization",
			refreshErr: fmt.Errorf(
				"failed to persist rotated upstream refresh token for provider %q; row deleted: %w: %w",
				readerProvider, storage.ErrNotFound, errors.New("redis connection lost")),
			wantAuth:      true,
			wantReachable: []error{ErrRefreshFailed, storage.ErrNotFound},
		},
		{
			name:          "foreign row found during the refresh is a binding error",
			refreshErr:    storage.ErrInvalidBinding,
			wantBinding:   true,
			wantReachable: []error{storage.ErrInvalidBinding},
		},
		{
			name:          "5xx is an infrastructure error",
			refreshErr:    errors.New("upstream token refresh failed: 503 service unavailable"),
			wantReachable: []error{ErrRefreshFailed},
		},
		{
			name:          "lost refresh race is an infrastructure error",
			refreshErr:    fmt.Errorf("concurrent refresh: %w", storage.ErrConcurrentRefresh),
			wantReachable: []error{storage.ErrConcurrentRefresh},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).
				Return(readerRow(readerUser, time.Now().Add(-time.Hour)), storage.ErrExpired)
			refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
			refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).Return(nil, tt.refreshErr)

			cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

			assert.Nil(t, cred)
			require.Error(t, err)
			assert.Equal(t, tt.wantAuth, errors.Is(err, ErrNeedsAuthorization))
			assert.Equal(t, tt.wantBinding, errors.Is(err, ErrInvalidBinding))
			for _, want := range tt.wantReachable {
				assert.ErrorIs(t, err, want)
			}
		})
	}
}

func TestGetValidTokensForUser_StorageErrors(t *testing.T) {
	t.Parallel()
	bg := context.Background()

	t.Run("storage failure is an infrastructure error", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("connection refused")
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), gomock.Any(), readerProvider).Return(nil, boom)

		cred, err := NewInProcessService(stor, nil).GetValidTokensForUser(bg, readerUser, readerProvider)

		assert.Nil(t, cred)
		assert.ErrorIs(t, err, boom)
		assert.NotErrorIs(t, err, ErrNeedsAuthorization)
		assert.NotErrorIs(t, err, ErrInvalidBinding)
	})

	t.Run("a missing canonical user surfaces unchanged", func(t *testing.T) {
		t.Parallel()
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), gomock.Any(), readerProvider).Return(nil, ErrNoCanonicalUser)

		_, err := NewInProcessService(stor, nil).GetValidTokensForUser(bg, readerUser, readerProvider)

		assert.ErrorIs(t, err, storage.ErrNoCanonicalUser)
		assert.NotErrorIs(t, err, ErrNeedsAuthorization)
	})

	t.Run("storage that reports a binding error maps to the service's", func(t *testing.T) {
		t.Parallel()
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), gomock.Any(), readerProvider).Return(nil, storage.ErrInvalidBinding)

		_, err := NewInProcessService(stor, nil).GetValidTokensForUser(bg, readerUser, readerProvider)

		assert.ErrorIs(t, err, ErrInvalidBinding)
		assert.ErrorIs(t, err, storage.ErrInvalidBinding)
	})

	for _, expiresAt := range []time.Time{time.Now().Add(time.Hour), time.Now().Add(-time.Hour)} {
		t.Run(fmt.Sprintf("foreign row over storage that is not user-keyed, expires %s", time.Until(expiresAt).Round(time.Hour)),
			func(t *testing.T) {
				t.Parallel()
				stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
				var readErr error
				if expiresAt.Before(time.Now()) {
					readErr = storage.ErrExpired
				}
				stor.EXPECT().GetUpstreamTokens(gomock.Any(), gomock.Any(), readerProvider).
					Return(readerRow(readerIntruder, expiresAt), readErr)
				refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))

				cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

				assert.Nil(t, cred)
				assert.ErrorIs(t, err, ErrInvalidBinding)
			})
	}

	t.Run("empty user is not found and touches nothing", func(t *testing.T) {
		t.Parallel()
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))

		cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, "", readerProvider)

		assert.Nil(t, cred)
		assert.ErrorIs(t, err, ErrSessionNotFound)
		assert.NotErrorIs(t, err, ErrNeedsAuthorization)
	})

	t.Run("re-exported sentinel is storage's", func(t *testing.T) {
		t.Parallel()
		assert.Same(t, storage.ErrNoCanonicalUser, ErrNoCanonicalUser)
	})
}

type recordingHandler struct {
	mu     sync.Mutex
	levels []slog.Level
}

func (*recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.levels = append(h.levels, r.Level)
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

//nolint:paralleltest // Replaces the process-wide default logger.
func TestGetValidTokensForUser_LogsNoWarnOrErrorForFailedRefresh(t *testing.T) {
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctrl := gomock.NewController(t)
	stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	stor.EXPECT().GetUpstreamTokens(gomock.Any(), gomock.Any(), readerProvider).
		Return(readerRow(readerUser, time.Now().Add(-time.Hour)), storage.ErrExpired)
	refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
	refresher.EXPECT().RefreshAndStore(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("503 service unavailable"))

	_, err := NewInProcessService(stor, refresher).GetValidTokensForUser(context.Background(), readerUser, readerProvider)

	require.Error(t, err)
	handler.mu.Lock()
	defer handler.mu.Unlock()
	for _, level := range handler.levels {
		assert.Less(t, level, slog.LevelWarn)
	}
}

// The reader returns the stored credential after a failed ID-token refresh, so
// its caller never sees the failure; the one WARN is the only signal.
//
//nolint:paralleltest // Replaces the process-wide default logger.
func TestGetValidTokensForUser_WarnsOnceForIDTokenFallback(t *testing.T) {
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	rowKey := userRowKey(readerUser)
	row := readerRow(readerUser, time.Now().Add(time.Hour))
	row.IDToken = signedIDToken(t, time.Now().Add(-10*time.Minute))
	ctrl := gomock.NewController(t)
	stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	allowRowIDResolution(stor)
	stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).Return(row, nil).Times(2)
	refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
	refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).
		Return(nil, errors.New("upstream token refresh failed: 503"))

	cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(context.Background(), readerUser, readerProvider)

	require.NoError(t, err)
	assert.Equal(t, "access-"+readerUser, cred.AccessToken)
	handler.mu.Lock()
	defer handler.mu.Unlock()
	warns := 0
	for _, level := range handler.levels {
		assert.Less(t, level, slog.LevelError)
		if level == slog.LevelWarn {
			warns++
		}
	}
	assert.Equal(t, 1, warns)
}

// The session-based read keeps its WARN: the reader's quiet path must not
// change what existing callers log.
//
//nolint:paralleltest // Replaces the process-wide default logger.
func TestGetValidTokens_StillWarnsForFailedRefresh(t *testing.T) {
	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctrl := gomock.NewController(t)
	stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	stor.EXPECT().GetUpstreamTokens(gomock.Any(), "s", readerProvider).
		Return(readerRow(readerUser, time.Now().Add(-time.Hour)), storage.ErrExpired)
	refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
	refresher.EXPECT().RefreshAndStore(gomock.Any(), "s", gomock.Any()).Return(nil, errors.New("503"))

	_, err := NewInProcessService(stor, refresher).GetValidTokens(context.Background(), "s", readerProvider)

	require.ErrorIs(t, err, ErrRefreshFailed)
	handler.mu.Lock()
	defer handler.mu.Unlock()
	assert.Contains(t, handler.levels, slog.LevelWarn)
}

func TestGetValidTokensForUser_ExpiryPaths(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	rowKey := userRowKey(readerUser)

	t.Run("expired ID token on a live row refreshes it", func(t *testing.T) {
		t.Parallel()
		row := readerRow(readerUser, time.Now().Add(time.Hour))
		row.IDToken = signedIDToken(t, time.Now().Add(-10*time.Minute))
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		allowRowIDResolution(stor)
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).Return(row, nil)
		refreshed := readerRow(readerUser, time.Now().Add(time.Hour))
		refreshed.AccessToken = "new-access"
		refreshed.IDToken = "new-id"
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).Return(refreshed, nil)

		cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

		require.NoError(t, err)
		assert.Equal(t, &UpstreamCredential{AccessToken: "new-access", IDToken: "new-id"}, cred)
	})

	t.Run("failed refresh for an expired ID token keeps the live access token", func(t *testing.T) {
		t.Parallel()
		row := readerRow(readerUser, time.Now().Add(time.Hour))
		row.IDToken = signedIDToken(t, time.Now().Add(-10*time.Minute))
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		allowRowIDResolution(stor)
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).Return(row, nil).Times(2)
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).
			Return(nil, errors.New("upstream token refresh failed: 503"))

		cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

		require.NoError(t, err)
		assert.Equal(t, "access-"+readerUser, cred.AccessToken)
	})

	for _, tt := range []struct {
		name        string
		readErr     error
		wantAuth    bool
		wantBinding bool
		wantCause   error
	}{
		{
			name:      "row deleted by a failed ID token refresh needs authorization",
			readErr:   storage.ErrNotFound,
			wantAuth:  true,
			wantCause: storage.ErrNotFound,
		},
		{
			name:        "foreign row found when re-reading after a failed ID token refresh is a binding error",
			readErr:     storage.ErrInvalidBinding,
			wantBinding: true,
			wantCause:   storage.ErrInvalidBinding,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			row := readerRow(readerUser, time.Now().Add(time.Hour))
			row.IDToken = signedIDToken(t, time.Now().Add(-10*time.Minute))
			ctrl := gomock.NewController(t)
			stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			allowRowIDResolution(stor)
			gomock.InOrder(
				stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).Return(row, nil),
				stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).Return(nil, tt.readErr),
			)
			refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
			refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).
				Return(nil, errors.New("upstream token refresh failed: 503"))

			cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

			assert.Nil(t, cred)
			require.Error(t, err)
			assert.Equal(t, tt.wantAuth, errors.Is(err, ErrNeedsAuthorization))
			assert.Equal(t, tt.wantBinding, errors.Is(err, ErrInvalidBinding))
			assert.ErrorIs(t, err, tt.wantCause)
		})
	}

	t.Run("live access token with an expired ID token ignores invalid_grant and returns the stored credential", func(t *testing.T) {
		t.Parallel()
		row := readerRow(readerUser, time.Now().Add(time.Hour))
		row.IDToken = signedIDToken(t, time.Now().Add(-10*time.Minute))
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		allowRowIDResolution(stor)
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).Return(row, nil).Times(2)
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).
			Return(nil, fmt.Errorf("upstream token refresh failed: %w", upstream.ErrGrantRejected))

		cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

		require.NoError(t, err)
		assert.Equal(t, &UpstreamCredential{AccessToken: row.AccessToken, IDToken: row.IDToken}, cred)
	})

	t.Run("binding error from the refresher for an expired ID token is not masked by the stored credential", func(t *testing.T) {
		t.Parallel()
		row := readerRow(readerUser, time.Now().Add(time.Hour))
		row.IDToken = signedIDToken(t, time.Now().Add(-10*time.Minute))
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		allowRowIDResolution(stor)
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).Return(row, nil).AnyTimes()
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).
			Return(nil, fmt.Errorf("%w: %w", ErrRefreshFailed, storage.ErrInvalidBinding))

		cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

		assert.Nil(t, cred)
		assert.ErrorIs(t, err, ErrInvalidBinding)
	})

	t.Run("expired read without a row needs authorization", func(t *testing.T) {
		t.Parallel()
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		allowRowIDResolution(stor)
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).Return(nil, storage.ErrExpired)

		cred, err := NewInProcessService(stor, nil).GetValidTokensForUser(bg, readerUser, readerProvider)

		assert.Nil(t, cred)
		assert.ErrorIs(t, err, ErrNeedsAuthorization)
		assert.ErrorIs(t, err, ErrNoRefreshToken)
	})

	t.Run("row past its expiry without ErrExpired from storage is refreshed", func(t *testing.T) {
		t.Parallel()
		stor := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
		allowRowIDResolution(stor)
		stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).
			Return(readerRow(readerUser, time.Now().Add(-time.Hour)), nil)
		refreshed := readerRow(readerUser, time.Now().Add(time.Hour))
		refreshed.AccessToken = "new-access"
		refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
		refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).Return(refreshed, nil)

		cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

		require.NoError(t, err)
		assert.Equal(t, "new-access", cred.AccessToken)
	})
}

func TestGetValidTokensForUser_RefreshedRowOwnedByAnotherUser(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	rowKey := userRowKey(readerUser)

	tests := []struct {
		name string
		row  func(t *testing.T) *storage.UpstreamTokens
		read error
	}{
		{
			name: "expired access token",
			row: func(*testing.T) *storage.UpstreamTokens {
				return readerRow(readerUser, time.Now().Add(-time.Hour))
			},
			read: storage.ErrExpired,
		},
		{
			name: "expired ID token with a live access token",
			row: func(t *testing.T) *storage.UpstreamTokens {
				t.Helper()
				row := readerRow(readerUser, time.Now().Add(time.Hour))
				row.IDToken = signedIDToken(t, time.Now().Add(-10*time.Minute))
				return row
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			allowRowIDResolution(stor)
			stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).
				Return(tt.row(t), tt.read).AnyTimes()
			refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
			refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).
				Return(readerRow(readerIntruder, time.Now().Add(time.Hour)), nil)

			cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

			assert.Nil(t, cred)
			assert.ErrorIs(t, err, ErrInvalidBinding)
			assert.NotErrorIs(t, err, ErrNeedsAuthorization)
		})
	}
}

func TestGetValidTokensForUser_RefreshedCredentialWithoutAccessToken(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	rowKey := userRowKey(readerUser)

	tests := []struct {
		name string
		row  func(t *testing.T) *storage.UpstreamTokens
		read error
	}{
		{
			name: "expired access token",
			row: func(*testing.T) *storage.UpstreamTokens {
				return readerRow(readerUser, time.Now().Add(-time.Hour))
			},
			read: storage.ErrExpired,
		},
		{
			name: "expired ID token with a live access token",
			row: func(t *testing.T) *storage.UpstreamTokens {
				t.Helper()
				row := readerRow(readerUser, time.Now().Add(time.Hour))
				row.IDToken = signedIDToken(t, time.Now().Add(-10*time.Minute))
				return row
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			stor := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			allowRowIDResolution(stor)
			stor.EXPECT().GetUpstreamTokens(gomock.Any(), rowKey, readerProvider).
				Return(tt.row(t), tt.read).AnyTimes()
			refreshed := readerRow(readerUser, time.Now().Add(time.Hour))
			refreshed.AccessToken = ""
			refresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
			refresher.EXPECT().RefreshAndStore(gomock.Any(), rowKey, gomock.Any()).Return(refreshed, nil)

			cred, err := NewInProcessService(stor, refresher).GetValidTokensForUser(bg, readerUser, readerProvider)

			assert.Nil(t, cred)
			assert.ErrorIs(t, err, ErrNeedsAuthorization)
			assert.ErrorIs(t, err, ErrSessionNotFound)
		})
	}
}

// TestIDTokenSuppression_SharedAcrossReadPaths checks that the ID-token
// suppression follows the storage row, not the session ID a path reads it
// with. Under user-keyed storage the per-user reader and the session-level
// reads address the same row, so once a refresh left the ID token expired,
// neither path refreshes again; a different user's row is suppressed on its
// own.
func TestIDTokenSuppression_SharedAcrossReadPaths(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	expiredIDToken := signedIDToken(t, time.Now().Add(-10*time.Minute))

	for storeName, newStore := range userKeyedStores(t) {
		t.Run(storeName, func(t *testing.T) {
			t.Parallel()
			stor, _ := newStore(t)
			for _, user := range []string{readerUser, readerIntruder} {
				row := readerRow(user, time.Now().Add(time.Hour))
				row.IDToken = expiredIDToken
				require.NoError(t, stor.StoreUpstreamTokens(identity.WithCanonicalUser(bg, user), "", readerProvider, row))
			}

			// The provider omits id_token, so every refresh leaves the ID token
			// expired and records a suppression.
			var mu sync.Mutex
			refreshes := map[string]int{}
			refresher := storagemocks.NewMockUpstreamTokenRefresher(gomock.NewController(t))
			refresher.EXPECT().RefreshAndStore(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().
				DoAndReturn(func(_ context.Context, _ string, in *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
					mu.Lock()
					refreshes[in.UserID]++
					mu.Unlock()
					out := *in
					out.ExpiresAt = time.Now().Add(time.Hour)
					return &out, nil
				})
			svc := NewInProcessService(stor, refresher)

			bulk := func(user, tsid string) {
				t.Helper()
				creds, failed, err := svc.GetAllUpstreamCredentials(identity.WithCanonicalUser(bg, user), tsid)
				require.NoError(t, err)
				assert.Empty(t, failed)
				assert.Equal(t, expiredIDToken, creds[readerProvider].IDToken)
			}
			single := func(user, tsid string) {
				t.Helper()
				cred, err := svc.GetValidTokens(identity.WithCanonicalUser(bg, user), tsid, readerProvider)
				require.NoError(t, err)
				assert.Equal(t, expiredIDToken, cred.IDToken)
			}
			forUser := func(user string) {
				t.Helper()
				cred, err := svc.GetValidTokensForUser(bg, user, readerProvider)
				require.NoError(t, err)
				assert.Equal(t, expiredIDToken, cred.IDToken)
			}

			// User A: the per-user reader refreshes first; the session paths,
			// under two different tsids, then find the trigger suppressed.
			forUser(readerUser)
			bulk(readerUser, "tsid-a1")
			single(readerUser, "tsid-a2")

			// User B: the other way round. A's suppression does not cover B's row.
			bulk(readerIntruder, "tsid-b1")
			forUser(readerIntruder)

			assert.Equal(t, map[string]int{readerUser: 1, readerIntruder: 1}, refreshes)
		})
	}
}
