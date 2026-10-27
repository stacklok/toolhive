// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestConfig_SetFields_Scopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base []string
		opts []string
		want []string
	}{
		{"nil leaves existing scopes", []string{"openid", "custom"}, nil, []string{"openid", "custom"}},
		{"extras are added to the defaults", nil, []string{"toolhive:config:read"},
			[]string{"openid", "offline_access", "toolhive:config:read"}},
		{"duplicates of defaults and each other are dropped", nil,
			[]string{"openid", "a", "a", " "}, []string{"openid", "offline_access", "a"}},
		{"re-running replaces earlier extras", []string{"openid", "offline_access", "old"}, []string{"new"},
			[]string{"openid", "offline_access", "new"}},
		{"empty non-nil slice resets to the defaults", []string{"openid", "offline_access", "old"}, []string{},
			[]string{"openid", "offline_access"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{OIDC: OIDCConfig{Scopes: tt.base}}
			require.NoError(t, cfg.SetFields(SetOptions{Scopes: tt.opts}))
			assert.Equal(t, tt.want, cfg.OIDC.EffectiveScopes())
		})
	}
}

func TestDeleteCachedTokensOnScopeChange(t *testing.T) {
	t.Parallel()

	llmKey := secrets.SystemKeyPrefix + string(secrets.ScopeLLM) + "_refresh_token"
	caps := secrets.ProviderCapabilities{CanRead: true, CanWrite: true, CanDelete: true, CanList: true}

	tests := []struct {
		name        string
		base        []string
		opts        SetOptions
		wantDeleted bool
		wantErr     bool
		providerErr error
	}{
		{name: "scopes not provided", base: nil, opts: SetOptions{}},
		{name: "same extras re-applied", base: []string{"openid", "offline_access", "a"},
			opts: SetOptions{Scopes: []string{"a"}}},
		{name: "extras equal to the defaults on a default config", base: nil,
			opts: SetOptions{Scopes: []string{"openid"}}},
		{name: "new extra clears the cache", base: nil,
			opts: SetOptions{Scopes: []string{"toolhive:config:read"}}, wantDeleted: true},
		{name: "reset to defaults clears the cache", base: []string{"openid", "offline_access", "a"},
			opts: SetOptions{Scopes: []string{}}, wantDeleted: true},
		{name: "secrets provider failure is returned", base: nil,
			opts: SetOptions{Scopes: []string{"a"}}, wantErr: true, providerErr: errors.New("keyring locked")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			mock := secretsmocks.NewMockProvider(ctrl)
			mock.EXPECT().Capabilities().Return(caps).AnyTimes()
			if tt.wantDeleted {
				mock.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{{Key: llmKey}}, nil)
				mock.EXPECT().DeleteSecrets(gomock.Any(), []string{llmKey}).Return(nil)
			}
			getProvider := func() (secrets.Provider, error) {
				if tt.providerErr != nil {
					return nil, tt.providerErr
				}
				return mock, nil
			}

			var errOut bytes.Buffer
			cfg := Config{OIDC: OIDCConfig{Scopes: tt.base}}
			err := DeleteCachedTokensOnScopeChange(context.Background(), &errOut, cfg, tt.opts, getProvider)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantDeleted {
				assert.Contains(t, errOut.String(), "Scopes changed")
			} else {
				assert.Empty(t, errOut.String())
			}
		})
	}
}
