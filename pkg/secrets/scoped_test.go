// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// ---------------------------------------------------------------------------
// ScopedProvider tests
// ---------------------------------------------------------------------------

func TestScopedProvider_GetSecret(t *testing.T) { //nolint:paralleltest
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name        string
		innerKey    string
		innerValue  string
		innerErr    error
		wantValue   string
		wantErr     bool
		wantErrSame bool // true when we expect the exact inner error back
	}{
		{
			name:       "returns value with prefixed key",
			innerKey:   "__thv_registry_mykey",
			innerValue: "value",
			innerErr:   nil,
			wantValue:  "value",
			wantErr:    false,
		},
		{
			name:        "propagates error from inner",
			innerKey:    "__thv_registry_mykey",
			innerErr:    errors.New("backend error"),
			wantErr:     true,
			wantErrSame: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			mock.EXPECT().GetSecret(ctx, tc.innerKey).Return(tc.innerValue, tc.innerErr)

			p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
			got, err := p.GetSecret(ctx, "mykey")

			if tc.wantErr {
				require.Error(t, err)
				if tc.wantErrSame {
					assert.Equal(t, tc.innerErr, err)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantValue, got)
			}
		})
	}
}

func TestScopedProvider_SetSecret(t *testing.T) { //nolint:paralleltest
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name     string
		innerKey string
		innerErr error
		wantErr  bool
	}{
		{
			name:     "sets secret with prefixed key",
			innerKey: "__thv_registry_mykey",
			innerErr: nil,
			wantErr:  false,
		},
		{
			name:     "propagates error from inner",
			innerKey: "__thv_registry_mykey",
			innerErr: errors.New("write error"),
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			mock.EXPECT().SetSecret(ctx, tc.innerKey, "val").Return(tc.innerErr)

			p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
			err := p.SetSecret(ctx, "mykey", "val")

			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, tc.innerErr, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestScopedProvider_DeleteSecret(t *testing.T) { //nolint:paralleltest
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name     string
		innerKey string
		innerErr error
		wantErr  bool
	}{
		{
			name:     "deletes secret with prefixed key",
			innerKey: "__thv_registry_mykey",
			innerErr: nil,
			wantErr:  false,
		},
		{
			name:     "propagates error from inner",
			innerKey: "__thv_registry_mykey",
			innerErr: errors.New("delete error"),
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			mock.EXPECT().DeleteSecret(ctx, tc.innerKey).Return(tc.innerErr)

			p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
			err := p.DeleteSecret(ctx, "mykey")

			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, tc.innerErr, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestScopedProvider_ListSecrets(t *testing.T) { //nolint:paralleltest
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name       string
		innerList  []secrets.SecretDescription
		innerErr   error
		wantKeys   []string
		wantErr    bool
	}{
		{
			name: "returns only entries in scope with prefix stripped",
			innerList: []secrets.SecretDescription{
				{Key: "__thv_registry_key1", Description: "reg key"},
				{Key: "__thv_workloads_key2", Description: "workload key"},
				{Key: "user-key", Description: "user key"},
			},
			wantKeys: []string{"key1"},
			wantErr:  false,
		},
		{
			name: "returns empty slice when no entries in scope",
			innerList: []secrets.SecretDescription{
				{Key: "__thv_workloads_key2"},
				{Key: "user-key"},
			},
			wantKeys: nil,
			wantErr:  false,
		},
		{
			name:     "propagates inner error",
			innerErr: errors.New("list error"),
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			mock.EXPECT().ListSecrets(ctx).Return(tc.innerList, tc.innerErr)

			p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
			got, err := p.ListSecrets(ctx)

			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, tc.innerErr, err)
				return
			}

			require.NoError(t, err)

			if tc.wantKeys == nil {
				assert.Empty(t, got)
			} else {
				require.Len(t, got, len(tc.wantKeys))
				for i, wantKey := range tc.wantKeys {
					assert.Equal(t, wantKey, got[i].Key)
				}
			}
		})
	}
}

func TestScopedProvider_Cleanup(t *testing.T) { //nolint:paralleltest
	t.Parallel()

	t.Run("deletes only scoped keys", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		inner := []secrets.SecretDescription{
			{Key: "__thv_registry_key1"},
			{Key: "__thv_workloads_key2"},
			{Key: "user-key"},
		}

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(inner, nil)
		// Only the registry-scoped key should be bulk-deleted in a single call.
		mock.EXPECT().BulkDeleteSecrets(gomock.Any(), []string{"__thv_registry_key1"}).Return(nil)

		p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
		err := p.Cleanup()
		require.NoError(t, err)
	})

	t.Run("returns error from BulkDeleteSecrets", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		inner := []secrets.SecretDescription{
			{Key: "__thv_registry_key1"},
			{Key: "__thv_registry_key2"},
		}

		bulkErr := errors.New("bulk delete failed")

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(inner, nil)
		mock.EXPECT().BulkDeleteSecrets(gomock.Any(), []string{"__thv_registry_key1", "__thv_registry_key2"}).Return(bulkErr)

		p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
		err := p.Cleanup()
		require.Error(t, err)
		assert.Equal(t, bulkErr, err)
	})
}

func TestScopedProvider_Capabilities(t *testing.T) { //nolint:paralleltest
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	expected := secrets.ProviderCapabilities{
		CanRead:    true,
		CanWrite:   true,
		CanDelete:  true,
		CanList:    true,
		CanCleanup: true,
	}

	mock := mocks.NewMockProvider(ctrl)
	mock.EXPECT().Capabilities().Return(expected)

	p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
	assert.Equal(t, expected, p.Capabilities())
}

// ---------------------------------------------------------------------------
// UserProvider tests
// ---------------------------------------------------------------------------

func TestUserProvider_GetSecret(t *testing.T) { //nolint:paralleltest
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name        string
		key         string
		innerValue  string
		innerErr    error
		wantValue   string
		wantErr     bool
		wantReserve bool // true when we expect ErrReservedKeyName
	}{
		{
			name:       "passes through normal key",
			key:        "mykey",
			innerValue: "val",
			wantValue:  "val",
		},
		{
			name:        "rejects system-prefixed key",
			key:         "__thv_registry_mykey",
			wantErr:     true,
			wantReserve: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			if !tc.wantReserve {
				mock.EXPECT().GetSecret(ctx, tc.key).Return(tc.innerValue, tc.innerErr)
			}

			p := secrets.NewUserProvider(mock)
			got, err := p.GetSecret(ctx, tc.key)

			if tc.wantErr {
				require.Error(t, err)
				if tc.wantReserve {
					assert.ErrorIs(t, err, secrets.ErrReservedKeyName)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantValue, got)
			}
		})
	}
}

func TestUserProvider_SetSecret(t *testing.T) { //nolint:paralleltest
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name        string
		key         string
		innerErr    error
		wantErr     bool
		wantReserve bool
	}{
		{
			name:    "passes through normal key",
			key:     "mykey",
			wantErr: false,
		},
		{
			name:        "rejects system-prefixed key",
			key:         "__thv_registry_mykey",
			wantErr:     true,
			wantReserve: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			if !tc.wantReserve {
				mock.EXPECT().SetSecret(ctx, tc.key, "val").Return(tc.innerErr)
			}

			p := secrets.NewUserProvider(mock)
			err := p.SetSecret(ctx, tc.key, "val")

			if tc.wantErr {
				require.Error(t, err)
				if tc.wantReserve {
					assert.ErrorIs(t, err, secrets.ErrReservedKeyName)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUserProvider_DeleteSecret(t *testing.T) { //nolint:paralleltest
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name        string
		key         string
		innerErr    error
		wantErr     bool
		wantReserve bool
	}{
		{
			name:    "passes through normal key",
			key:     "mykey",
			wantErr: false,
		},
		{
			name:        "rejects system-prefixed key",
			key:         "__thv_registry_mykey",
			wantErr:     true,
			wantReserve: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			if !tc.wantReserve {
				mock.EXPECT().DeleteSecret(ctx, tc.key).Return(tc.innerErr)
			}

			p := secrets.NewUserProvider(mock)
			err := p.DeleteSecret(ctx, tc.key)

			if tc.wantErr {
				require.Error(t, err)
				if tc.wantReserve {
					assert.ErrorIs(t, err, secrets.ErrReservedKeyName)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUserProvider_ListSecrets(t *testing.T) { //nolint:paralleltest
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name      string
		innerList []secrets.SecretDescription
		innerErr  error
		wantKeys  []string
		wantErr   bool
	}{
		{
			name: "filters out system-prefixed entries",
			innerList: []secrets.SecretDescription{
				{Key: "__thv_registry_key1"},
				{Key: "__thv_workloads_key2"},
				{Key: "user-key1"},
				{Key: "user-key2"},
			},
			wantKeys: []string{"user-key1", "user-key2"},
		},
		{
			name: "returns empty slice when all entries are system keys",
			innerList: []secrets.SecretDescription{
				{Key: "__thv_registry_key1"},
				{Key: "__thv_workloads_key2"},
			},
			wantKeys: nil,
		},
		{
			name: "returns all entries when none are system keys",
			innerList: []secrets.SecretDescription{
				{Key: "user-key1"},
				{Key: "user-key2"},
			},
			wantKeys: []string{"user-key1", "user-key2"},
		},
		{
			name:     "propagates inner error",
			innerErr: errors.New("list error"),
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			mock.EXPECT().ListSecrets(ctx).Return(tc.innerList, tc.innerErr)

			p := secrets.NewUserProvider(mock)
			got, err := p.ListSecrets(ctx)

			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, tc.innerErr, err)
				return
			}

			require.NoError(t, err)

			if tc.wantKeys == nil {
				assert.Empty(t, got)
			} else {
				require.Len(t, got, len(tc.wantKeys))
				for i, wantKey := range tc.wantKeys {
					assert.Equal(t, wantKey, got[i].Key)
				}
			}
		})
	}
}

func TestUserProvider_Capabilities(t *testing.T) { //nolint:paralleltest
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	expected := secrets.ProviderCapabilities{
		CanRead:    true,
		CanWrite:   true,
		CanDelete:  true,
		CanList:    true,
		CanCleanup: true,
	}

	mock := mocks.NewMockProvider(ctrl)
	mock.EXPECT().Capabilities().Return(expected)

	p := secrets.NewUserProvider(mock)
	assert.Equal(t, expected, p.Capabilities())
}
