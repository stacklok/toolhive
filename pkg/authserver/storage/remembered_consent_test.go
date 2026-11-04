// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"
)

func TestMemoryRememberedPendingRoundTrip(t *testing.T) {
	t.Parallel()
	s := NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	want := &PendingAuthorization{
		ClientID: "client", Scopes: []string{"openid"}, CreatedAt: time.Now(), ConsentStage: ConsentStageApproved,
		ExpectedUserID: "user", ExpectedProviderSubject: "sub", ConsentSessionDigest: "digest", FirstProviderSubject: "sub", RememberConsent: true,
	}
	require.NoError(t, s.StorePendingAuthorization(ctx, "pending", want))
	got, err := s.LoadPendingAuthorization(ctx, "pending")
	require.NoError(t, err)
	require.Equal(t, want.ExpectedUserID, got.ExpectedUserID)
	require.Equal(t, want.ExpectedProviderSubject, got.ExpectedProviderSubject)
	require.Equal(t, want.ConsentSessionDigest, got.ConsentSessionDigest)
	require.True(t, got.RememberConsent)
}

func TestMemoryRememberedConsent(t *testing.T) {
	t.Parallel()
	s := NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	_, err := s.GetConsentSession(ctx, "digest")
	require.ErrorIs(t, err, ErrNotFound)
	expiry := time.Now().Add(time.Hour)
	session := &ConsentSession{UserID: "user", ProviderID: "first", ProviderSubject: "sub", ExpiresAt: expiry}
	require.NoError(t, s.StoreConsentSession(ctx, "digest", session))
	session.UserID = "mutated"
	got, err := s.GetConsentSession(ctx, "digest")
	require.NoError(t, err)
	require.Equal(t, "user", got.UserID)
	got.UserID = "also mutated"
	got, err = s.GetConsentSession(ctx, "digest")
	require.NoError(t, err)
	require.Equal(t, "user", got.UserID)
	require.NoError(t, s.DeleteConsentSession(ctx, "digest"))
	_, err = s.GetConsentSession(ctx, "digest")
	require.ErrorIs(t, err, ErrNotFound)
	approval := &ClientApproval{RedirectURI: "https://client/cb", Scopes: []string{"read"}, Resource: "https://resource", ExpiresAt: expiry}
	require.NoError(t, s.StoreClientApproval(ctx, "user", "client", approval))
	approval.Scopes[0] = "write"
	gotApproval, err := s.GetClientApproval(ctx, "user", "client")
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, gotApproval.Scopes)
	gotApproval.Scopes[0] = "write"
	gotApproval, err = s.GetClientApproval(ctx, "user", "client")
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, gotApproval.Scopes)
	_, err = s.GetClientApproval(ctx, "user", "other")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.StoreClientApproval(ctx, "user", "client", &ClientApproval{Scopes: []string{"write"}, ExpiresAt: expiry}))
	gotApproval, err = s.GetClientApproval(ctx, "user", "client")
	require.NoError(t, err)
	require.Equal(t, []string{"write"}, gotApproval.Scopes)
	s.mu.Lock()
	s.consentSessions["expired"] = &timedEntry[*ConsentSession]{value: &ConsentSession{ExpiresAt: time.Now().Add(-time.Second)}, expiresAt: time.Now().Add(-time.Second)}
	s.clientApprovals[[2]string{"user", "expired"}] = &timedEntry[*ClientApproval]{value: &ClientApproval{}, expiresAt: time.Now().Add(-time.Second)}
	s.mu.Unlock()
	_, err = s.GetConsentSession(ctx, "expired")
	require.True(t, errors.Is(err, ErrExpired))
	_, err = s.GetClientApproval(ctx, "user", "expired")
	require.ErrorIs(t, err, ErrExpired)
	s.cleanupExpired()
	_, err = s.GetConsentSession(ctx, "expired")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetClientApproval(ctx, "user", "expired")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryRememberedConsentStoreValidation(t *testing.T) {
	t.Parallel()
	s := NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
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
			t.Parallel()
			require.ErrorIs(t, tt.store(), tt.want)
		})
	}
}
