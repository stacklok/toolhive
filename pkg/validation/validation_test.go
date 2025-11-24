package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateGroupName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		// ✅ Valid cases
		{"valid_simple_name", "teamalpha", false},
		{"valid_with_spaces", "team alpha", false},
		{"valid_with_dash_and_underscore", "team-alpha_123", false},

		// ❌ Empty or whitespace-only
		{"empty_string", "", true},
		{"only_spaces", "    ", true},

		// ❌ Invalid characters
		{"invalid_special_characters", "team@alpha!", true},
		{"invalid_unicode", "团队🚀", true},

		// ❌ Null byte
		{"null_byte", "team\x00alpha", true},

		// ❌ Leading/trailing whitespace
		{"leading_space", " teamalpha", true},
		{"trailing_space", "teamalpha ", true},

		// ❌ Consecutive spaces
		{"consecutive_spaces_middle", "team  alpha", true},
		{"consecutive_spaces_start", "  teamalpha", true},
		{"consecutive_spaces_end", "teamalpha  ", true},

		// ❌ Uppercase letters
		{"uppercase_letters", "TeamAlpha", true},

		// ✅ Borderline valid
		{"single_char", "t", false},
		{"max_typical", "alpha team 2025 - squad_01", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateGroupName(tc.input)
			if tc.expectErr {
				assert.Error(t, err, "Expected error for input: %q", tc.input)
			} else {
				assert.NoError(t, err, "Did not expect error for input: %q", tc.input)
			}
		})
	}
}

func TestValidateHTTPHeaderName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		// Valid cases
		{"valid simple", "X-API-Key", false},
		{"valid authorization", "Authorization", false},
		{"valid with numbers", "X-API-Key-123", false},
		{"valid with dots", "X.Custom.Header", false},

		// CRLF injection attacks
		{"crlf injection", "X-API-Key\r\nX-Injected: malicious", true},
		{"newline injection", "X-API-Key\nInjected", true},
		{"carriage return", "X-API-Key\r", true},

		// Other invalid characters
		{"null byte", "X-API-Key\x00", true},
		{"contains space", "X API Key", true},
		{"empty string", "", true},

		// Length limits
		{"too long", strings.Repeat("A", 300), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateHTTPHeaderName(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateHTTPHeaderValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		// Valid cases
		{"valid simple", "my-api-key-12345", false},
		{"valid with spaces", "Bearer token123", false},
		{"valid special chars", "key!@#$%^&*()", false},

		// CRLF injection attacks
		{"crlf injection", "key\r\nX-Injected: malicious", true},
		{"newline injection", "key\ninjected", true},
		{"carriage return", "key\r", true},

		// Control characters
		{"null byte", "key\x00value", true},
		{"control char", "key\x01value", true},
		{"delete char", "key\x7Fvalue", true},
		{"tab allowed", "key\tvalue", false}, // Tab is allowed in values

		// Length limits
		{"too long", strings.Repeat("A", 10000), true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateHTTPHeaderValue(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateResourceURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		expectError   bool
		errorContains string
	}{
		// Valid cases
		{
			name:        "valid https URL with path",
			input:       "https://mcp.example.com/mcp",
			expectError: false,
		},
		{
			name:        "valid https URL without path",
			input:       "https://mcp.example.com",
			expectError: false,
		},
		{
			name:        "valid https URL with port",
			input:       "https://mcp.example.com:8443",
			expectError: false,
		},
		{
			name:        "valid https URL with port and path",
			input:       "https://mcp.example.com:8443/api/mcp",
			expectError: false,
		},
		{
			name:        "valid http URL",
			input:       "http://localhost:3000",
			expectError: false,
		},
		{
			name:        "root path slash is valid",
			input:       "https://mcp.example.com/",
			expectError: false,
		},
		// Invalid cases
		{
			name:          "empty string",
			input:         "",
			expectError:   true,
			errorContains: "cannot be empty",
		},
		{
			name:          "missing scheme",
			input:         "mcp.example.com",
			expectError:   true,
			errorContains: "must include a scheme",
		},
		{
			name:          "missing host",
			input:         "https://",
			expectError:   true,
			errorContains: "must include a host",
		},
		{
			name:          "contains fragment",
			input:         "https://mcp.example.com/mcp#section",
			expectError:   true,
			errorContains: "must not contain fragments",
		},
		{
			name:          "invalid URL format",
			input:         "ht!tp://invalid",
			expectError:   true,
			errorContains: "invalid resource URI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateResourceURI(tt.input)

			if tt.expectError {
				require.Error(t, err, "Expected an error but got none")
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains,
						"Error message should contain expected text")
				}
			} else {
				require.NoError(t, err, "Expected no error but got: %v", err)
			}
		})
	}
}
