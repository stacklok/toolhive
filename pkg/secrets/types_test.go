// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSecretParameter(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		input          string
		expectError    bool
		errorContains  string
		expectedResult SecretParameter
	}{
		{
			name:           "valid CLI format",
			input:          "GITHUB_TOKEN,target=GITHUB_PERSONAL_ACCESS_TOKEN",
			expectError:    false,
			expectedResult: SecretParameter{Name: "GITHUB_TOKEN", Target: "GITHUB_PERSONAL_ACCESS_TOKEN"},
		},
		{
			name:           "valid CLI format with different target",
			input:          "MY_SECRET,target=CUSTOM_TARGET",
			expectError:    false,
			expectedResult: SecretParameter{Name: "MY_SECRET", Target: "CUSTOM_TARGET"},
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
			result, err := ParseSecretParameter(tc.input)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
				assert.Equal(t, SecretParameter{}, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedResult, result)
			}
		})
	}
}

func TestSecretParameter_ToCLIString(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		param    SecretParameter
		expected string
	}{
		{
			name:     "normal secret parameter",
			param:    SecretParameter{Name: "GITHUB_TOKEN", Target: "GITHUB_PERSONAL_ACCESS_TOKEN"},
			expected: "GITHUB_TOKEN,target=GITHUB_PERSONAL_ACCESS_TOKEN",
		},
		{
			name:     "secret parameter with different target",
			param:    SecretParameter{Name: "MY_SECRET", Target: "CUSTOM_TARGET"},
			expected: "MY_SECRET,target=CUSTOM_TARGET",
		},
		{
			name:     "secret parameter with special characters",
			param:    SecretParameter{Name: "MY-SECRET_123", Target: "CUSTOM-TARGET_456"},
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
