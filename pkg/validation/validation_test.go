package validation_test

import (
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
