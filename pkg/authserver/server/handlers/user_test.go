// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

func TestUserResolver_ResolveUser(t *testing.T) {
	t.Parallel()

	testUserID := "test-user-id-123"
	testProviderID := "github"
	testProviderSubject := "github-user-456"

	tests := []struct {
		name            string
		providerID      string
		providerSubject string
		setupMock       func(*mocks.MockUserStorage)
		wantErr         bool
		wantErrContains string
		validateResult  func(*testing.T, *storage.User)
	}{
		{
			name:            "empty provider ID returns error",
			providerID:      "",
			providerSubject: testProviderSubject,
			setupMock:       func(_ *mocks.MockUserStorage) {},
			wantErr:         true,
			wantErrContains: "provider ID cannot be empty",
		},
		{
			name:            "empty provider subject returns error",
			providerID:      testProviderID,
			providerSubject: "",
			setupMock:       func(_ *mocks.MockUserStorage) {},
			wantErr:         true,
			wantErrContains: "provider subject cannot be empty",
		},
		{
			name:            "existing identity found returns linked user",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				existingIdentity := &storage.ProviderIdentity{
					UserID:          testUserID,
					ProviderID:      testProviderID,
					ProviderSubject: testProviderSubject,
					LinkedAt:        time.Now(),
					LastUsedAt:      time.Now(),
				}
				existingUser := &storage.User{
					ID:        testUserID,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}
				m.EXPECT().
					GetProviderIdentity(gomock.Any(), testProviderID, testProviderSubject).
					Return(existingIdentity, nil)
				m.EXPECT().
					GetUser(gomock.Any(), testUserID).
					Return(existingUser, nil)
			},
			wantErr: false,
			validateResult: func(t *testing.T, user *storage.User) {
				t.Helper()
				require.Equal(t, testUserID, user.ID)
			},
		},
		{
			name:            "identity exists but user not found returns error",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				existingIdentity := &storage.ProviderIdentity{
					UserID:          testUserID,
					ProviderID:      testProviderID,
					ProviderSubject: testProviderSubject,
					LinkedAt:        time.Now(),
					LastUsedAt:      time.Now(),
				}
				m.EXPECT().
					GetProviderIdentity(gomock.Any(), testProviderID, testProviderSubject).
					Return(existingIdentity, nil)
				m.EXPECT().
					GetUser(gomock.Any(), testUserID).
					Return(nil, storage.ErrNotFound)
			},
			wantErr:         true,
			wantErrContains: "identity exists but user not found",
		},
		{
			name:            "GetProviderIdentity returns unexpected error",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				m.EXPECT().
					GetProviderIdentity(gomock.Any(), testProviderID, testProviderSubject).
					Return(nil, errors.New("database connection failed"))
			},
			wantErr:         true,
			wantErrContains: "failed to lookup provider identity",
		},
		{
			name:            "new user creation success",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				// No existing identity found
				m.EXPECT().
					GetProviderIdentity(gomock.Any(), testProviderID, testProviderSubject).
					Return(nil, storage.ErrNotFound)
				// Create new user succeeds
				m.EXPECT().
					CreateUser(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, user *storage.User) error {
						require.NotEmpty(t, user.ID)
						require.False(t, user.CreatedAt.IsZero())
						require.False(t, user.UpdatedAt.IsZero())
						return nil
					})
				// Create provider identity succeeds
				m.EXPECT().
					CreateProviderIdentity(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, identity *storage.ProviderIdentity) error {
						require.NotEmpty(t, identity.UserID)
						require.Equal(t, testProviderID, identity.ProviderID)
						require.Equal(t, testProviderSubject, identity.ProviderSubject)
						require.False(t, identity.LinkedAt.IsZero())
						require.False(t, identity.LastUsedAt.IsZero())
						return nil
					})
			},
			wantErr: false,
			validateResult: func(t *testing.T, user *storage.User) {
				t.Helper()
				require.NotEmpty(t, user.ID)
				require.False(t, user.CreatedAt.IsZero())
				require.False(t, user.UpdatedAt.IsZero())
			},
		},
		{
			name:            "new user creation fails returns error",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				m.EXPECT().
					GetProviderIdentity(gomock.Any(), testProviderID, testProviderSubject).
					Return(nil, storage.ErrNotFound)
				m.EXPECT().
					CreateUser(gomock.Any(), gomock.Any()).
					Return(errors.New("user creation failed"))
			},
			wantErr:         true,
			wantErrContains: "failed to create user",
		},
		{
			name:            "new user creation with rollback on identity link failure",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				var createdUserID string

				m.EXPECT().
					GetProviderIdentity(gomock.Any(), testProviderID, testProviderSubject).
					Return(nil, storage.ErrNotFound)
				m.EXPECT().
					CreateUser(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, user *storage.User) error {
						createdUserID = user.ID
						return nil
					})
				m.EXPECT().
					CreateProviderIdentity(gomock.Any(), gomock.Any()).
					Return(errors.New("identity link failed"))
				// Rollback should be attempted
				m.EXPECT().
					DeleteUser(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, userID string) error {
						require.Equal(t, createdUserID, userID)
						return nil
					})
			},
			wantErr:         true,
			wantErrContains: "failed to link provider identity",
		},
		{
			name:            "new user creation with rollback failure logs warning but still returns original error",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				m.EXPECT().
					GetProviderIdentity(gomock.Any(), testProviderID, testProviderSubject).
					Return(nil, storage.ErrNotFound)
				m.EXPECT().
					CreateUser(gomock.Any(), gomock.Any()).
					Return(nil)
				m.EXPECT().
					CreateProviderIdentity(gomock.Any(), gomock.Any()).
					Return(errors.New("identity link failed"))
				// Rollback fails but error should still be the original identity link error
				m.EXPECT().
					DeleteUser(gomock.Any(), gomock.Any()).
					Return(errors.New("rollback also failed"))
			},
			wantErr:         true,
			wantErrContains: "failed to link provider identity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockStorage := mocks.NewMockUserStorage(ctrl)
			tt.setupMock(mockStorage)

			resolver := NewUserResolver(mockStorage)
			ctx := context.Background()

			user, err := resolver.ResolveUser(ctx, tt.providerID, tt.providerSubject)

			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrContains)
				require.Nil(t, user)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, user)
			if tt.validateResult != nil {
				tt.validateResult(t, user)
			}
		})
	}
}

func TestUserResolver_UpdateLastAuthenticated(t *testing.T) {
	t.Parallel()

	testProviderID := "github"
	testProviderSubject := "github-user-456"

	tests := []struct {
		name            string
		providerID      string
		providerSubject string
		setupMock       func(*mocks.MockUserStorage)
	}{
		{
			name:            "success case updates timestamp",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				m.EXPECT().
					UpdateProviderIdentityLastUsed(gomock.Any(), testProviderID, testProviderSubject, gomock.Any()).
					DoAndReturn(func(_ context.Context, _, _ string, lastUsed time.Time) error {
						// Verify timestamp is recent (within last second)
						require.WithinDuration(t, time.Now(), lastUsed, time.Second)
						return nil
					})
			},
		},
		{
			name:            "error case logs warning but does not fail",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				m.EXPECT().
					UpdateProviderIdentityLastUsed(gomock.Any(), testProviderID, testProviderSubject, gomock.Any()).
					Return(errors.New("database error"))
			},
		},
		{
			name:            "not found error is handled gracefully",
			providerID:      testProviderID,
			providerSubject: testProviderSubject,
			setupMock: func(m *mocks.MockUserStorage) {
				m.EXPECT().
					UpdateProviderIdentityLastUsed(gomock.Any(), testProviderID, testProviderSubject, gomock.Any()).
					Return(storage.ErrNotFound)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockStorage := mocks.NewMockUserStorage(ctrl)
			tt.setupMock(mockStorage)

			resolver := NewUserResolver(mockStorage)
			ctx := context.Background()

			// This method should not panic or return an error regardless of storage behavior
			// It only logs warnings for failures
			resolver.UpdateLastAuthenticated(ctx, tt.providerID, tt.providerSubject)
		})
	}
}

func TestNewUserResolver(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockStorage := mocks.NewMockUserStorage(ctrl)

	resolver := NewUserResolver(mockStorage)

	require.NotNil(t, resolver)
	require.NotNil(t, resolver.storage)
}
