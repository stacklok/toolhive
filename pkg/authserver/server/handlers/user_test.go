// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func TestUserResolver_CreateUserWithIdentity_LinkFailure(t *testing.T) {
	t.Parallel()

	const (
		providerID      = "github"
		providerSubject = "github-user-456"
		winnerID        = "winner-user-id"
	)
	alreadyExists := fmt.Errorf("%w: provider identity already linked", storage.ErrAlreadyExists)
	timeout := errors.New("i/o timeout")

	tests := []struct {
		name string
		// linkErr is what CreateProviderIdentity returns.
		linkErr error
		// owner is who the link points at on re-read: "self", "winner" or "none"
		// (no link exists).
		owner       string
		linkReadErr error
		userReadErr error
		// wantDelete is whether our own candidate must be rolled back.
		wantDelete bool
		deleteErr  error
		// wantUser is "self" or "winner" when the call must succeed.
		wantUser string
		wantErr  string
	}{
		{
			name:    "already exists returns the winner after rolling back own user",
			linkErr: alreadyExists, owner: "winner", wantDelete: true, wantUser: "winner",
		},
		{
			name:    "already exists still returns the winner when the rollback fails",
			linkErr: alreadyExists, owner: "winner", wantDelete: true, deleteErr: errors.New("delete failed"),
			wantUser: "winner",
		},
		{
			// Ownership is unknown, so the candidate must not be deleted.
			name:    "already exists but link read fails returns an error without deleting",
			linkErr: alreadyExists, owner: "winner", linkReadErr: errors.New("read failed"),
			wantErr: "re-reading the link also failed",
		},
		{
			name:    "already exists but winner user read fails returns an error",
			linkErr: alreadyExists, owner: "winner", wantDelete: true, userReadErr: storage.ErrNotFound,
			wantErr: "re-read winner",
		},
		{
			// The write committed but its reply was lost, so a retry saw
			// "already exists". The candidate owns the link and must survive.
			name:    "already exists with the link pointing at our own user keeps it",
			linkErr: alreadyExists, owner: "self", wantUser: "self",
		},
		{
			// No link does not prove the write failed: a timed-out write can
			// still commit after the read, so the candidate is kept. An
			// unlinked row is harmless: nothing resolves to it.
			name:    "other link error with no link returns the link error without deleting",
			linkErr: timeout, owner: "none", wantErr: "failed to link provider identity",
		},
		{
			// The script committed but the reply timed out, so the error is not
			// "already exists". The candidate owns the link and must survive.
			name:    "other link error with the link pointing at our own user keeps it",
			linkErr: timeout, owner: "self", wantUser: "self",
		},
		{
			name:    "other link error with the link owned by another user adopts it",
			linkErr: timeout, owner: "winner", wantDelete: true, wantUser: "winner",
		},
		{
			// Ownership is unknown, so the candidate must not be deleted.
			name:    "other link error and link read fails returns an error without deleting",
			linkErr: timeout, owner: "winner", linkReadErr: errors.New("read failed"),
			wantErr: "re-reading the link also failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			m := mocks.NewMockUserStorage(ctrl)

			var ownID string
			ownerID := func() string {
				if tt.owner == "self" {
					return ownID
				}
				return winnerID
			}

			linkRead := m.EXPECT().GetProviderIdentity(gomock.Any(), providerID, providerSubject)
			switch {
			case tt.linkReadErr != nil:
				linkRead.Return(nil, tt.linkReadErr)
			case tt.owner == "none":
				linkRead.Return(nil, storage.ErrNotFound)
			default:
				linkRead.DoAndReturn(func(context.Context, string, string) (*storage.ProviderIdentity, error) {
					return &storage.ProviderIdentity{UserID: ownerID(), ProviderID: providerID, ProviderSubject: providerSubject}, nil
				})
			}

			calls := []any{
				m.EXPECT().GetProviderIdentity(gomock.Any(), providerID, providerSubject).Return(nil, storage.ErrNotFound),
				m.EXPECT().CreateUser(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, u *storage.User) error { ownID = u.ID; return nil }),
				m.EXPECT().CreateProviderIdentity(gomock.Any(), gomock.Any()).Return(tt.linkErr),
				linkRead,
			}
			if tt.wantDelete {
				calls = append(calls, m.EXPECT().DeleteUser(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, id string) error {
						require.Equal(t, ownID, id, "rollback must delete the user this call created")
						return tt.deleteErr
					}))
			}
			if tt.owner == "winner" && tt.linkReadErr == nil {
				getUser := m.EXPECT().GetUser(gomock.Any(), winnerID)
				if tt.userReadErr != nil {
					getUser.Return(nil, tt.userReadErr)
				} else {
					getUser.Return(&storage.User{ID: winnerID}, nil)
				}
				calls = append(calls, getUser)
			}
			gomock.InOrder(calls...)

			user, err := NewUserResolver(m).ResolveUser(context.Background(), providerID, providerSubject)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.Nil(t, user)
				return
			}
			require.NoError(t, err)
			if tt.wantUser == "self" {
				require.Equal(t, ownID, user.ID)
			} else {
				require.Equal(t, winnerID, user.ID)
			}
		})
	}
}

func TestUserResolver_WithCreateOnlyFor(t *testing.T) {
	t.Parallel()

	const subject = "subject-1"

	tests := []struct {
		name     string
		provider string
		// linked is whether an identity link already exists for (provider, subject).
		linked bool
		// wantErr is the sentinel the call must return; nil means success.
		wantErr   error
		wantUsers int
	}{
		{name: "first provider with no link creates the user", provider: "first", wantUsers: 1},
		{name: "first provider with a link resolves it", provider: "first", linked: true, wantUsers: 1},
		{name: "later provider with a link resolves it", provider: "later", linked: true, wantUsers: 1},
		{name: "later provider with no link is refused", provider: "later", wantErr: ErrUpstreamCannotCreateUsers},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { require.NoError(t, stor.Close()) })
			ctx := context.Background()
			if tt.linked {
				// Seed a user and link through an unrestricted resolver.
				_, err := NewUserResolver(stor).ResolveUser(ctx, tt.provider, subject)
				require.NoError(t, err)
			}

			user, err := NewUserResolver(stor, WithCreateOnlyFor("first")).ResolveUser(ctx, tt.provider, subject)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Nil(t, user)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, user.ID)
			}
			require.Equal(t, tt.wantUsers, stor.Stats().Users, "a refused call must create no user")
			require.Equal(t, tt.wantUsers, stor.Stats().ProviderIdentities, "a refused call must create no link")
		})
	}
}

// nonSwappingUserStorage wraps a UserStorage without implementing
// storage.ProviderIdentitySwapper or Unwrap, mirroring an external enterprise
// storage decorator (such as SEP's directory-bound store).
type nonSwappingUserStorage struct {
	storage.UserStorage
}

// unwrappingExternalStorage wraps a full storage.Storage and exposes Unwrap(),
// but does not implement ProviderIdentitySwapper itself. ProviderIdentitySwapperFor
// must NOT peel past external decorators.
type unwrappingExternalStorage struct {
	storage.Storage
}

func (s unwrappingExternalStorage) Unwrap() storage.Storage { return s.Storage }

func TestUserResolver_IdentityBinding(t *testing.T) {
	t.Parallel()

	const (
		provider  = "default"
		subject   = "sub-1"
		oldIssuer = "https://old-idp.example.com"
		newIssuer = "https://new-idp.example.com"
		oldClaim  = "sub"
		newClaim  = "oid"
	)

	t.Run("creates stamped row and matches same binding", func(t *testing.T) {
		t.Parallel()
		stor := storage.NewMemoryStorage()
		t.Cleanup(func() { require.NoError(t, stor.Close()) })
		ctx := context.Background()

		r := NewUserResolver(stor, WithCreateOnlyFor(provider), WithIdentityBinding(provider, oldIssuer, oldClaim))
		u1, err := r.ResolveUser(ctx, provider, subject)
		require.NoError(t, err)

		row, err := stor.GetProviderIdentity(ctx, provider, subject)
		require.NoError(t, err)
		assert.Equal(t, oldIssuer, row.Issuer)
		assert.Equal(t, oldClaim, row.SubjectClaim)

		u2, err := r.ResolveUser(ctx, provider, subject)
		require.NoError(t, err)
		assert.Equal(t, u1.ID, u2.ID)
	})

	t.Run("issuer or claim change creates new user and leaves old user untouched", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name    string
			nextIss string
			nextClm string
		}{
			{name: "issuer changed", nextIss: newIssuer, nextClm: oldClaim},
			{name: "subject claim changed", nextIss: oldIssuer, nextClm: newClaim},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				stor := storage.NewMemoryStorage()
				t.Cleanup(func() { require.NoError(t, stor.Close()) })
				ctx := context.Background()

				oldRes := NewUserResolver(stor, WithCreateOnlyFor(provider), WithIdentityBinding(provider, oldIssuer, oldClaim))
				oldUser, err := oldRes.ResolveUser(ctx, provider, subject)
				require.NoError(t, err)

				newRes := NewUserResolver(stor, WithCreateOnlyFor(provider), WithIdentityBinding(provider, tc.nextIss, tc.nextClm))
				newUser, err := newRes.ResolveUser(ctx, provider, subject)
				require.NoError(t, err)
				assert.NotEqual(t, oldUser.ID, newUser.ID)

				// Old user still exists untouched.
				keptOld, err := stor.GetUser(ctx, oldUser.ID)
				require.NoError(t, err)
				assert.Equal(t, oldUser.ID, keptOld.ID)

				// Row now points at new user with the new binding.
				row, err := stor.GetProviderIdentity(ctx, provider, subject)
				require.NoError(t, err)
				assert.Equal(t, newUser.ID, row.UserID)
				assert.Equal(t, tc.nextIss, row.Issuer)
				assert.Equal(t, tc.nextClm, row.SubjectClaim)

				// Subsequent sign-in resolves the new user.
				again, err := newRes.ResolveUser(ctx, provider, subject)
				require.NoError(t, err)
				assert.Equal(t, newUser.ID, again.ID)
			})
		}
	})

	t.Run("mismatch on later upstream refuses without replacing row", func(t *testing.T) {
		t.Parallel()
		stor := storage.NewMemoryStorage()
		t.Cleanup(func() { require.NoError(t, stor.Close()) })
		ctx := context.Background()

		// Seed a row on a later upstream under old binding.
		oldUser, err := NewUserResolver(stor, WithIdentityBinding("later", oldIssuer, oldClaim)).ResolveUser(ctx, "later", subject)
		require.NoError(t, err)

		r := NewUserResolver(stor,
			WithCreateOnlyFor("first"),
			WithIdentityBinding("later", newIssuer, oldClaim),
		)
		user, err := r.ResolveUser(ctx, "later", subject)
		require.ErrorIs(t, err, ErrUpstreamCannotCreateUsers)
		require.Nil(t, user)

		// Existing row and user must be untouched.
		row, err := stor.GetProviderIdentity(ctx, "later", subject)
		require.NoError(t, err)
		assert.Equal(t, oldUser.ID, row.UserID)
		assert.Equal(t, oldIssuer, row.Issuer)
		assert.Equal(t, 1, stor.Stats().Users)
	})

	t.Run("decorated storage unwraps to ProviderIdentitySwapper", func(t *testing.T) {
		t.Parallel()
		base := storage.NewMemoryStorage()
		t.Cleanup(func() { require.NoError(t, base.Close()) })
		decorated, err := storage.NewUserKeyedUpstreamStorage(base)
		require.NoError(t, err)
		ctx := context.Background()

		oldUser, err := NewUserResolver(decorated, WithIdentityBinding(provider, oldIssuer, oldClaim)).ResolveUser(ctx, provider, subject)
		require.NoError(t, err)

		newUser, err := NewUserResolver(decorated, WithIdentityBinding(provider, newIssuer, oldClaim)).ResolveUser(ctx, provider, subject)
		require.NoError(t, err)
		assert.NotEqual(t, oldUser.ID, newUser.ID)
	})

	t.Run("external store without ProviderIdentitySwapper", func(t *testing.T) {
		t.Parallel()
		t.Run("unstamped row matches without writing", func(t *testing.T) {
			t.Parallel()
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { require.NoError(t, base.Close()) })
			ctx := context.Background()

			// Seed an unstamped row.
			u, err := NewUserResolver(base).ResolveUser(ctx, provider, subject)
			require.NoError(t, err)

			wrapped := nonSwappingUserStorage{UserStorage: base}
			r := NewUserResolver(wrapped, WithIdentityBinding(provider, oldIssuer, oldClaim))
			got, err := r.ResolveUser(ctx, provider, subject)
			require.NoError(t, err)
			assert.Equal(t, u.ID, got.ID)

			// Row remains unstamped because wrapped does not expose ProviderIdentitySwapper.
			row, err := base.GetProviderIdentity(ctx, provider, subject)
			require.NoError(t, err)
			assert.True(t, row.IsUnstamped())
		})

		t.Run("stamped mismatch fails closed without creating orphan user", func(t *testing.T) {
			t.Parallel()
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { require.NoError(t, base.Close()) })
			ctx := context.Background()

			_, err := NewUserResolver(base, WithIdentityBinding(provider, oldIssuer, oldClaim)).ResolveUser(ctx, provider, subject)
			require.NoError(t, err)

			wrapped := nonSwappingUserStorage{UserStorage: base}
			r := NewUserResolver(wrapped, WithIdentityBinding(provider, newIssuer, oldClaim))
			got, err := r.ResolveUser(ctx, provider, subject)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Equal(t, 1, base.Stats().Users, "must not create an orphan user when swap is unsupported")
		})
		t.Run("external decorator exposing Unwrap is not bypassed", func(t *testing.T) {
			t.Parallel()
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { require.NoError(t, base.Close()) })
			ctx := context.Background()

			u, err := NewUserResolver(base).ResolveUser(ctx, provider, subject)
			require.NoError(t, err)

			ext := unwrappingExternalStorage{Storage: base}
			r := NewUserResolver(ext, WithIdentityBinding(provider, oldIssuer, oldClaim))
			got, err := r.ResolveUser(ctx, provider, subject)
			require.NoError(t, err)
			assert.Equal(t, u.ID, got.ID)

			// Row stays unstamped because ProviderIdentitySwapperFor does not peel ext.
			row, err := base.GetProviderIdentity(ctx, provider, subject)
			require.NoError(t, err)
			assert.True(t, row.IsUnstamped())
		})
	})

	t.Run("resolveLinkFailure never adopts a mismatching row", func(t *testing.T) {
		t.Parallel()
		t.Run("rolls back candidate on definite non-commit", func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			m := mocks.NewMockUserStorage(ctrl)
			ctx := context.Background()

			var createdID string
			gomock.InOrder(
				m.EXPECT().GetProviderIdentity(gomock.Any(), provider, subject).Return(nil, storage.ErrNotFound),
				m.EXPECT().CreateUser(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, u *storage.User) error {
					createdID = u.ID
					return nil
				}),
				m.EXPECT().CreateProviderIdentity(gomock.Any(), gomock.Any()).Return(storage.ErrAlreadyExists),
				m.EXPECT().GetProviderIdentity(gomock.Any(), provider, subject).Return(&storage.ProviderIdentity{
					UserID: "other-idp-user", ProviderID: provider, ProviderSubject: subject,
					Issuer: oldIssuer, SubjectClaim: oldClaim,
				}, nil),
				m.EXPECT().DeleteUser(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, id string) error {
					require.Equal(t, createdID, id)
					return nil
				}),
			)

			r := NewUserResolver(m, WithIdentityBinding(provider, newIssuer, oldClaim))
			user, err := r.ResolveUser(ctx, provider, subject)
			require.Error(t, err)
			assert.Nil(t, user)
		})

		t.Run("does not roll back candidate on ambiguous transport error", func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			m := mocks.NewMockUserStorage(ctrl)
			ctx := context.Background()

			gomock.InOrder(
				m.EXPECT().GetProviderIdentity(gomock.Any(), provider, subject).Return(nil, storage.ErrNotFound),
				m.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(nil),
				m.EXPECT().CreateProviderIdentity(gomock.Any(), gomock.Any()).Return(errors.New("i/o timeout")),
				m.EXPECT().GetProviderIdentity(gomock.Any(), provider, subject).Return(&storage.ProviderIdentity{
					UserID: "other-idp-user", ProviderID: provider, ProviderSubject: subject,
					Issuer: oldIssuer, SubjectClaim: oldClaim,
				}, nil),
				// No DeleteUser expectation: ambiguous error must leave candidate intact.
			)

			r := NewUserResolver(m, WithIdentityBinding(provider, newIssuer, oldClaim))
			user, err := r.ResolveUser(ctx, provider, subject)
			require.Error(t, err)
			assert.Nil(t, user)
		})
	})

	t.Run("validateNamedUpstreams rejects half-populated binding", func(t *testing.T) {
		t.Parallel()
		mockProv := &mockIDPProvider{}
		require.Error(t, validateNamedUpstreams([]NamedUpstream{{Name: "a", Provider: mockProv, Issuer: oldIssuer}}))
		require.Error(t, validateNamedUpstreams([]NamedUpstream{{Name: "a", Provider: mockProv, SubjectClaim: oldClaim}}))
		require.NoError(t, validateNamedUpstreams([]NamedUpstream{{Name: "a", Provider: mockProv, Issuer: oldIssuer, SubjectClaim: oldClaim}}))
	})
}

//nolint:paralleltest // captures the process-global slog default
func TestUserResolver_UnstampedRowStampsAndWarnsOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, stor.Close()) })
	ctx := context.Background()

	// Seed two unstamped rows.
	u1, err := NewUserResolver(stor).ResolveUser(ctx, "default", "sub-1")
	require.NoError(t, err)
	u2, err := NewUserResolver(stor).ResolveUser(ctx, "default", "sub-2")
	require.NoError(t, err)

	r := NewUserResolver(stor, WithIdentityBinding("default", "https://idp.example.com", "sub"))
	got1, err := r.ResolveUser(ctx, "default", "sub-1")
	require.NoError(t, err)
	assert.Equal(t, u1.ID, got1.ID)

	got2, err := r.ResolveUser(ctx, "default", "sub-2")
	require.NoError(t, err)
	assert.Equal(t, u2.ID, got2.ID)

	for _, sub := range []string{"sub-1", "sub-2"} {
		row, getErr := stor.GetProviderIdentity(ctx, "default", sub)
		require.NoError(t, getErr)
		assert.Equal(t, "https://idp.example.com", row.Issuer)
		assert.Equal(t, "sub", row.SubjectClaim)
	}

	assert.Equal(t, 1, strings.Count(buf.String(), "stamped legacy provider identity"),
		"must log once per resolver when stamping unstamped rows")
	assert.NotContains(t, buf.String(), "sub-1", "warning must not log provider subject")
	assert.NotContains(t, buf.String(), u1.ID, "warning must not log user ID")
}

func TestWithIdentityBinding_PanicsOnEmptyArg(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { WithIdentityBinding("", "https://idp.example.com", "sub") })
	assert.Panics(t, func() { WithIdentityBinding("default", "", "sub") })
	assert.Panics(t, func() { WithIdentityBinding("default", "https://idp.example.com", "") })
}
