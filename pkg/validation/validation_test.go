package validation_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/validation"
)

func TestValidateGroupName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		// ✅ Valid cases
		{"valid_simple_name", "TeamAlpha", false},
		{"valid_with_spaces", "Team Alpha", false},
		{"valid_with_dash_and_underscore", "Team-Alpha_123", false},

		// ❌ Empty or whitespace-only
		{"empty_string", "", true},
		{"only_spaces", "    ", true},

		// ❌ Invalid characters
		{"invalid_special_characters", "Team@Alpha!", true},
		{"invalid_unicode", "团队🚀", true},

		// ❌ Null byte
		{"null_byte", "Team\x00Alpha", true},

		// ❌ Leading/trailing whitespace
		{"leading_space", " TeamAlpha", true},
		{"trailing_space", "TeamAlpha ", true},

		// ❌ Consecutive spaces
		{"consecutive_spaces_middle", "Team  Alpha", true},
		{"consecutive_spaces_start", "  TeamAlpha", true},
		{"consecutive_spaces_end", "TeamAlpha  ", true},

		// ✅ Borderline valid
		{"single_char", "T", false},
		{"max_typical", "Alpha Team 2025 - Squad_01", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validation.ValidateGroupName(tc.input)
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
			err := validation.ValidateHTTPHeaderName(tt.input)
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
			err := validation.ValidateHTTPHeaderValue(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
