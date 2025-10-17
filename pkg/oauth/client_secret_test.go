package oauth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func TestGenerateOAuthClientSecretName(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		workloadName string
		expected     string
	}{
		{
			name:         "normal workload name",
			workloadName: "my-workload",
			expected:     "OAUTH_CLIENT_SECRET_my-workload",
		},
		{
			name:         "empty workload name",
			workloadName: "",
			expected:     "OAUTH_CLIENT_SECRET_",
		},
		{
			name:         "workload name with special characters",
			workloadName: "my_workload-123",
			expected:     "OAUTH_CLIENT_SECRET_my_workload-123",
		},
		{
			name:         "workload name with underscores",
			workloadName: "another_workload",
			expected:     "OAUTH_CLIENT_SECRET_another_workload",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := generateOAuthClientSecretName(tc.workloadName)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestSecretParameterToCLIString(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		param    secrets.SecretParameter
		expected string
	}{
		{
			name:     "normal secret parameter",
			param:    secrets.SecretParameter{Name: "GITHUB_TOKEN", Target: "GITHUB_PERSONAL_ACCESS_TOKEN"},
			expected: "GITHUB_TOKEN,target=GITHUB_PERSONAL_ACCESS_TOKEN",
		},
		{
			name:     "secret parameter with different target",
			param:    secrets.SecretParameter{Name: "MY_SECRET", Target: "CUSTOM_TARGET"},
			expected: "MY_SECRET,target=CUSTOM_TARGET",
		},
		{
			name:     "secret parameter with special characters",
			param:    secrets.SecretParameter{Name: "MY-SECRET_123", Target: "CUSTOM-TARGET_456"},
			expected: "MY-SECRET_123,target=CUSTOM-TARGET_456",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := tc.param.ToCLIString()
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestParseSecretParameter(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		input          string
		expectError    bool
		errorContains  string
		expectedResult secrets.SecretParameter
	}{
		{
			name:           "valid CLI format",
			input:          "GITHUB_TOKEN,target=GITHUB_PERSONAL_ACCESS_TOKEN",
			expectError:    false,
			expectedResult: secrets.SecretParameter{Name: "GITHUB_TOKEN", Target: "GITHUB_PERSONAL_ACCESS_TOKEN"},
		},
		{
			name:           "valid CLI format with different target",
			input:          "MY_SECRET,target=CUSTOM_TARGET",
			expectError:    false,
			expectedResult: secrets.SecretParameter{Name: "MY_SECRET", Target: "CUSTOM_TARGET"},
		},
		{
			name:          "empty parameter",
			input:         "",
			expectError:   true,
			errorContains: "secret parameter cannot be empty",
		},
		{
			name:          "invalid format - no target",
			input:         "GITHUB_TOKEN",
			expectError:   true,
			errorContains: "invalid secret parameter format",
		},
		{
			name:          "invalid format - no comma",
			input:         "GITHUB_TOKENtarget=GITHUB_PERSONAL_ACCESS_TOKEN",
			expectError:   true,
			errorContains: "invalid secret parameter format",
		},
		{
			name:          "invalid format - no equals",
			input:         "GITHUB_TOKEN,targetGITHUB_PERSONAL_ACCESS_TOKEN",
			expectError:   true,
			errorContains: "invalid secret parameter format",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := secrets.ParseSecretParameter(tc.input)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
				assert.Equal(t, secrets.SecretParameter{}, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedResult, result)
			}
		})
	}
}

// TestGenerateUniqueSecretNameWithProvider tests the testable version with dependency injection
func TestGenerateUniqueSecretNameWithProvider(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		workloadName   string
		mockSetup      func(*mocks.MockProvider)
		expectedResult string
		expectError    bool
		errorContains  string
	}{
		{
			name:         "base name available",
			workloadName: "test-workload",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("", errors.New("secret not found"))
			},
			expectedResult: "OAUTH_CLIENT_SECRET_test-workload",
			expectError:    false,
		},
		{
			name:         "base name conflicts, generates unique name",
			workloadName: "test-workload",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("existing-secret", nil)
			},
			expectedResult: "", // Will have timestamp, so we'll check prefix
			expectError:    false,
		},
		{
			name:         "empty workload name",
			workloadName: "",
			mockSetup: func(mock *mocks.MockProvider) {
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_").
					Return("", errors.New("secret not found"))
			},
			expectedResult: "OAUTH_CLIENT_SECRET_",
			expectError:    false,
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

			result, err := GenerateUniqueSecretNameWithProvider(tc.workloadName, mockProvider)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
				if tc.expectedResult != "" {
					assert.Equal(t, tc.expectedResult, result)
				} else {
					// For timestamp-based results, just check the prefix
					assert.Contains(t, result, "OAUTH_CLIENT_SECRET_")
				}
			}
		})
	}
}

// TestStoreSecretInManagerWithProvider tests the testable version with dependency injection
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

// TestProcessOAuthClientSecretWithProvider tests the testable version with dependency injection
func TestProcessOAuthClientSecretWithProvider(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		workloadName   string
		clientSecret   string
		mockSetup      func(*mocks.MockProvider)
		expectedResult string
		expectError    bool
		errorContains  string
	}{
		{
			name:           "empty client secret",
			workloadName:   "test-workload",
			clientSecret:   "",
			mockSetup:      func(_ *mocks.MockProvider) {},
			expectedResult: "",
			expectError:    false,
		},
		{
			name:           "already in CLI format",
			workloadName:   "test-workload",
			clientSecret:   "EXISTING_SECRET,target=oauth_secret",
			mockSetup:      func(_ *mocks.MockProvider) {},
			expectedResult: "EXISTING_SECRET,target=oauth_secret",
			expectError:    false,
		},
		{
			name:         "plain text secret - successful conversion",
			workloadName: "test-workload",
			clientSecret: "plain-text-secret",
			mockSetup: func(mock *mocks.MockProvider) {
				// Mock GenerateUniqueSecretNameWithProvider
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("", errors.New("secret not found"))

				// Mock StoreSecretInManagerWithProvider
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload", "plain-text-secret").
					Return(nil)
			},
			expectedResult: "OAUTH_CLIENT_SECRET_test-workload,target=oauth_secret",
			expectError:    false,
		},
		{
			name:         "plain text secret - storage fails",
			workloadName: "test-workload",
			clientSecret: "plain-text-secret",
			mockSetup: func(mock *mocks.MockProvider) {
				// Mock GenerateUniqueSecretNameWithProvider
				mock.EXPECT().
					GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
					Return("", errors.New("secret not found"))

				// Mock StoreSecretInManagerWithProvider - storage fails
				mock.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
				mock.EXPECT().
					SetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload", "plain-text-secret").
					Return(errors.New("storage failed"))
			},
			expectError:   true,
			errorContains: "failed to store secret in manager",
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

			result, err := ProcessOAuthClientSecretWithProvider(tc.workloadName, tc.clientSecret, mockProvider)

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
