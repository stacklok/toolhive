// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestMigrateSystemKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		migrations  []secrets.KeyMigration
		setup       func(m *secretsmocks.MockProvider)
		wantErr     bool
		errContains string
	}{
		{
			name: "migrates all keys successfully",
			migrations: []secrets.KeyMigration{
				{OldKey: "BEARER_TOKEN_foo", NewKey: "__thv_workloads_BEARER_TOKEN_foo"},
				{OldKey: "REGISTRY_OAUTH_bar", NewKey: "__thv_registry_REGISTRY_OAUTH_bar"},
				{OldKey: "BUILD_AUTH_FILE_docker", NewKey: "__thv_workloads_BUILD_AUTH_FILE_docker"},
			},
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_foo").Return("", errors.New("secret not found"))
				m.EXPECT().GetSecret(gomock.Any(), "BEARER_TOKEN_foo").Return("token-val", nil)
				m.EXPECT().SetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_foo", "token-val").Return(nil)
				m.EXPECT().DeleteSecret(gomock.Any(), "BEARER_TOKEN_foo").Return(nil)

				m.EXPECT().GetSecret(gomock.Any(), "__thv_registry_REGISTRY_OAUTH_bar").Return("", errors.New("secret not found"))
				m.EXPECT().GetSecret(gomock.Any(), "REGISTRY_OAUTH_bar").Return("oauth-val", nil)
				m.EXPECT().SetSecret(gomock.Any(), "__thv_registry_REGISTRY_OAUTH_bar", "oauth-val").Return(nil)
				m.EXPECT().DeleteSecret(gomock.Any(), "REGISTRY_OAUTH_bar").Return(nil)

				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BUILD_AUTH_FILE_docker").Return("", errors.New("secret not found"))
				m.EXPECT().GetSecret(gomock.Any(), "BUILD_AUTH_FILE_docker").Return("auth-val", nil)
				m.EXPECT().SetSecret(gomock.Any(), "__thv_workloads_BUILD_AUTH_FILE_docker", "auth-val").Return(nil)
				m.EXPECT().DeleteSecret(gomock.Any(), "BUILD_AUTH_FILE_docker").Return(nil)
			},
		},
		{
			name: "idempotent: scoped key already exists — skips write, cleans up bare key",
			migrations: []secrets.KeyMigration{
				{OldKey: "BEARER_TOKEN_foo", NewKey: "__thv_workloads_BEARER_TOKEN_foo"},
			},
			setup: func(m *secretsmocks.MockProvider) {
				// Scoped key already exists — SetSecret must NOT be called.
				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_foo").Return("existing-val", nil)
				m.EXPECT().DeleteSecret(gomock.Any(), "BEARER_TOKEN_foo").Return(nil)
			},
		},
		{
			name: "idempotent: scoped key exists and bare key already gone",
			migrations: []secrets.KeyMigration{
				{OldKey: "BEARER_TOKEN_foo", NewKey: "__thv_workloads_BEARER_TOKEN_foo"},
			},
			setup: func(m *secretsmocks.MockProvider) {
				// Scoped key already exists — SetSecret must NOT be called.
				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_foo").Return("existing-val", nil)
				// Bare key is already gone — not-found on delete is ignored.
				m.EXPECT().DeleteSecret(gomock.Any(), "BEARER_TOKEN_foo").Return(errors.New("secret not found: BEARER_TOKEN_foo"))
			},
		},
		{
			name: "skips key that does not exist",
			migrations: []secrets.KeyMigration{
				{OldKey: "BEARER_TOKEN_missing", NewKey: "__thv_workloads_BEARER_TOKEN_missing"},
			},
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_missing").Return("", errors.New("secret not found"))
				m.EXPECT().GetSecret(gomock.Any(), "BEARER_TOKEN_missing").Return("", errors.New("secret not found: BEARER_TOKEN_missing"))
				// SetSecret and DeleteSecret must NOT be called.
			},
		},
		{
			name:       "empty migration list is a no-op",
			migrations: []secrets.KeyMigration{},
			setup:      func(_ *secretsmocks.MockProvider) {},
		},
		{
			name: "returns error when GetSecret fails with unexpected error",
			migrations: []secrets.KeyMigration{
				{OldKey: "BEARER_TOKEN_err", NewKey: "__thv_workloads_BEARER_TOKEN_err"},
			},
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_err").Return("", errors.New("secret not found"))
				m.EXPECT().GetSecret(gomock.Any(), "BEARER_TOKEN_err").Return("", errors.New("backend unavailable"))
			},
			wantErr:     true,
			errContains: "migration: reading",
		},
		{
			name: "returns error when SetSecret fails",
			migrations: []secrets.KeyMigration{
				{OldKey: "BEARER_TOKEN_setfail", NewKey: "__thv_workloads_BEARER_TOKEN_setfail"},
			},
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_setfail").Return("", errors.New("secret not found"))
				m.EXPECT().GetSecret(gomock.Any(), "BEARER_TOKEN_setfail").Return("val", nil)
				m.EXPECT().SetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_setfail", "val").Return(errors.New("write error"))
			},
			wantErr:     true,
			errContains: "migration: writing",
		},
		{
			name: "returns error when DeleteSecret fails",
			migrations: []secrets.KeyMigration{
				{OldKey: "BEARER_TOKEN_delfail", NewKey: "__thv_workloads_BEARER_TOKEN_delfail"},
			},
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_delfail").Return("", errors.New("secret not found"))
				m.EXPECT().GetSecret(gomock.Any(), "BEARER_TOKEN_delfail").Return("val", nil)
				m.EXPECT().SetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_delfail", "val").Return(nil)
				m.EXPECT().DeleteSecret(gomock.Any(), "BEARER_TOKEN_delfail").Return(errors.New("delete error"))
			},
			wantErr:     true,
			errContains: "migration: deleting",
		},
		{
			name: "idempotent when old key is already gone",
			migrations: []secrets.KeyMigration{
				{OldKey: "BEARER_TOKEN_gone", NewKey: "__thv_workloads_BEARER_TOKEN_gone"},
			},
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().GetSecret(gomock.Any(), "__thv_workloads_BEARER_TOKEN_gone").Return("", errors.New("secret not found"))
				// Old key already deleted in a previous migration run.
				m.EXPECT().GetSecret(gomock.Any(), "BEARER_TOKEN_gone").Return("", errors.New("secret not found: BEARER_TOKEN_gone"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)

			mock := secretsmocks.NewMockProvider(ctrl)
			tt.setup(mock)

			err := secrets.MigrateSystemKeys(context.Background(), mock, tt.migrations)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDiscoverMigrations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(m *secretsmocks.MockProvider)
		wantCount   int
		wantErr     bool
		errContains string
	}{
		{
			name: "discovers all system key prefixes",
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{
					{Key: "BEARER_TOKEN_foo"},
					{Key: "OAUTH_CLIENT_SECRET_bar"},
					{Key: "OAUTH_REFRESH_TOKEN_baz"},
					{Key: "registry-user-myrepo"},
					{Key: "registry-default-prod"},
					{Key: "BUILD_AUTH_FILE_docker"},
					{Key: "REGISTRY_OAUTH_ghcr"},
				}, nil)
			},
			wantCount: 7,
		},
		{
			name: "skips already-migrated keys",
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{
					{Key: "__thv_workloads_BEARER_TOKEN_foo"},
					{Key: "__thv_registry_REGISTRY_OAUTH_bar"},
					{Key: "user-secret"},
				}, nil)
			},
			wantCount: 0,
		},
		{
			name: "empty store returns no migrations",
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{}, nil)
			},
			wantCount: 0,
		},
		{
			name: "user keys are not included in migrations",
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{
					{Key: "my-api-key"},
					{Key: "github-token"},
					{Key: "BEARER_TOKEN_workload1"},
				}, nil)
			},
			wantCount: 1, // only the system key
		},
		{
			name: "returns error when ListSecrets fails",
			setup: func(m *secretsmocks.MockProvider) {
				m.EXPECT().ListSecrets(gomock.Any()).Return(nil, errors.New("backend unavailable"))
			},
			wantErr:     true,
			errContains: "migration discovery: listing secrets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)

			mock := secretsmocks.NewMockProvider(ctrl)
			tt.setup(mock)

			migs, err := secrets.DiscoverMigrations(context.Background(), mock)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				require.Len(t, migs, tt.wantCount)
			}
		})
	}
}
