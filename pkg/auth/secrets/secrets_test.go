// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	envmocks "github.com/stacklok/toolhive-core/env/mocks"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// TestGetSystemSecretsProvider_EnvOverrideBypassesSetup verifies the regression
// fix: TOOLHIVE_SECRETS_PROVIDER=environment must succeed even when
// SetupCompleted is false (no config file), so Kubernetes / test deployments
// don't have to run interactive setup.
func TestGetSystemSecretsProvider_EnvOverrideBypassesSetup(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// PathProvider pointing at a non-existent file → SetupCompleted defaults to false.
	cfgProvider := config.NewPathProvider(t.TempDir() + "/config.yaml")

	// Mock env reader returns "environment" for the provider env var.
	mockEnv := envmocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return(string(secrets.EnvironmentType))

	provider, err := getSystemSecretsProviderFromConfig(cfgProvider, mockEnv)
	assert.NoError(t, err, "environment provider should succeed without interactive setup")
	assert.NotNil(t, provider, "should return a non-nil provider")
}

// TestGetSystemSecretsProvider_NoSetupNoEnvVar verifies that without both
// SetupCompleted and a TOOLHIVE_SECRETS_PROVIDER override, the function
// returns ErrSecretsNotSetup.
func TestGetSystemSecretsProvider_NoSetupNoEnvVar(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// PathProvider pointing at a non-existent file → SetupCompleted defaults to false.
	cfgProvider := config.NewPathProvider(t.TempDir() + "/config.yaml")

	// Mock env reader returns empty string → no override present.
	mockEnv := envmocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return("")

	_, err := getSystemSecretsProviderFromConfig(cfgProvider, mockEnv)
	assert.ErrorIs(t, err, secrets.ErrSecretsNotSetup,
		"should return ErrSecretsNotSetup when setup is incomplete and no env override is present")
}

func TestGenerateUniqueSecretNameWithPrefix(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		workloadName string
		prefix       string
		mockSetup    func(*mocks.MockProvider)
		expected     string
		expectError  bool
	}{
		{
			name:         "custom prefix generates correct name",
			workloadName: "test-workload",
			prefix:       "BEARER_TOKEN_",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "BEARER_TOKEN_test-workload").
					Return("", errors.New("secret not found"))
			},
			expected:    "BEARER_TOKEN_test-workload",
			expectError: false,
		},
		{
			name:         "custom prefix with conflict generates unique name",
			workloadName: "test-workload",
			prefix:       "CUSTOM_PREFIX_",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "CUSTOM_PREFIX_test-workload").
					Return("existing-secret", nil)
			},
			expectError: false,
			// Expected will contain the prefix and timestamp/random suffix
		},
		{
			name:         "OAuth prefix generates correct name",
			workloadName: "test-workload",
			prefix:       "OAUTH_CLIENT_SECRET_",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("", errors.New("secret not found"))
			},
			expected:    "OAUTH_CLIENT_SECRET_test-workload",
			expectError: false,
		},
		{
			name:         "OAuth prefix with conflict generates unique name",
			workloadName: "test-workload",
			prefix:       "OAUTH_CLIENT_SECRET_",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("existing-secret", nil)
			},
			expectError: false,
		},
		{
			name:         "empty workload name",
			workloadName: "",
			prefix:       "OAUTH_CLIENT_SECRET_",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_").
					Return("", errors.New("secret not found"))
			},
			expected:    "OAUTH_CLIENT_SECRET_",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockProvider := mocks.NewMockProvider(ctrl)
			tc.mockSetup(mockProvider)

			result, err := GenerateUniqueSecretNameWithPrefix(tc.workloadName, tc.prefix, mockProvider)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tc.expected != "" {
					assert.Equal(t, tc.expected, result)
				} else {
					// For conflict case, just verify it contains the prefix
					assert.Contains(t, result, tc.prefix)
					assert.Contains(t, result, tc.workloadName)
				}
			}
		})
	}
}

func TestStoreSecretInManagerWithProvider(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		secretName    string
		secretValue   string
		mockSetup     func(*mocks.MockProvider)
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful storage",
			secretName:  "test-secret",
			secretValue: "test-value",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "test-secret", "test-value").
					Return(nil)
			},
			expectError: false,
		},
		{
			name:        "provider does not support writing",
			secretName:  "test-secret",
			secretValue: "test-value",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: false})
			},
			expectError:   true,
			errorContains: "does not support writing secrets",
		},
		{
			name:        "storage fails",
			secretName:  "test-secret",
			secretValue: "test-value",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "test-secret", "test-value").
					Return(errors.New("storage failed"))
			},
			expectError:   true,
			errorContains: "failed to store secret",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockProvider := mocks.NewMockProvider(ctrl)
			tc.mockSetup(mockProvider)

			err := StoreSecretInManagerWithProvider(context.Background(), tc.secretName, tc.secretValue, mockProvider)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestProcessSecret verifies early returns that do not require secrets setup.
func TestProcessSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		secretValue string
		tokenType   TokenType
		want        string
		wantErr     string
	}{
		{
			name:      "empty OAuth client secret",
			tokenType: TokenTypeOAuthClientSecret,
		},
		{
			name:      "empty bearer token",
			tokenType: TokenTypeBearerToken,
		},
		{
			name:        "existing OAuth client secret reference",
			secretValue: "EXISTING_SECRET,target=oauth_secret",
			tokenType:   TokenTypeOAuthClientSecret,
			want:        "EXISTING_SECRET,target=oauth_secret",
		},
		{
			name:        "existing bearer token reference",
			secretValue: "EXISTING_TOKEN,target=bearer_token",
			tokenType:   TokenTypeBearerToken,
			want:        "EXISTING_TOKEN,target=bearer_token",
		},
		{
			name:        "unknown token type rejects an existing reference",
			secretValue: "EXISTING_TOKEN,target=bearer_token",
			tokenType:   TokenType("unknown"),
			wantErr:     "unknown token type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := ProcessSecret("test-workload", tt.secretValue, tt.tokenType)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				assert.Empty(t, result)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestProcessSecretWithProvider(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		workloadName   string
		secretValue    string
		tokenType      TokenType
		mockSetup      func(*mocks.MockProvider)
		expectedResult string
		expectError    bool
		errorContains  string
	}{
		{
			name:           "empty secret (OAuth)",
			workloadName:   "test-workload",
			secretValue:    "",
			tokenType:      TokenTypeOAuthClientSecret,
			mockSetup:      func(_ *mocks.MockProvider) {},
			expectedResult: "",
			expectError:    false,
		},
		{
			name:           "already in CLI format (OAuth)",
			workloadName:   "test-workload",
			secretValue:    "EXISTING_SECRET,target=oauth_secret",
			tokenType:      TokenTypeOAuthClientSecret,
			mockSetup:      func(_ *mocks.MockProvider) {},
			expectedResult: "EXISTING_SECRET,target=oauth_secret",
			expectError:    false,
		},
		{
			name:         "plain text secret - successful conversion (OAuth)",
			workloadName: "test-workload",
			secretValue:  "plain-text-secret",
			tokenType:    TokenTypeOAuthClientSecret,
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("", errors.New("secret not found"))
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload", "plain-text-secret").
					Return(nil)
			},
			expectedResult: "OAUTH_CLIENT_SECRET_test-workload,target=oauth_secret",
			expectError:    false,
		},
		{
			name:         "plain text secret - successful conversion (Bearer)",
			workloadName: "test-workload",
			secretValue:  "my-secret-token",
			tokenType:    TokenTypeBearerToken,
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "BEARER_TOKEN_test-workload").
					Return("", errors.New("secret not found"))
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "BEARER_TOKEN_test-workload", "my-secret-token").
					Return(nil)
			},
			expectedResult: "BEARER_TOKEN_test-workload,target=bearer_token",
			expectError:    false,
		},
		{
			name:         "plain text secret - storage fails (OAuth)",
			workloadName: "test-workload",
			secretValue:  "plain-text-secret",
			tokenType:    TokenTypeOAuthClientSecret,
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("", errors.New("secret not found"))
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload", "plain-text-secret").
					Return(errors.New("storage failed"))
			},
			expectError:   true,
			errorContains: "failed to store OAuth client secret in manager",
		},
		{
			name:         "provider without write capability returns error (Bearer)",
			workloadName: "test-workload",
			secretValue:  "my-secret-token",
			tokenType:    TokenTypeBearerToken,
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "BEARER_TOKEN_test-workload").
					Return("", errors.New("secret not found"))
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: false})
			},
			expectError:   true,
			errorContains: "does not support writing secrets",
		},
		{
			name:         "set secret error propagates (Bearer)",
			workloadName: "test-workload",
			secretValue:  "my-secret-token",
			tokenType:    TokenTypeBearerToken,
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "BEARER_TOKEN_test-workload").
					Return("", errors.New("secret not found"))
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "BEARER_TOKEN_test-workload", "my-secret-token").
					Return(errors.New("storage error"))
			},
			expectError:   true,
			errorContains: "failed to store bearer token in manager",
		},
		{
			name:          "unknown token type returns error",
			workloadName:  "test-workload",
			secretValue:   "my-secret-token",
			tokenType:     TokenType("unknown"),
			mockSetup:     func(_ *mocks.MockProvider) {},
			expectError:   true,
			errorContains: "unknown token type",
		},
		{
			name:           "already in CLI format (Bearer)",
			workloadName:   "test-workload",
			secretValue:    "EXISTING_SECRET,target=bearer_token",
			tokenType:      TokenTypeBearerToken,
			mockSetup:      func(_ *mocks.MockProvider) {},
			expectedResult: "EXISTING_SECRET,target=bearer_token",
			expectError:    false,
		},
		{
			name:         "plain text secret - storage fails (Bearer)",
			workloadName: "test-workload",
			secretValue:  "plain-text-token",
			tokenType:    TokenTypeBearerToken,
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "BEARER_TOKEN_test-workload").
					Return("", errors.New("secret not found"))
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "BEARER_TOKEN_test-workload", "plain-text-token").
					Return(errors.New("storage failed"))
			},
			expectError:   true,
			errorContains: "failed to store bearer token in manager",
		},
		{
			name:         "provider without write capability returns error (OAuth)",
			workloadName: "test-workload",
			secretValue:  "my-client-secret",
			tokenType:    TokenTypeOAuthClientSecret,
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("", errors.New("secret not found"))
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: false})
			},
			expectError:   true,
			errorContains: "does not support writing secrets",
		},
		{
			name:         "set secret error propagates (OAuth)",
			workloadName: "test-workload",
			secretValue:  "my-client-secret",
			tokenType:    TokenTypeOAuthClientSecret,
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("", errors.New("secret not found"))
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload", "my-client-secret").
					Return(errors.New("storage error"))
			},
			expectError:   true,
			errorContains: "failed to store OAuth client secret in manager",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockProvider := mocks.NewMockProvider(ctrl)
			tc.mockSetup(mockProvider)

			result, err := ProcessSecretWithProvider(tc.workloadName, tc.secretValue, mockProvider, tc.tokenType)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedResult, result)
			}
		})
	}
}
