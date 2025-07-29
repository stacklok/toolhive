// Package validation provides functions for validating input data.
package validation

import (
	"fmt"
	"regexp"
	"strings"
)

var validGroupNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-\s]+$`)

// ValidateGroupName validates that a group name only contains allowed characters:
// alphanumeric, underscore, dash, and space.
// It also enforces no leading/trailing/consecutive spaces and disallows null bytes.
func ValidateGroupName(name string) error {
	if name == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("group name cannot be empty or consist only of whitespace")
	}

	// Check for null bytes explicitly
	if strings.Contains(name, "\x00") {
		return fmt.Errorf("group name cannot contain null bytes")
	}

	// Validate characters
	if !validGroupNameRegex.MatchString(name) {
		return fmt.Errorf("group name can only contain alphanumeric characters, underscores, dashes, and spaces: %q", name)
	}

	// Check for leading/trailing whitespace
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("group name cannot have leading or trailing whitespace: %q", name)
	}

	// Check for consecutive spaces
	if strings.Contains(name, "  ") {
		return fmt.Errorf("group name cannot contain consecutive spaces: %q", name)
	}

	return nil
}
