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
			err := validation.ValidateGroupName(tc.input)
			if tc.expectErr {
				assert.Error(t, err, "Expected error for input: %q", tc.input)
			} else {
				assert.NoError(t, err, "Did not expect error for input: %q", tc.input)
			}
		})
	}
}
