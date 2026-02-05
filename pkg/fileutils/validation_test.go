// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileutils_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/fileutils"
)

func TestValidateWorkloadNameForPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		expectError  bool
		errorMsg     string
	}{
		// Valid cases
		{
			name:         "valid simple name",
			workloadName: "test-workload",
			expectError:  false,
		},
		{
			name:         "valid with underscores",
			workloadName: "test_workload",
			expectError:  false,
		},
		{
			name:         "valid with dots",
			workloadName: "test.workload",
			expectError:  false,
		},
		{
			name:         "valid alphanumeric",
			workloadName: "test123",
			expectError:  false,
		},
		{
			name:         "valid mixed characters",
			workloadName: "test-workload_123.v1",
			expectError:  false,
		},

		// Invalid cases - path traversal
		{
			name:         "path traversal with double dots",
			workloadName: "../test",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},
		{
			name:         "path traversal nested",
			workloadName: "../../etc/passwd",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},
		{
			name:         "path traversal in middle",
			workloadName: "test/../passwd",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},

		// Invalid cases - path separators
		{
			name:         "forward slash",
			workloadName: "test/workload",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},
		{
			name:         "backslash",
			workloadName: "test\\workload",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},
		{
			name:         "absolute path unix",
			workloadName: "/etc/passwd",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},

		// Invalid cases - empty
		{
			name:         "empty workload name",
			workloadName: "",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},

		// Invalid cases - command injection
		{
			name:         "command injection with semicolon",
			workloadName: "test; rm -rf /",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},
		{
			name:         "command injection with pipe",
			workloadName: "test | cat /etc/passwd",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},

		// Invalid cases - null bytes
		{
			name:         "null byte",
			workloadName: "test\x00workload",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},

		// Invalid cases - invalid characters
		{
			name:         "invalid special characters",
			workloadName: "test@workload!",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},
		{
			name:         "invalid spaces",
			workloadName: "test workload",
			expectError:  true,
			errorMsg:     "invalid workload name for path construction",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := fileutils.ValidateWorkloadNameForPath(tt.workloadName)

			if tt.expectError {
				assert.Error(t, err, "Expected error for input: %q", tt.workloadName)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg, "Error message should contain expected text")
				}
			} else {
				assert.NoError(t, err, "Did not expect error for input: %q", tt.workloadName)
			}
		})
	}
}

// TestValidateWorkloadNameForPathSecurityCases tests specific security-focused scenarios
func TestValidateWorkloadNameForPathSecurityCases(t *testing.T) {
	t.Parallel()

	// These are real-world attack patterns that should always be rejected
	attackPatterns := []string{
		"../../../etc/passwd",
		"./../../../etc/passwd",
		"../../../../../../etc/passwd",
		"/etc/passwd",
		"/etc/shadow",
		"C:\\Windows\\System32",
		"..\\..\\..\\Windows\\System32",
		"test; rm -rf /",
		"test && cat /etc/passwd",
		"test | whoami",
		"test$(whoami)",
		"test`whoami`",
		"test$USER",
		"test\x00workload",
		"test/subdir",
		"test\\subdir",
	}

	for _, pattern := range attackPatterns {
		t.Run("reject_"+pattern, func(t *testing.T) {
			t.Parallel()

			err := fileutils.ValidateWorkloadNameForPath(pattern)
			assert.Error(t, err, "Should reject attack pattern: %q", pattern)
			assert.Contains(t, err.Error(), "invalid workload name for path construction",
				"Error should indicate path construction issue for: %q", pattern)
		})
	}
}
