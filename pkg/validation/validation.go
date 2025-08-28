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

// SanitizeWorkloadName sanitizes a user-provided workload name to ensure it's safe for file paths.
// It allows only alphanumeric characters and dashes, replacing any other character with a dash.
// Returns the sanitized name and a boolean indicating whether the name was modified.
// This is used for both container names and remote server names.
func SanitizeWorkloadName(name string) (string, bool) {
	if name == "" {
		return "", false
	}

	var sanitized strings.Builder
	modified := false

	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			sanitized.WriteRune(c)
		} else {
			sanitized.WriteRune('-')
			modified = true
		}
	}

	result := sanitized.String()
	return result, modified
}
