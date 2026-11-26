// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package subjectresolver_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
	"github.com/stacklok/toolhive/pkg/authserver/subjectresolver"
)

const (
	idpIssuer   = "https://idp.example.com"
	entraIssuer = "https://entra.example.com"
	// oktaCustomIssuer is a custom authorization server whose tokens carry the
	// sign-in subject in uid and are mapped to the same provider as idpIssuer.
	oktaCustomIssuer = "https://okta.example.com/oauth2/default"
)

func TestResolve(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("boom")

	tests := []struct {
		name      string
		ref       subject.Ref
		lookup    func(*mocks.MockUserStorage)
		want      string
		wantErr   error
		wantCause error
	}{
		{
			name: "found with matching binding",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1", Issuer: idpIssuer, SubjectClaim: "sub"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-1").Return(&storage.User{ID: "user-1"}, nil)
			},
			want: "user-1",
		},
		{
			name: "found unstamped legacy row",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-1").Return(&storage.User{ID: "user-1"}, nil)
			},
			want: "user-1",
		},
		{
			name: "row stamped with different issuer returns unknown",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{
						UserID: "user-1", Issuer: "https://previous-idp.example.com", SubjectClaim: "sub",
					}, nil)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "row stamped with different subject claim returns unknown",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{
						UserID: "user-1", Issuer: idpIssuer, SubjectClaim: "oid",
					}, nil)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "half-stamped row with issuer only returns unknown",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1", Issuer: idpIssuer}, nil)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "half-stamped row with claim only returns unknown",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1", SubjectClaim: "sub"}, nil)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "second issuer queries its own provider",
			ref:  subject.Ref{Issuer: entraIssuer, Claim: "oid", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "entra", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-2"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-2").Return(&storage.User{ID: "user-2"}, nil)
			},
			want: "user-2",
		},
		{
			name: "second issuer for the same provider uses its own subject claim",
			ref:  subject.Ref{Issuer: oktaCustomIssuer, Claim: "uid", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-1").Return(&storage.User{ID: "user-1"}, nil)
			},
			want: "user-1",
		},
		{
			name:    "second issuer with the claim of another issuer",
			ref:     subject.Ref{Issuer: oktaCustomIssuer, Claim: "sub", Value: "abc"},
			wantErr: subject.ErrNotConfigured,
		},
		{
			name: "no row",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").Return(nil, storage.ErrNotFound)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "storage error",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").Return(nil, storeErr)
			},
			wantErr:   subject.ErrUnavailable,
			wantCause: storeErr,
		},
		{
			name:    "issuer without provider",
			ref:     subject.Ref{Issuer: "https://other.example.com", Claim: "sub", Value: "abc"},
			wantErr: subject.ErrNotConfigured,
		},
		{
			name:    "claim is not the subject claim",
			ref:     subject.Ref{Issuer: idpIssuer, Claim: "email", Value: "abc"},
			wantErr: subject.ErrNotConfigured,
		},
		{
			name: "nil row",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").Return(nil, nil)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "row with empty user ID",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{}, nil)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "link without user",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-1").Return(nil, storage.ErrNotFound)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "user lookup storage error",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-1").Return(nil, storeErr)
			},
			wantErr:   subject.ErrUnavailable,
			wantCause: storeErr,
		},
		{
			name: "nil user",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-1").Return(nil, nil)
			},
			wantErr: subject.ErrUnknown,
		},
		{
			name: "user with empty ID",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-1").Return(&storage.User{}, nil)
			},
			wantErr: subject.ErrUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := mocks.NewMockUserStorage(gomock.NewController(t))
			if tt.lookup != nil {
				tt.lookup(store)
			}
			r, err := subjectresolver.New(map[string]subjectresolver.Provider{
				idpIssuer:        {Name: "okta", SubjectClaim: "sub"},
				entraIssuer:      {Name: "entra", SubjectClaim: "oid"},
				oktaCustomIssuer: {Name: "okta", SubjectClaim: "uid"},
			}, store)
			require.NoError(t, err)

			got, err := r.Resolve(context.Background(), tt.ref)
			if err == nil {
				require.NotEmpty(t, got)
			}
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, got)
				if tt.wantCause != nil {
					assert.ErrorIs(t, err, tt.wantCause)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewRefusesInvalidInput(t *testing.T) {
	t.Parallel()
	store := mocks.NewMockUserStorage(gomock.NewController(t))
	valid := subjectresolver.Provider{Name: "okta", SubjectClaim: "sub"}

	tests := []struct {
		name      string
		providers map[string]subjectresolver.Provider
		store     subjectresolver.IdentityStore
		wantErr   error
	}{
		{"nil store", map[string]subjectresolver.Provider{idpIssuer: valid}, nil, nil},
		{"no providers", nil, store, nil},
		{"empty issuer", map[string]subjectresolver.Provider{"": valid}, store, nil},
		{"empty name", map[string]subjectresolver.Provider{idpIssuer: {SubjectClaim: "sub"}}, store, nil},
		{"empty subject claim", map[string]subjectresolver.Provider{idpIssuer: {Name: "okta"}}, store, nil},
		{
			"mutable email claim",
			map[string]subjectresolver.Provider{idpIssuer: {Name: "okta", SubjectClaim: "email"}},
			store, subject.ErrInvalidBindingClaim,
		},
		{
			"mutable preferred_username claim",
			map[string]subjectresolver.Provider{idpIssuer: {Name: "okta", SubjectClaim: "preferred_username"}},
			store, subject.ErrInvalidBindingClaim,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := subjectresolver.New(tt.providers, tt.store)
			require.Error(t, err)
			assert.Nil(t, r)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

//nolint:paralleltest // captures the process-global slog default
func TestResolve_UnstampedRowWarnsOnceAndDoesNotWrite(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, stor.Close()) })
	ctx := context.Background()

	now := time.Now()
	for _, sub := range []string{"sub-1", "sub-2"} {
		uid := "user-" + sub
		require.NoError(t, stor.CreateUser(ctx, &storage.User{ID: uid, CreatedAt: now, UpdatedAt: now}))
		require.NoError(t, stor.CreateProviderIdentity(ctx, &storage.ProviderIdentity{
			UserID: uid, ProviderID: "okta", ProviderSubject: sub, LinkedAt: now, LastUsedAt: now,
		}))
	}

	r, err := subjectresolver.New(map[string]subjectresolver.Provider{
		idpIssuer: {Name: "okta", SubjectClaim: "sub"},
	}, stor)
	require.NoError(t, err)

	u1, err := r.Resolve(ctx, subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "sub-1"})
	require.NoError(t, err)
	assert.Equal(t, "user-sub-1", u1)

	u2, err := r.Resolve(ctx, subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "sub-2"})
	require.NoError(t, err)
	assert.Equal(t, "user-sub-2", u2)

	// Resolver is read-only: rows remain unstamped.
	row, err := stor.GetProviderIdentity(ctx, "okta", "sub-1")
	require.NoError(t, err)
	assert.True(t, row.IsUnstamped())

	assert.Equal(t, 1, strings.Count(buf.String(), "unstamped provider identity"),
		"must warn once per resolver")
	assert.NotContains(t, buf.String(), "sub-1")
	assert.NotContains(t, buf.String(), "user-sub-1")
}

// TestResolveAndSignIn_BackendAcceptance verifies issue #7256 acceptance criteria
// end-to-end across both memory and Redis (miniredis) backends: after an
// upstream's issuer or subject claim changes, a row created under the old values
// matches neither resolution (subject_unknown) nor sign-in (new user created,
// old user untouched), while a row created under the current values works for both.
func TestResolveAndSignIn_BackendAcceptance(t *testing.T) {
	t.Parallel()

	backends := map[string]func(*testing.T) storage.UserStorage{
		"memory": func(t *testing.T) storage.UserStorage {
			t.Helper()
			s := storage.NewMemoryStorage()
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			return s
		},
		"redis": func(t *testing.T) storage.UserStorage {
			t.Helper()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			return storage.NewRedisStorageWithClient(client, "thv:auth:{ns:accept}:")
		},
	}

	changes := []struct {
		name      string
		oldIssuer string
		oldClaim  string
		newIssuer string
		newClaim  string
	}{
		{
			name:      "issuer changed",
			oldIssuer: "https://old-idp.example.com", oldClaim: "sub",
			newIssuer: "https://new-idp.example.com", newClaim: "sub",
		},
		{
			name:      "subject claim changed",
			oldIssuer: "https://idp.example.com", oldClaim: "sub",
			newIssuer: "https://idp.example.com", newClaim: "oid",
		},
	}

	for backendName, newBackend := range backends {
		for _, tc := range changes {
			t.Run(backendName+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				stor := newBackend(t)

				// 1. User signs in under old config.
				oldSignIn := handlers.NewUserResolver(stor,
					handlers.WithCreateOnlyFor("default"),
					handlers.WithIdentityBinding("default", tc.oldIssuer, tc.oldClaim),
				)
				oldUser, err := oldSignIn.ResolveUser(ctx, "default", "alice")
				require.NoError(t, err)

				// Resolution works under old config.
				oldResolve, err := subjectresolver.New(map[string]subjectresolver.Provider{
					tc.oldIssuer: {Name: "default", SubjectClaim: tc.oldClaim},
				}, stor)
				require.NoError(t, err)
				resolvedOld, err := oldResolve.Resolve(ctx, subject.Ref{Issuer: tc.oldIssuer, Claim: tc.oldClaim, Value: "alice"})
				require.NoError(t, err)
				assert.Equal(t, oldUser.ID, resolvedOld)

				// 2. Operator changes upstream issuer or subject claim.
				// Resolution under new config refuses the old row with subject.ErrUnknown and writes nothing.
				newResolve, err := subjectresolver.New(map[string]subjectresolver.Provider{
					tc.newIssuer: {Name: "default", SubjectClaim: tc.newClaim},
				}, stor)
				require.NoError(t, err)
				_, err = newResolve.Resolve(ctx, subject.Ref{Issuer: tc.newIssuer, Claim: tc.newClaim, Value: "alice"})
				require.ErrorIs(t, err, subject.ErrUnknown)

				// 3. Sign-in under new config creates a new user and leaves the old user untouched.
				newSignIn := handlers.NewUserResolver(stor,
					handlers.WithCreateOnlyFor("default"),
					handlers.WithIdentityBinding("default", tc.newIssuer, tc.newClaim),
				)
				newUser, err := newSignIn.ResolveUser(ctx, "default", "alice")
				require.NoError(t, err)
				assert.NotEqual(t, oldUser.ID, newUser.ID)

				keptOld, err := stor.GetUser(ctx, oldUser.ID)
				require.NoError(t, err)
				assert.Equal(t, oldUser.ID, keptOld.ID)

				// 4. Resolution under new config now resolves the new user, and old config refuses it.
				resolvedNew, err := newResolve.Resolve(ctx, subject.Ref{Issuer: tc.newIssuer, Claim: tc.newClaim, Value: "alice"})
				require.NoError(t, err)
				assert.Equal(t, newUser.ID, resolvedNew)

				_, err = oldResolve.Resolve(ctx, subject.Ref{Issuer: tc.oldIssuer, Claim: tc.oldClaim, Value: "alice"})
				require.ErrorIs(t, err, subject.ErrUnknown)
			})
		}
	}
}
