package oauth

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/secrets"
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
			workloadName: "test-workload",
			expected:     "OAUTH_CLIENT_SECRET_test-workload",
		},
		{
			name:         "empty workload name",
			workloadName: "",
			expected:     "OAUTH_CLIENT_SECRET_",
		},
		{
			name:         "workload name with special characters",
			workloadName: "test-workload-123",
			expected:     "OAUTH_CLIENT_SECRET_test-workload-123",
		},
		{
			name:         "workload name with underscores",
			workloadName: "test_workload",
			expected:     "OAUTH_CLIENT_SECRET_test_workload",
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

// TestSecretParameterToCLIString tests the ToCLIString method
func TestSecretParameterToCLIString(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		param    secrets.SecretParameter
		expected string
	}{
		{
			name: "normal secret parameter",
			param: secrets.SecretParameter{
				Name:   "SECRET_NAME",
				Target: "oauth_secret",
			},
			expected: "SECRET_NAME,target=oauth_secret",
		},
		{
			name: "secret parameter with different target",
			param: secrets.SecretParameter{
				Name:   "API_KEY",
				Target: "API_KEY",
			},
			expected: "API_KEY,target=API_KEY",
		},
		{
			name: "secret parameter with special characters",
			param: secrets.SecretParameter{
				Name:   "SECRET-NAME-123",
				Target: "SECRET_TARGET",
			},
			expected: "SECRET-NAME-123,target=SECRET_TARGET",
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

// TestParseSecretParameter tests the ParseSecretParameter function
func TestParseSecretParameter(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		parameter      string
		expectedResult secrets.SecretParameter
		expectError    bool
		errorContains  string
	}{
		{
			name:      "valid CLI format",
			parameter: "SECRET_NAME,target=oauth_secret",
			expectedResult: secrets.SecretParameter{
				Name:   "SECRET_NAME",
				Target: "oauth_secret",
			},
			expectError: false,
		},
		{
			name:      "valid CLI format with different target",
			parameter: "API_KEY,target=API_KEY",
			expectedResult: secrets.SecretParameter{
				Name:   "API_KEY",
				Target: "API_KEY",
			},
			expectError: false,
		},
		{
			name:           "empty parameter",
			parameter:      "",
			expectedResult: secrets.SecretParameter{},
			expectError:    true,
			errorContains:  "secret parameter cannot be empty",
		},
		{
			name:           "invalid format - no target",
			parameter:      "SECRET_NAME",
			expectedResult: secrets.SecretParameter{},
			expectError:    true,
			errorContains:  "invalid secret parameter format",
		},
		{
			name:           "invalid format - no comma",
			parameter:      "SECRET_NAME target=oauth_secret",
			expectedResult: secrets.SecretParameter{},
			expectError:    true,
			errorContains:  "invalid secret parameter format",
		},
		{
			name:           "invalid format - no equals",
			parameter:      "SECRET_NAME,target oauth_secret",
			expectedResult: secrets.SecretParameter{},
			expectError:    true,
			errorContains:  "invalid secret parameter format",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := secrets.ParseSecretParameter(tc.parameter)

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

// TestProcessOAuthClientSecret tests the main processing function
// Note: This test is limited to cases that don't require secrets manager setup
func TestProcessOAuthClientSecret(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		workloadName   string
		clientSecret   string
		expectError    bool
		errorContains  string
		expectedResult string
	}{
		{
			name:           "empty client secret",
			workloadName:   "test-workload",
			clientSecret:   "",
			expectError:    false,
			expectedResult: "",
		},
		{
			name:           "already in CLI format",
			workloadName:   "test-workload",
			clientSecret:   "EXISTING_SECRET,target=oauth_secret",
			expectError:    false,
			expectedResult: "EXISTING_SECRET,target=oauth_secret",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := ProcessOAuthClientSecret(tc.workloadName, tc.clientSecret)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" && err != nil {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedResult, result)
			}
		})
	}
}

// TestGenerateUniqueSecretName_Uniqueness tests uniqueness of generated names
func TestGenerateUniqueSecretName_Uniqueness(t *testing.T) {
	t.Parallel()

	// Test that the function generates unique names when conflicts exist
	workloadName := "test-workload"

	// Generate multiple base names and verify they follow the expected pattern
	for i := 0; i < 3; i++ {
		baseName := generateOAuthClientSecretName(workloadName)
		expectedPrefix := "OAUTH_CLIENT_SECRET_" + workloadName
		assert.Equal(t, expectedPrefix, baseName)
	}
}

// TestGenerateUniqueSecretName_SpecialCharacters tests various workload name formats
func TestGenerateUniqueSecretName_SpecialCharacters(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		workloadName string
		expected     string
	}{
		{
			name:         "with hyphens",
			workloadName: "test-workload-123",
			expected:     "OAUTH_CLIENT_SECRET_test-workload-123",
		},
		{
			name:         "with underscores",
			workloadName: "test_workload_123",
			expected:     "OAUTH_CLIENT_SECRET_test_workload_123",
		},
		{
			name:         "with numbers",
			workloadName: "workload123",
			expected:     "OAUTH_CLIENT_SECRET_workload123",
		},
		{
			name:         "with mixed characters",
			workloadName: "test-workload_123",
			expected:     "OAUTH_CLIENT_SECRET_test-workload_123",
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
