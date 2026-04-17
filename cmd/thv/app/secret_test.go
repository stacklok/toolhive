// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestFormatSystemSecretEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		key      string
		expected string
	}{
		{
			name:     "simple scope and name",
			key:      "__thv_auth_session",
			expected: "  - session  [auth]",
		},
		{
			name:     "name contains underscores, only first underscore splits scope",
			key:      "__thv_registry_REGISTRY_OAUTH_abc12345",
			expected: "  - REGISTRY_OAUTH_abc12345  [registry]",
		},
		{
			name:     "name contains underscore",
			key:      "__thv_workloads_token_abc",
			expected: "  - token_abc  [workloads]",
		},
		{
			name:     "name with multiple underscores",
			key:      "__thv_auth_session_access",
			expected: "  - session_access  [auth]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatSystemSecretEntry(tt.key)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestValidateSystemKeyName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		key         string
		wantErr     bool
		errContains []string
	}{
		{
			name:    "valid system key with scope and name",
			key:     "__thv_auth_session",
			wantErr: false,
		},
		{
			name:    "valid system key with underscores in name",
			key:     "__thv_registry_REGISTRY_OAUTH_abc",
			wantErr: false,
		},
		{
			name:        "plain user secret rejected",
			key:         "my-secret",
			wantErr:     true,
			errContains: []string{"--system", "__thv_"},
		},
		{
			name:        "missing double underscore prefix rejected",
			key:         "thv_auth_session",
			wantErr:     true,
			errContains: []string{"--system", "__thv_"},
		},
		{
			name:        "empty string rejected",
			key:         "",
			wantErr:     true,
			errContains: []string{"--system", "__thv_"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSystemKeyName(tt.key)
			if tt.wantErr {
				require.Error(t, err)
				for _, fragment := range tt.errContains {
					require.True(t, strings.Contains(err.Error(), fragment),
						"expected error message to contain %q, got: %s", fragment, err.Error())
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRunSystemSecretList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		storedKeys   []secrets.SecretDescription
		listErr      error
		wantErr      bool
		wantContains []string
		wantAbsent   []string
	}{
		{
			name: "system keys shown with scope labels",
			storedKeys: []secrets.SecretDescription{
				{Key: "__thv_auth_session"},
				{Key: "__thv_registry_REGISTRY_OAUTH_abc12345"},
			},
			wantContains: []string{
				"System-managed secrets:",
				"  - session  [auth]",
				"  - REGISTRY_OAUTH_abc12345  [registry]",
			},
		},
		{
			name: "non-system keys filtered out",
			storedKeys: []secrets.SecretDescription{
				{Key: "my-user-secret"},
				{Key: "__thv_auth_session"},
			},
			wantContains: []string{"  - session  [auth]"},
			wantAbsent:   []string{"my-user-secret"},
		},
		{
			name:         "no system keys prints empty message",
			storedKeys:   []secrets.SecretDescription{{Key: "user-secret"}},
			wantContains: []string{"No system-managed secrets found"},
			wantAbsent:   []string{"System-managed secrets:"},
		},
		{
			name:         "empty store prints empty message",
			storedKeys:   nil,
			wantContains: []string{"No system-managed secrets found"},
		},
		{
			name:    "provider list error is returned",
			listErr: errors.New("backend unavailable"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			provider := secretsmocks.NewMockProvider(ctrl)
			provider.EXPECT().ListSecrets(gomock.Any()).Return(tt.storedKeys, tt.listErr)

			var buf bytes.Buffer
			err := runSystemSecretList(context.Background(), provider, &buf)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			out := buf.String()
			for _, want := range tt.wantContains {
				require.Contains(t, out, want)
			}
			for _, absent := range tt.wantAbsent {
				require.NotContains(t, out, absent)
			}
		})
	}
}

func TestRunSystemSecretDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       string
		deleteErr error
		wantErr   bool
		wantCalls bool // whether DeleteSecret should be called
	}{
		{
			name:      "valid system key is deleted",
			key:       "__thv_auth_session",
			wantCalls: true,
		},
		{
			name:      "valid key with underscores in name is deleted",
			key:       "__thv_registry_REGISTRY_OAUTH_abc",
			wantCalls: true,
		},
		{
			name:    "non-system key rejected before provider call",
			key:     "my-user-secret",
			wantErr: true,
			// DeleteSecret must NOT be called when validation fails.
			wantCalls: false,
		},
		{
			name:    "key without double-underscore rejected",
			key:     "thv_auth_session",
			wantErr: true,
			// DeleteSecret must NOT be called when validation fails.
			wantCalls: false,
		},
		{
			name:      "provider delete error is propagated",
			key:       "__thv_auth_session",
			deleteErr: errors.New("keyring locked"),
			wantErr:   true,
			wantCalls: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			provider := secretsmocks.NewMockProvider(ctrl)
			if tt.wantCalls {
				provider.EXPECT().DeleteSecret(gomock.Any(), tt.key).Return(tt.deleteErr)
			}

			err := runSystemSecretDelete(context.Background(), provider, tt.key)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// newTestEncryptedProvider creates a real EncryptedManager backed by a temp
// file for integration-style tests. It does not touch the OS keyring.
func newTestEncryptedProvider(t *testing.T) secrets.Provider {
	t.Helper()

	key := sha256.Sum256([]byte("integration-test-password"))
	filePath := filepath.Join(t.TempDir(), "secrets_encrypted")

	provider, err := secrets.NewEncryptedManager(filePath, key[:])
	require.NoError(t, err)
	return provider
}

// TestRunSystemSecretListIntegration exercises runSystemSecretList against a
// real EncryptedManager instead of a mock, giving end-to-end coverage of the
// filtering and formatting path with actual encrypted storage.
//
//nolint:paralleltest // Uses real encrypted file; parallel is safe but serial keeps output readable
func TestRunSystemSecretListIntegration(t *testing.T) {
	ctx := context.Background()
	provider := newTestEncryptedProvider(t)

	// Seed a mix of system and user keys.
	require.NoError(t, provider.SetSecret(ctx, "__thv_auth_session", "enterprise_refresh_tok"))
	require.NoError(t, provider.SetSecret(ctx, "__thv_registry_REGISTRY_OAUTH_deadbeef", "registry_oauth_tok"))
	require.NoError(t, provider.SetSecret(ctx, "user-visible-secret", "should-not-appear"))

	var buf bytes.Buffer
	require.NoError(t, runSystemSecretList(ctx, provider, &buf))

	out := buf.String()
	require.Contains(t, out, "System-managed secrets:")
	require.Contains(t, out, "  - session  [auth]")
	require.Contains(t, out, "  - REGISTRY_OAUTH_deadbeef  [registry]")
	require.NotContains(t, out, "user-visible-secret")
}

// TestRunSystemSecretDeleteIntegration exercises the full delete path against a
// real EncryptedManager: seed a system key, delete it, confirm it's gone.
//
//nolint:paralleltest // Uses real encrypted file; serial keeps output readable
func TestRunSystemSecretDeleteIntegration(t *testing.T) {
	ctx := context.Background()
	provider := newTestEncryptedProvider(t)

	const key = "__thv_auth_session"
	require.NoError(t, provider.SetSecret(ctx, key, "enterprise_refresh_tok"))

	// Delete the key via the function under test.
	require.NoError(t, runSystemSecretDelete(ctx, provider, key))

	// Confirm the key is gone.
	_, err := provider.GetSecret(ctx, key)
	require.Error(t, err, "key should no longer exist after deletion")
}
