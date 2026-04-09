// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestScopedProvider_GetSecret(t *testing.T) {
	t.Parallel()

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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

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

func TestScopedProvider_SetSecret(t *testing.T) {
	t.Parallel()

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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

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

func TestScopedProvider_DeleteSecret(t *testing.T) {
	t.Parallel()

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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

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

func TestScopedProvider_ListSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		innerList []secrets.SecretDescription
		innerErr  error
		wantKeys  []string
		wantErr   bool
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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

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

func TestScopedProvider_Cleanup(t *testing.T) {
	t.Parallel()

	t.Run("no-op when no keys in scope", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		inner := []secrets.SecretDescription{
			{Key: "__thv_workloads_key1"},
			{Key: "user-key"},
		}

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(inner, nil)

		p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
		err := p.Cleanup()
		require.NoError(t, err)
	})

	t.Run("propagates ListSecrets error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		listErr := errors.New("list failed")

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(nil, listErr)

		p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
		err := p.Cleanup()
		require.Error(t, err)
		assert.Equal(t, listErr, err)
	})

	t.Run("deletes only scoped keys", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		inner := []secrets.SecretDescription{
			{Key: "__thv_registry_key1"},
			{Key: "__thv_workloads_key2"},
			{Key: "user-key"},
		}

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(inner, nil)
		mock.EXPECT().DeleteSecrets(gomock.Any(), []string{"__thv_registry_key1"}).Return(nil)

		p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
		err := p.Cleanup()
		require.NoError(t, err)
	})

	t.Run("returns error from DeleteSecrets", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		inner := []secrets.SecretDescription{
			{Key: "__thv_registry_key1"},
			{Key: "__thv_registry_key2"},
		}

		bulkErr := errors.New("bulk delete failed")

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(inner, nil)
		mock.EXPECT().DeleteSecrets(gomock.Any(), []string{"__thv_registry_key1", "__thv_registry_key2"}).Return(bulkErr)

		p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
		err := p.Cleanup()
		require.Error(t, err)
		assert.Equal(t, bulkErr, err)
	})
}

func TestScopedProvider_Capabilities(t *testing.T) {
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

func TestUserProvider_GetSecret(t *testing.T) {
	t.Parallel()

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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

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

func TestUserProvider_SetSecret(t *testing.T) {
	t.Parallel()

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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

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

func TestUserProvider_DeleteSecret(t *testing.T) {
	t.Parallel()

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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

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

func TestUserProvider_ListSecrets(t *testing.T) {
	t.Parallel()

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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

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

func TestUserProvider_Cleanup(t *testing.T) {
	t.Parallel()

	t.Run("deletes only user keys, leaves system keys", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		inner := []secrets.SecretDescription{
			{Key: "__thv_registry_key1"},
			{Key: "__thv_workloads_key2"},
			{Key: "user-key1"},
			{Key: "user-key2"},
		}

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(inner, nil)
		mock.EXPECT().DeleteSecrets(gomock.Any(), []string{"user-key1", "user-key2"}).Return(nil)

		p := secrets.NewUserProvider(mock)
		err := p.Cleanup()
		require.NoError(t, err)
	})

	t.Run("no-op when no user keys exist", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		inner := []secrets.SecretDescription{
			{Key: "__thv_registry_key1"},
			{Key: "__thv_workloads_key2"},
		}

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(inner, nil)

		p := secrets.NewUserProvider(mock)
		err := p.Cleanup()
		require.NoError(t, err)
	})

	t.Run("propagates ListSecrets error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		listErr := errors.New("list failed")

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(nil, listErr)

		p := secrets.NewUserProvider(mock)
		err := p.Cleanup()
		require.Error(t, err)
		assert.Equal(t, listErr, err)
	})

	t.Run("propagates DeleteSecrets error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		inner := []secrets.SecretDescription{{Key: "user-key1"}}
		bulkErr := errors.New("bulk delete failed")

		mock := mocks.NewMockProvider(ctrl)
		mock.EXPECT().ListSecrets(gomock.Any()).Return(inner, nil)
		mock.EXPECT().DeleteSecrets(gomock.Any(), []string{"user-key1"}).Return(bulkErr)

		p := secrets.NewUserProvider(mock)
		err := p.Cleanup()
		require.Error(t, err)
		assert.Equal(t, bulkErr, err)
	})
}

func TestScopedProvider_DeleteSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		inputNames []string
		expectKeys []string
		innerErr   error
		wantErr    bool
	}{
		{
			name:       "prefixes bare names with scope key",
			inputNames: []string{"key1", "key2"},
			expectKeys: []string{"__thv_registry_key1", "__thv_registry_key2"},
		},
		{
			name:       "propagates error from inner",
			inputNames: []string{"key1"},
			expectKeys: []string{"__thv_registry_key1"},
			innerErr:   errors.New("backend error"),
			wantErr:    true,
		},
		{
			name:       "empty list delegates empty list",
			inputNames: []string{},
			expectKeys: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			mock.EXPECT().DeleteSecrets(ctx, tc.expectKeys).Return(tc.innerErr)

			p := secrets.NewScopedProvider(mock, secrets.ScopeRegistry)
			err := p.DeleteSecrets(ctx, tc.inputNames)

			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, tc.innerErr, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUserProvider_DeleteSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		inputNames  []string
		wantErr     bool
		wantReserve bool
		innerErr    error
		expectCall  bool
	}{
		{
			name:       "passes through user keys",
			inputNames: []string{"key1", "key2"},
			expectCall: true,
		},
		{
			name:        "rejects system-prefixed key",
			inputNames:  []string{"__thv_registry_mykey"},
			wantErr:     true,
			wantReserve: true,
		},
		{
			name:        "mixed input: aborts without deleting when any key is reserved",
			inputNames:  []string{"valid-key", "__thv_registry_reserved"},
			wantErr:     true,
			wantReserve: true,
			// expectCall is false: the inner provider must NOT be called at all
		},
		{
			name:       "propagates error from inner",
			inputNames: []string{"valid-key"},
			wantErr:    true,
			expectCall: true,
			innerErr:   errors.New("backend error"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			if tc.expectCall {
				mock.EXPECT().DeleteSecrets(ctx, tc.inputNames).Return(tc.innerErr)
			}

			p := secrets.NewUserProvider(mock)
			err := p.DeleteSecrets(ctx, tc.inputNames)

			if tc.wantErr {
				require.Error(t, err)
				if tc.wantReserve {
					assert.ErrorIs(t, err, secrets.ErrReservedKeyName)
				} else {
					assert.Equal(t, tc.innerErr, err)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SecretScope invariant tests
// ---------------------------------------------------------------------------

// TestSecretScopeInvariants verifies that every declared SecretScope constant
// satisfies the invariants documented on the SecretScope type:
//   - non-empty
//   - contains no underscores (underscore is the delimiter in "__thv_<scope>_<name>")
func TestSecretScopeInvariants(t *testing.T) {
	t.Parallel()

	scopes := []secrets.SecretScope{
		secrets.ScopeRegistry,
		secrets.ScopeWorkloads,
		secrets.ScopeAuth,
	}

	for _, scope := range scopes {
		s := string(scope)
		assert.NotEmpty(t, s, "scope %q must not be empty", s)
		assert.False(t, strings.Contains(s, "_"), "scope %q must not contain underscores", s)
	}
}

func TestUserProvider_Capabilities(t *testing.T) {
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

// ---------------------------------------------------------------------------
// ScopedProvider migration fallback tests
// ---------------------------------------------------------------------------

func TestScopedProvider_GetSecret_MigrationFallback(t *testing.T) {
	t.Parallel()
	notFoundErr := func(key string) error {
		return fmt.Errorf("secret not found: %s", key)
	}

	tests := []struct {
		name                string
		scopedErr           error
		scopedVal           string
		expectBareLookup    bool
		bareVal             string
		bareErr             error
		wantVal             string
		wantErr             bool
		wantBareErrSurfaced bool // true when the bare-key backend error should be returned
	}{
		{
			name:             "bare key found when scoped key missing",
			scopedErr:        notFoundErr("__thv_workloads_mykey"),
			expectBareLookup: true,
			bareVal:          "bare-value",
			bareErr:          nil,
			wantVal:          "bare-value",
		},
		{
			name:             "scoped key found, no fallback",
			scopedVal:        "scoped-value",
			scopedErr:        nil,
			expectBareLookup: false,
			wantVal:          "scoped-value",
		},
		{
			name:             "both keys missing returns original scoped error",
			scopedErr:        notFoundErr("__thv_workloads_mykey"),
			expectBareLookup: true,
			bareErr:          notFoundErr("mykey"),
			wantErr:          true,
		},
		{
			name:             "non-not-found error on scoped key skips bare lookup",
			scopedErr:        errors.New("backend connection failed"),
			expectBareLookup: false,
			wantErr:          true,
		},
		{
			// When the bare-key lookup returns a real backend error (not a
			// not-found), that error must be surfaced so the caller doesn't
			// misdiagnose a connection failure as "secret not found".
			name:                "bare key lookup hits backend error, error is surfaced",
			scopedErr:           notFoundErr("__thv_workloads_mykey"),
			expectBareLookup:    true,
			bareErr:             errors.New("backend connection failed"),
			wantErr:             true,
			wantBareErrSurfaced: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := mocks.NewMockProvider(ctrl)
			mock.EXPECT().GetSecret(ctx, "__thv_workloads_mykey").Return(tc.scopedVal, tc.scopedErr)
			if tc.expectBareLookup {
				mock.EXPECT().GetSecret(ctx, "mykey").Return(tc.bareVal, tc.bareErr)
			}

			p := secrets.NewScopedProvider(mock, secrets.ScopeWorkloads)
			got, err := p.GetSecret(ctx, "mykey")

			if tc.wantErr {
				require.Error(t, err)
				if tc.wantBareErrSurfaced {
					assert.ErrorIs(t, err, tc.bareErr)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantVal, got)
			}
		})
	}
}
