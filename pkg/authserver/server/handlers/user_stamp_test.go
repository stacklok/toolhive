// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

// swappingStore is a mock user store that also exposes the swapper capability.
type swappingStore struct {
	*mocks.MockUserStorage
	*mocks.MockProviderIdentitySwapper
}

func newSwappingStore(t *testing.T) (swappingStore, *mocks.MockUserStorage, *mocks.MockProviderIdentitySwapper) {
	t.Helper()
	ctrl := gomock.NewController(t)
	users := mocks.NewMockUserStorage(ctrl)
	swapper := mocks.NewMockProviderIdentitySwapper(ctrl)
	return swappingStore{MockUserStorage: users, MockProviderIdentitySwapper: swapper}, users, swapper
}

const (
	stampProvider = "default"
	stampSubject  = "sub-1"
	stampIssuer   = "https://idp.example.com"
	stampClaim    = "sub"
	otherIssuer   = "https://other-idp.example.com"
)

func unstampedRow(userID string) *storage.ProviderIdentity {
	return &storage.ProviderIdentity{UserID: userID, ProviderID: stampProvider, ProviderSubject: stampSubject}
}

func stampedRow(userID, issuer string) *storage.ProviderIdentity {
	row := unstampedRow(userID)
	row.Issuer, row.SubjectClaim = issuer, stampClaim
	return row
}

func TestResolveUser_LostStampRereadsRow(t *testing.T) {
	t.Parallel()
	changed := storage.ErrProviderIdentityChanged

	tests := []struct {
		name string
		// expect sets the calls after the first read returned an unstamped row owned by "old-user".
		expect   func(users *mocks.MockUserStorage, swapper *mocks.MockProviderIdentitySwapper)
		wantUser string
		// wantNewUser means the result must be a freshly created user.
		wantNewUser bool
		wantErr     error
	}{
		{
			name: "row restamped with another binding takes the mismatch path",
			expect: func(users *mocks.MockUserStorage, swapper *mocks.MockProviderIdentitySwapper) {
				gomock.InOrder(
					users.EXPECT().GetUser(gomock.Any(), "old-user").Return(&storage.User{ID: "old-user"}, nil),
					swapper.EXPECT().CompareAndSwapProviderIdentity(gomock.Any(), gomock.Any(), gomock.Any()).Return(changed),
					users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).
						Return(stampedRow("old-user", otherIssuer), nil),
					users.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(nil),
					swapper.EXPECT().CompareAndSwapProviderIdentity(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil),
				)
			},
			wantNewUser: true,
		},
		{
			name: "row stamped with the same binding returns its owner",
			expect: func(users *mocks.MockUserStorage, swapper *mocks.MockProviderIdentitySwapper) {
				gomock.InOrder(
					users.EXPECT().GetUser(gomock.Any(), "old-user").Return(&storage.User{ID: "old-user"}, nil),
					swapper.EXPECT().CompareAndSwapProviderIdentity(gomock.Any(), gomock.Any(), gomock.Any()).Return(changed),
					users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).
						Return(stampedRow("new-owner", stampIssuer), nil),
					users.EXPECT().GetUser(gomock.Any(), "new-owner").Return(&storage.User{ID: "new-owner"}, nil),
				)
			},
			wantUser: "new-owner",
		},
		{
			name: "a second lost stamp is an error, not the stale user",
			expect: func(users *mocks.MockUserStorage, swapper *mocks.MockProviderIdentitySwapper) {
				gomock.InOrder(
					users.EXPECT().GetUser(gomock.Any(), "old-user").Return(&storage.User{ID: "old-user"}, nil),
					swapper.EXPECT().CompareAndSwapProviderIdentity(gomock.Any(), gomock.Any(), gomock.Any()).Return(changed),
					users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(unstampedRow("old-user"), nil),
					users.EXPECT().GetUser(gomock.Any(), "old-user").Return(&storage.User{ID: "old-user"}, nil),
					swapper.EXPECT().CompareAndSwapProviderIdentity(gomock.Any(), gomock.Any(), gomock.Any()).
						Return(storage.ErrNotFound),
				)
			},
			wantErr: errStampRaceLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, users, swapper := newSwappingStore(t)
			users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).
				Return(unstampedRow("old-user"), nil)
			tt.expect(users, swapper)

			r := NewUserResolver(store, WithIdentityBinding(stampProvider, stampIssuer, stampClaim))
			got, err := r.ResolveUser(context.Background(), stampProvider, stampSubject)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.wantNewUser {
				assert.NotEqual(t, "old-user", got.ID, "the stale user must not be returned")
				assert.NotEmpty(t, got.ID)
				return
			}
			assert.Equal(t, tt.wantUser, got.ID)
		})
	}
}

func TestResolveLinkFailure_AdoptRereadsAfterLostStamp(t *testing.T) {
	t.Parallel()
	changed := storage.ErrProviderIdentityChanged

	tests := []struct {
		name     string
		reread   *storage.ProviderIdentity
		wantUser string
		wantErr  error
	}{
		{name: "same binding returns that owner", reread: stampedRow("owner-2", stampIssuer), wantUser: "owner-2"},
		{name: "another binding is refused", reread: stampedRow("old-user", otherIssuer), wantErr: ErrIdentityBindingMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, users, swapper := newSwappingStore(t)
			calls := []any{
				users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(nil, storage.ErrNotFound),
				users.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(nil),
				users.EXPECT().CreateProviderIdentity(gomock.Any(), gomock.Any()).Return(storage.ErrAlreadyExists),
				users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(unstampedRow("old-user"), nil),
				users.EXPECT().DeleteUser(gomock.Any(), gomock.Any()).Return(nil),
				users.EXPECT().GetUser(gomock.Any(), "old-user").Return(&storage.User{ID: "old-user"}, nil),
				swapper.EXPECT().CompareAndSwapProviderIdentity(gomock.Any(), gomock.Any(), gomock.Any()).Return(changed),
				users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(tt.reread, nil),
			}
			if tt.wantUser != "" {
				calls = append(calls, users.EXPECT().GetUser(gomock.Any(), tt.wantUser).Return(&storage.User{ID: tt.wantUser}, nil))
			}
			gomock.InOrder(calls...)

			r := NewUserResolver(store, WithIdentityBinding(stampProvider, stampIssuer, stampClaim))
			got, err := r.ResolveUser(context.Background(), stampProvider, stampSubject)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantUser, got.ID)
		})
	}
}

func TestResolveLinkFailure_RollsBackWhenLinkIsGone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		linkErr    error
		wantDelete bool
	}{
		{name: "already exists proves the write did not land", linkErr: storage.ErrAlreadyExists, wantDelete: true},
		{name: "changed proves the write did not land", linkErr: storage.ErrProviderIdentityChanged, wantDelete: true},
		{name: "an ambiguous error keeps the candidate", linkErr: errors.New("i/o timeout")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := mocks.NewMockUserStorage(gomock.NewController(t))
			calls := []any{
				m.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(nil, storage.ErrNotFound),
				m.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(nil),
				m.EXPECT().CreateProviderIdentity(gomock.Any(), gomock.Any()).Return(tt.linkErr),
				m.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(nil, storage.ErrNotFound),
			}
			if tt.wantDelete {
				calls = append(calls, m.EXPECT().DeleteUser(gomock.Any(), gomock.Any()).Return(nil))
			}
			gomock.InOrder(calls...)

			got, err := NewUserResolver(m).ResolveUser(context.Background(), stampProvider, stampSubject)
			require.ErrorIs(t, err, tt.linkErr)
			assert.Nil(t, got)
		})
	}
}

func TestResolveExpectedUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// row is the stored link; nil means no link.
		row     *storage.ProviderIdentity
		wantErr error
	}{
		{name: "matching binding", row: stampedRow("owner", stampIssuer)},
		{name: "unstamped row with the right owner is stamped", row: unstampedRow("owner")},
		{name: "wrong owner", row: stampedRow("someone-else", stampIssuer), wantErr: ErrRememberedIdentityMismatch},
		{name: "wrong owner on an unstamped row", row: unstampedRow("someone-else"), wantErr: ErrRememberedIdentityMismatch},
		{name: "mismatching binding", row: stampedRow("owner", otherIssuer), wantErr: ErrRememberedIdentityMismatch},
		{name: "no link", wantErr: ErrRememberedIdentityMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { require.NoError(t, stor.Close()) })
			ctx := context.Background()
			now := time.Now()
			for _, id := range []string{"owner", "someone-else"} {
				require.NoError(t, stor.CreateUser(ctx, &storage.User{ID: id, CreatedAt: now, UpdatedAt: now}))
			}
			if tt.row != nil {
				row := *tt.row
				row.LinkedAt, row.LastUsedAt = now, now
				require.NoError(t, stor.CreateProviderIdentity(ctx, &row))
			}
			usersBefore := stor.Stats().Users

			r := NewUserResolver(stor, WithIdentityBinding(stampProvider, stampIssuer, stampClaim))
			got, err := r.ResolveExpectedUser(ctx, stampProvider, stampSubject, "owner")

			assert.Equal(t, usersBefore, stor.Stats().Users, "the lookup must never create a user")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				if tt.row != nil {
					after, getErr := stor.GetProviderIdentity(ctx, stampProvider, stampSubject)
					require.NoError(t, getErr)
					assert.Equal(t, tt.row.UserID, after.UserID, "a refusal must not replace the row")
					assert.Equal(t, tt.row.Issuer, after.Issuer, "a refusal must not restamp the row")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "owner", got.ID)
			after, err := stor.GetProviderIdentity(ctx, stampProvider, stampSubject)
			require.NoError(t, err)
			assert.Equal(t, stampIssuer, after.Issuer)
			assert.Equal(t, stampClaim, after.SubjectClaim)
		})
	}
}

func TestResolveExpectedUser_LostStampRereads(t *testing.T) {
	t.Parallel()
	changed := storage.ErrProviderIdentityChanged

	tests := []struct {
		name     string
		reread   *storage.ProviderIdentity
		wantUser string
		wantErr  error
	}{
		{name: "same binding and owner is accepted", reread: stampedRow("owner", stampIssuer), wantUser: "owner"},
		{name: "another binding is refused", reread: stampedRow("owner", otherIssuer), wantErr: ErrRememberedIdentityMismatch},
		{name: "another owner is refused", reread: stampedRow("someone-else", stampIssuer), wantErr: ErrRememberedIdentityMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, users, swapper := newSwappingStore(t)
			calls := []any{
				users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(unstampedRow("owner"), nil),
				users.EXPECT().GetUser(gomock.Any(), "owner").Return(&storage.User{ID: "owner"}, nil),
				swapper.EXPECT().CompareAndSwapProviderIdentity(gomock.Any(), gomock.Any(), gomock.Any()).Return(changed),
				users.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(tt.reread, nil),
			}
			if tt.wantUser != "" {
				calls = append(calls, users.EXPECT().GetUser(gomock.Any(), "owner").Return(&storage.User{ID: "owner"}, nil))
			}
			gomock.InOrder(calls...)

			r := NewUserResolver(store, WithIdentityBinding(stampProvider, stampIssuer, stampClaim))
			got, err := r.ResolveExpectedUser(context.Background(), stampProvider, stampSubject, "owner")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantUser, got.ID)
		})
	}
}

func TestResolveExpectedUser_OwnerLookupFailures(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("boom")

	tests := []struct {
		name       string
		getUserErr error
		wantIs     error
		wantNotIs  error
	}{
		{name: "owner no longer exists", getUserErr: storage.ErrNotFound, wantIs: ErrRememberedIdentityMismatch},
		{name: "storage failure is not a mismatch", getUserErr: storeErr, wantIs: storeErr, wantNotIs: ErrRememberedIdentityMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := mocks.NewMockUserStorage(gomock.NewController(t))
			m.EXPECT().GetProviderIdentity(gomock.Any(), stampProvider, stampSubject).Return(stampedRow("owner", stampIssuer), nil)
			m.EXPECT().GetUser(gomock.Any(), "owner").Return(nil, tt.getUserErr)

			r := NewUserResolver(m, WithIdentityBinding(stampProvider, stampIssuer, stampClaim))
			got, err := r.ResolveExpectedUser(context.Background(), stampProvider, stampSubject, "owner")
			require.ErrorIs(t, err, tt.wantIs)
			if tt.wantNotIs != nil {
				require.NotErrorIs(t, err, tt.wantNotIs)
			}
			assert.Nil(t, got)
		})
	}
}
