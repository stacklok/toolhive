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

func TestProcessSecretWithProvider(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		workloadName   string
		secretValue    string
		prefix         string
		target         string
		errorContext   string
		mockSetup      func(*mocks.MockProvider)
		expectedResult string
		expectError    bool
		errorContains  string
	}{
		{
			name:           "empty secret",
			workloadName:   "test-workload",
			secretValue:    "",
			prefix:         "OAUTH_CLIENT_SECRET_",
			target:         "oauth_secret",
			errorContext:   "OAuth client secret",
			mockSetup:      func(_ *mocks.MockProvider) {},
			expectedResult: "",
			expectError:    false,
		},
		{
			name:           "already in CLI format",
			workloadName:   "test-workload",
			secretValue:    "EXISTING_SECRET,target=oauth_secret",
			prefix:         "OAUTH_CLIENT_SECRET_",
			target:         "oauth_secret",
			errorContext:   "OAuth client secret",
			mockSetup:      func(_ *mocks.MockProvider) {},
			expectedResult: "EXISTING_SECRET,target=oauth_secret",
			expectError:    false,
		},
		{
			name:         "plain text secret - successful conversion (OAuth)",
			workloadName: "test-workload",
			secretValue:  "plain-text-secret",
			prefix:       "OAUTH_CLIENT_SECRET_",
			target:       "oauth_secret",
			errorContext: "OAuth client secret",
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
			prefix:       "BEARER_TOKEN_",
			target:       "bearer_token",
			errorContext: "bearer token",
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
			name:         "plain text secret - storage fails",
			workloadName: "test-workload",
			secretValue:  "plain-text-secret",
			prefix:       "OAUTH_CLIENT_SECRET_",
			target:       "oauth_secret",
			errorContext: "OAuth client secret",
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
			name:         "provider without write capability returns error",
			workloadName: "test-workload",
			secretValue:  "my-secret-token",
			prefix:       "BEARER_TOKEN_",
			target:       "bearer_token",
			errorContext: "bearer token",
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
			name:         "set secret error propagates",
			workloadName: "test-workload",
			secretValue:  "my-secret-token",
			prefix:       "BEARER_TOKEN_",
			target:       "bearer_token",
			errorContext: "bearer token",
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
			errorContains: "storage error",
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

			result, err := ProcessSecretWithProvider(tc.workloadName, tc.secretValue, mockProvider, tc.prefix, tc.target, tc.errorContext)

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
