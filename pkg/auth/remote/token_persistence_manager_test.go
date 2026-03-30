// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestNewTokenPersistenceManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		provider    func(ctrl *gomock.Controller) *mocks.MockProvider
		wantErr     bool
		wantManager bool
	}{
		{
			name:        "nil provider returns error and nil manager",
			provider:    nil,
			wantErr:     true,
			wantManager: false,
		},
		{
			name: "non-nil provider returns non-nil manager and nil error",
			provider: func(ctrl *gomock.Controller) *mocks.MockProvider {
				return mocks.NewMockProvider(ctrl)
			},
			wantErr:     false,
			wantManager: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)

			var manager *TokenPersistenceManager
			var err error
			if tt.provider == nil {
				manager, err = NewTokenPersistenceManager(nil)
			} else {
				manager, err = NewTokenPersistenceManager(tt.provider(ctrl))
			}

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, manager)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, manager)
			}
		})
	}
}

func TestTokenPersistenceManager_RestoreFromCache(t *testing.T) {
	t.Parallel()

	minimalOAuth2Config := &oauth2.Config{
		ClientID: "test-client",
		Endpoint: oauth2.Endpoint{
			TokenURL: "https://example.com/token",
		},
	}

	tests := []struct {
		name           string
		tokenKey       string
		resource       string
		setupMock      func(provider *mocks.MockProvider)
		wantErr        bool
		wantErrContain string
		wantSource     bool
	}{
		{
			name:     "GetSecret returns error",
			tokenKey: "my-token-key",
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "my-token-key").
					Return("", errors.New("storage unavailable"))
			},
			wantErr:        true,
			wantErrContain: "failed to retrieve cached refresh token",
		},
		{
			name:     "GetSecret returns empty string",
			tokenKey: "my-token-key",
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "my-token-key").
					Return("", nil)
			},
			wantErr:        true,
			wantErrContain: "no cached refresh token found",
		},
		{
			name:     "valid refresh token with no resource returns non-nil TokenSource",
			tokenKey: "my-token-key",
			resource: "",
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "my-token-key").
					Return("valid-refresh-token", nil)
			},
			wantErr:    false,
			wantSource: true,
		},
		{
			name:     "valid refresh token with non-empty resource returns non-nil TokenSource",
			tokenKey: "my-token-key",
			resource: "https://api.example.com/resource",
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "my-token-key").
					Return("valid-refresh-token", nil)
			},
			wantErr:    false,
			wantSource: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := mocks.NewMockProvider(ctrl)
			tt.setupMock(mockProvider)

			manager, err := NewTokenPersistenceManager(mockProvider)
			require.NoError(t, err)
			require.NotNil(t, manager)

			source, err := manager.RestoreFromCache(
				context.Background(),
				tt.tokenKey,
				minimalOAuth2Config,
				time.Now().Add(time.Hour),
				tt.resource,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContain != "" {
					assert.Contains(t, err.Error(), tt.wantErrContain)
				}
				assert.Nil(t, source)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, source)
			}
		})
	}
}

func TestTokenPersistenceManager_FetchRefreshToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupMock      func(provider *mocks.MockProvider)
		wantErr        bool
		wantErrContain string
		wantToken      string
	}{
		{
			name: "GetSecret returns an error",
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "my-token-key").
					Return("", errors.New("storage unavailable"))
			},
			wantErr:        true,
			wantErrContain: "failed to retrieve cached refresh token",
		},
		{
			name: "GetSecret returns empty string",
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "my-token-key").
					Return("", nil)
			},
			wantErr:        true,
			wantErrContain: "no cached refresh token found",
		},
		{
			name: "GetSecret returns a non-empty token",
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "my-token-key").
					Return("valid-refresh-token", nil)
			},
			wantErr:   false,
			wantToken: "valid-refresh-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockProvider := mocks.NewMockProvider(ctrl)
			tt.setupMock(mockProvider)

			manager, err := NewTokenPersistenceManager(mockProvider)
			require.NoError(t, err)
			require.NotNil(t, manager)

			token, err := manager.FetchRefreshToken(context.Background(), "my-token-key")

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContain != "" {
					assert.Contains(t, err.Error(), tt.wantErrContain)
				}
				assert.Empty(t, token)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantToken, token)
			}
		})
	}
}
