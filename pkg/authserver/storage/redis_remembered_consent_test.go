// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // parallel execution handled by withRedisStorage helper
func TestRedisRememberedConsentLegacyJSON(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
		expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		expiresAt := expiry.Format(time.RFC3339)

		session := &ConsentSession{UserID: "user", ProviderID: "first", ProviderSubject: "subject", ExpiresAt: expiry}
		sessionJSON := fmt.Sprintf(`{"UserID":"user","ProviderID":"first","ProviderSubject":"subject","ExpiresAt":%q}`, expiresAt)
		sessionKey := redisKey(s.keyPrefix, KeyTypeConsentSession, "digest")
		require.NoError(t, mr.Set(sessionKey, sessionJSON))
		gotSession, err := s.GetConsentSession(ctx, "digest")
		require.NoError(t, err)
		require.Equal(t, session, gotSession)

		require.NoError(t, s.StoreConsentSession(ctx, "digest", session))
		rawSession, err := mr.Get(sessionKey)
		require.NoError(t, err)
		require.JSONEq(t, sessionJSON, rawSession)

		approval := &ClientApproval{RedirectURI: "https://client.example/callback", Scopes: []string{"read"}, Resource: "https://resource.example", ExpiresAt: expiry}
		approvalJSON := fmt.Sprintf(`{"RedirectURI":"https://client.example/callback","Scopes":["read"],"Resource":"https://resource.example","ExpiresAt":%q}`, expiresAt)
		key, err := approvalKey(s.keyPrefix, "user", "client")
		require.NoError(t, err)
		require.NoError(t, mr.Set(key, approvalJSON))
		gotApproval, err := s.GetClientApproval(ctx, "user", "client")
		require.NoError(t, err)
		require.Equal(t, approval, gotApproval)

		require.NoError(t, s.StoreClientApproval(ctx, "user", "client", approval))
		rawApproval, err := mr.Get(key)
		require.NoError(t, err)
		require.JSONEq(t, approvalJSON, rawApproval)
	})
}

//nolint:paralleltest // parallel execution handled by withRedisStorage helper
func TestRedisRememberedPendingRoundTrip(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		want := &PendingAuthorization{ClientID: "client", Scopes: []string{"openid"}, CreatedAt: time.Now(), ConsentStage: ConsentStageApproved,
			ExpectedUserID: "user", ExpectedProviderSubject: "sub", ConsentSessionDigest: "digest", FirstProviderSubject: "sub", RememberConsent: true}
		require.NoError(t, s.StorePendingAuthorization(ctx, "pending", want))
		got, err := s.LoadPendingAuthorization(ctx, "pending")
		require.NoError(t, err)
		require.Equal(t, want.ExpectedUserID, got.ExpectedUserID)
		require.Equal(t, want.ExpectedProviderSubject, got.ExpectedProviderSubject)
		require.Equal(t, want.ConsentSessionDigest, got.ConsentSessionDigest)
		require.True(t, got.RememberConsent)
	})
}

//nolint:paralleltest // parallel execution handled by withRedisStorage helper
func TestRedisRememberedConsent(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
		expiry := time.Now().Add(time.Hour)
		_, err := s.GetConsentSession(ctx, "digest")
		require.ErrorIs(t, err, ErrNotFound)
		require.NoError(t, s.StoreConsentSession(ctx, "digest", &ConsentSession{UserID: "user", ProviderID: "first", ProviderSubject: "sub", ExpiresAt: expiry}))
		key := redisKey(s.keyPrefix, KeyTypeConsentSession, "digest")
		require.InDelta(t, time.Hour.Seconds(), mr.TTL(key).Seconds(), 2)
		got, err := s.GetConsentSession(ctx, "digest")
		require.NoError(t, err)
		require.Equal(t, "sub", got.ProviderSubject)
		require.NoError(t, s.DeleteConsentSession(ctx, "digest"))
		_, err = s.GetConsentSession(ctx, "digest")
		require.ErrorIs(t, err, ErrNotFound)
		approval := &ClientApproval{Scopes: []string{"read"}, ExpiresAt: expiry}
		require.NoError(t, s.StoreClientApproval(ctx, "user", "client", approval))
		approval.Scopes[0] = "write"
		key, err = approvalKey(s.keyPrefix, "user", "client")
		require.NoError(t, err)
		require.InDelta(t, time.Hour.Seconds(), mr.TTL(key).Seconds(), 2)
		gotApproval, err := s.GetClientApproval(ctx, "user", "client")
		require.NoError(t, err)
		require.Equal(t, []string{"read"}, gotApproval.Scopes)
		_, err = s.GetClientApproval(ctx, "user", "other")
		require.ErrorIs(t, err, ErrNotFound)
		require.NoError(t, s.StoreClientApproval(ctx, "user", "client", &ClientApproval{Scopes: []string{"write"}, ExpiresAt: time.Now().Add(30 * time.Minute)}))
		require.InDelta(t, (30 * time.Minute).Seconds(), mr.TTL(key).Seconds(), 2)
		mr.FastForward(time.Hour)
		_, err = s.GetClientApproval(ctx, "user", "client")
		require.ErrorIs(t, err, ErrNotFound)
		require.ErrorIs(t, s.StoreClientApproval(ctx, "user", "client", &ClientApproval{ExpiresAt: time.Now().Add(-time.Second)}), ErrExpired)
	})
}

//nolint:paralleltest // parallel execution handled by withRedisStorage helper
func TestRedisRememberedConsentStoreValidation(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		future := time.Now().Add(time.Hour)
		for _, tt := range []struct {
			name  string
			store func() error
			want  error
		}{
			{name: "empty session digest", store: func() error { return s.StoreConsentSession(ctx, "", &ConsentSession{ExpiresAt: future}) }, want: fosite.ErrInvalidRequest},
			{name: "nil session", store: func() error { return s.StoreConsentSession(ctx, "digest", nil) }, want: fosite.ErrInvalidRequest},
			{name: "expired session", store: func() error {
				return s.StoreConsentSession(ctx, "digest", &ConsentSession{ExpiresAt: time.Now().Add(-time.Second)})
			}, want: ErrExpired},
			{name: "empty approval user ID", store: func() error { return s.StoreClientApproval(ctx, "", "client", &ClientApproval{ExpiresAt: future}) }, want: fosite.ErrInvalidRequest},
			{name: "empty approval client ID", store: func() error { return s.StoreClientApproval(ctx, "user", "", &ClientApproval{ExpiresAt: future}) }, want: fosite.ErrInvalidRequest},
			{name: "nil approval", store: func() error { return s.StoreClientApproval(ctx, "user", "client", nil) }, want: fosite.ErrInvalidRequest},
			{name: "expired approval", store: func() error {
				return s.StoreClientApproval(ctx, "user", "client", &ClientApproval{ExpiresAt: time.Now().Add(-time.Second)})
			}, want: ErrExpired},
		} {
			t.Run(tt.name, func(t *testing.T) {
				require.ErrorIs(t, tt.store(), tt.want)
			})
		}
	})
}

//nolint:paralleltest // parallel execution handled by withRedisStorage helper
func TestRedisRememberedConsentStoreErrorsIncludeContext(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		expiresAt := time.Now().Add(time.Hour)

		for _, tt := range []struct {
			name string
			err  error
			want string
		}{
			{name: "store session", err: s.StoreConsentSession(canceled, "digest", &ConsentSession{ExpiresAt: expiresAt}), want: "store consent session:"},
			{name: "store approval", err: s.StoreClientApproval(canceled, "user", "client", &ClientApproval{ExpiresAt: expiresAt}), want: "store client approval:"},
			{name: "delete session", err: s.DeleteConsentSession(canceled, "digest"), want: "delete consent session:"},
		} {
			require.ErrorIs(t, tt.err, context.Canceled, tt.name)
			require.Contains(t, tt.err.Error(), tt.want)
		}
	})
}
