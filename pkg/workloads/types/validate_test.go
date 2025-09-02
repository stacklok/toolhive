package types_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/workloads/types"
)

func TestValidateWorkloadName(t *testing.T) {
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

		// Invalid cases - empty
		{
			name:         "empty workload name",
			workloadName: "",
			expectError:  true,
			errorMsg:     "workload name cannot be empty",
		},

		// Invalid cases - path traversal
		{
			name:         "path traversal with dots",
			workloadName: "../test",
			expectError:  true,
			errorMsg:     "path traversal",
		},
		{
			name:         "path traversal nested",
			workloadName: "../../etc/passwd",
			expectError:  true,
			errorMsg:     "path traversal",
		},

		// Invalid cases - absolute paths
		{
			name:         "absolute path unix",
			workloadName: "/etc/passwd",
			expectError:  true,
			errorMsg:     "path traversal",
		},
		{
			name:         "absolute path windows",
			workloadName: "C:\\Windows\\System32",
			expectError:  true,
			errorMsg:     "alphanumeric characters",
		},

		// Invalid cases - command injection
		{
			name:         "command injection with semicolon",
			workloadName: "test; rm -rf /",
			expectError:  true,
			errorMsg:     "path normalization",
		},
		{
			name:         "command injection with pipe",
			workloadName: "test | cat /etc/passwd",
			expectError:  true,
			errorMsg:     "dangerous characters",
		},
		{
			name:         "command injection with ampersand",
			workloadName: "test & echo hello",
			expectError:  true,
			errorMsg:     "dangerous characters",
		},
		{
			name:         "command injection with dollar",
			workloadName: "test$USER",
			expectError:  true,
			errorMsg:     "dangerous characters",
		},
		{
			name:         "command injection with backtick",
			workloadName: "test`whoami`",
			expectError:  true,
			errorMsg:     "dangerous characters",
		},
		{
			name:         "command injection with command substitution",
			workloadName: "test$(whoami)",
			expectError:  true,
			errorMsg:     "dangerous characters",
		},

		// Invalid cases - null bytes
		{
			name:         "null byte",
			workloadName: "test\x00workload",
			expectError:  true,
			errorMsg:     "null bytes",
		},

		// Invalid cases - invalid characters
		{
			name:         "invalid special characters",
			workloadName: "test@workload!",
			expectError:  true,
			errorMsg:     "alphanumeric characters",
		},
		{
			name:         "invalid unicode",
			workloadName: "test🚀workload",
			expectError:  true,
			errorMsg:     "alphanumeric characters",
		},
		{
			name:         "invalid spaces",
			workloadName: "test workload",
			expectError:  true,
			errorMsg:     "alphanumeric characters",
		},

		// Invalid cases - too long
		{
			name:         "too long name",
			workloadName: "a" + string(make([]byte, 100)), // 101 characters
			expectError:  true,
			errorMsg:     "null bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := types.ValidateWorkloadName(tt.workloadName)

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

func TestSanitizeWorkloadName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		input            string
		expectedOutput   string
		expectedModified bool
	}{
		// Valid cases that shouldn't be modified
		{
			name:             "valid simple name",
			input:            "test-workload",
			expectedOutput:   "test-workload",
			expectedModified: false,
		},
		{
			name:             "valid with underscores",
			input:            "test_workload",
			expectedOutput:   "test_workload",
			expectedModified: false,
		},
		{
			name:             "valid with dots",
			input:            "test.workload",
			expectedOutput:   "test.workload",
			expectedModified: false,
		},
		{
			name:             "valid alphanumeric",
			input:            "test123",
			expectedOutput:   "test123",
			expectedModified: false,
		},

		// Empty input
		{
			name:             "empty input",
			input:            "",
			expectedOutput:   "",
			expectedModified: false,
		},

		// Cases that should be sanitized
		{
			name:             "spaces replaced with dashes",
			input:            "test workload",
			expectedOutput:   "test-workload",
			expectedModified: true,
		},
		{
			name:             "special characters replaced",
			input:            "test@workload!",
			expectedOutput:   "test-workload-",
			expectedModified: true,
		},
		{
			name:             "unicode characters replaced",
			input:            "test🚀workload",
			expectedOutput:   "test-workload",
			expectedModified: true,
		},
		{
			name:             "path traversal sanitized",
			input:            "../test",
			expectedOutput:   "---test",
			expectedModified: true,
		},
		{
			name:             "absolute path sanitized",
			input:            "/etc/passwd",
			expectedOutput:   "etc-passwd",
			expectedModified: true,
		},
		{
			name:             "command injection sanitized",
			input:            "test; rm -rf /",
			expectedOutput:   "test--rm--rf-",
			expectedModified: true,
		},
		{
			name:             "null bytes removed",
			input:            "test\x00workload",
			expectedOutput:   "testworkload",
			expectedModified: true,
		},
		{
			name:             "mixed invalid characters",
			input:            "test@#$%^&*()workload",
			expectedOutput:   "test---------workload",
			expectedModified: true,
		},

		// Length limit
		{
			name:             "too long name truncated",
			input:            string(make([]byte, 150)), // 150 null bytes
			expectedOutput:   "workload",                // All null bytes removed, becomes empty, replaced with "workload"
			expectedModified: true,
		},

		// Edge case: becomes empty after sanitization
		{
			name:             "becomes empty after sanitization",
			input:            "@#$%^&*()",
			expectedOutput:   "---------",
			expectedModified: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			output, modified := types.SanitizeWorkloadName(tt.input)

			assert.Equal(t, tt.expectedOutput, output, "Output should match expected")
			assert.Equal(t, tt.expectedModified, modified, "Modified flag should match expected")

			// Ensure the output is always valid (if not empty)
			if output != "" {
				err := types.ValidateWorkloadName(output)
				assert.NoError(t, err, "Sanitized output should always be valid")
			}
		})
	}
}

func TestSanitizeWorkloadNameConsistency(t *testing.T) {
	t.Parallel()

	// Test that sanitized names always pass validation
	testInputs := []string{
		"../../../etc/passwd",
		"test; rm -rf /",
		"test | cat /etc/passwd",
		"test & echo hello",
		"test$USER",
		"test`whoami`",
		"test$(whoami)",
		"test\x00workload",
		"test@workload!",
		"test🚀workload",
		"test workload",
		"/absolute/path",
		"C:\\Windows\\System32",
		string(make([]byte, 200)), // Very long input
	}

	for _, input := range testInputs {
		t.Run("sanitize_"+input[:minInt(len(input), 20)], func(t *testing.T) {
			t.Parallel()
			sanitized, _ := types.SanitizeWorkloadName(input)

			if sanitized != "" {
				err := types.ValidateWorkloadName(sanitized)
				assert.NoError(t, err, "Sanitized name should always be valid: input=%q, sanitized=%q", input, sanitized)
			}
		})
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
