// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package subjectresolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

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
			name: "found",
			ref:  subject.Ref{Issuer: idpIssuer, Claim: "sub", Value: "abc"},
			lookup: func(m *mocks.MockUserStorage) {
				m.EXPECT().GetProviderIdentity(gomock.Any(), "okta", "abc").
					Return(&storage.ProviderIdentity{UserID: "user-1"}, nil)
				m.EXPECT().GetUser(gomock.Any(), "user-1").Return(&storage.User{ID: "user-1"}, nil)
			},
			want: "user-1",
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
