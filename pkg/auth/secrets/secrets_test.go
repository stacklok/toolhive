package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

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

// TestProcessSecret tests the public ProcessSecret function for each token type
// These tests verify that ProcessSecret works correctly without requiring secrets setup
func TestProcessSecret(t *testing.T) {
	t.Parallel()

	t.Run("OAuth client secret - empty returns early", func(t *testing.T) {
		t.Parallel()
		// This test verifies that when secretValue is empty, ProcessSecret
		// returns early without attempting to access the secrets manager.
		// If it tried to access the secrets manager, it would fail because
		// no secrets provider is configured in the test environment.
		result, err := ProcessSecret("test-workload", "", TokenTypeOAuthClientSecret)
		assert.NoError(t, err, "Should not error when secret is empty")
		assert.Equal(t, "", result, "Should return empty string when input is empty")
	})

	t.Run("Bearer token - empty returns early", func(t *testing.T) {
		t.Parallel()
		// This test verifies that when secretValue is empty, ProcessSecret
		// returns early without attempting to access the secrets manager.
		result, err := ProcessSecret("test-workload", "", TokenTypeBearerToken)
		assert.NoError(t, err, "Should not error when token is empty")
		assert.Equal(t, "", result, "Should return empty string when input is empty")
	})

	t.Run("unknown token type returns error", func(t *testing.T) {
		t.Parallel()
		result, err := ProcessSecret("test-workload", "some-secret", TokenType("unknown"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown token type")
		assert.Equal(t, "", result)
	})
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
