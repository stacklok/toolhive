// Package types contains types and validation functions for workloads in ToolHive.
// This is separated to avoid circular dependencies with the core package.
package types

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrInvalidWorkloadName is returned when a workload name fails validation.
var ErrInvalidWorkloadName = fmt.Errorf("invalid workload name")

// workloadNamePattern validates workload names to prevent path traversal attacks
// and other security issues. Workload names should only contain alphanumeric
// characters, hyphens, underscores, and dots.
var workloadNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// commandInjectionPattern detects potentially dangerous command injection patterns
var commandInjectionPattern = regexp.MustCompile(`[$&;|]|\$\(|\` + "`")

// WorkloadNameIssue represents a specific issue found in a workload name
type WorkloadNameIssue struct {
	Type        string // "empty", "path_traversal", "absolute_path", "command_injection", "null_bytes", "invalid_chars", "too_long"
	Description string
	Position    int // For character-specific issues
}

// analyzeWorkloadName performs comprehensive analysis of a workload name and returns all issues found.
// This shared logic is used by both ValidateWorkloadName and SanitizeWorkloadName.
func analyzeWorkloadName(name string) []WorkloadNameIssue {
	var issues []WorkloadNameIssue

	if name == "" {
		issues = append(issues, WorkloadNameIssue{
			Type:        "empty",
			Description: "workload name cannot be empty",
		})
		return issues
	}

	// Check for null bytes
	if strings.Contains(name, "\x00") {
		issues = append(issues, WorkloadNameIssue{
			Type:        "null_bytes",
			Description: "workload name contains null bytes",
		})
	}

	// Use filepath.Clean to normalize the path and check for changes
	cleanName := filepath.Clean(name)
	if cleanName != name {
		issues = append(issues, WorkloadNameIssue{
			Type:        "path_normalization",
			Description: "workload name requires path normalization",
		})
	}

	// Check if the cleaned path tries to escape current directory
	if rel, err := filepath.Rel(".", cleanName); err != nil || strings.HasPrefix(rel, "..") {
		issues = append(issues, WorkloadNameIssue{
			Type:        "path_traversal",
			Description: "workload name contains path traversal",
		})
	}

	// Check for absolute paths
	if filepath.IsAbs(cleanName) {
		issues = append(issues, WorkloadNameIssue{
			Type:        "absolute_path",
			Description: "workload name cannot be an absolute path",
		})
	}

	// Check for command injection patterns
	if commandInjectionPattern.MatchString(name) {
		issues = append(issues, WorkloadNameIssue{
			Type:        "command_injection",
			Description: "workload name contains potentially dangerous characters",
		})
	}

	// Check against allowed pattern
	if !workloadNamePattern.MatchString(name) {
		issues = append(issues, WorkloadNameIssue{
			Type:        "invalid_chars",
			Description: "workload name can only contain alphanumeric characters, dots, hyphens, and underscores",
		})
	}

	// Check length limit
	if len(name) > 100 {
		issues = append(issues, WorkloadNameIssue{
			Type:        "too_long",
			Description: "workload name too long (max 100 characters)",
		})
	}

	return issues
}

// ValidateWorkloadName checks if the provided workload name is valid.
// This function performs strict validation and rejects invalid names.
func ValidateWorkloadName(name string) error {
	issues := analyzeWorkloadName(name)

	if len(issues) == 0 {
		return nil
	}

	// Return the first critical issue found
	issue := issues[0]
	return fmt.Errorf("%w: %s", ErrInvalidWorkloadName, issue.Description)
}

// SanitizeWorkloadName sanitizes a user-provided workload name to ensure it's safe for file paths.
// It applies the same security analysis as ValidateWorkloadName but transforms invalid characters
// instead of rejecting them. This provides a more permissive approach for user-facing scenarios
// where we want to accept user input and make it safe rather than rejecting it.
// Returns the sanitized name and a boolean indicating whether the name was modified.
func SanitizeWorkloadName(name string) (string, bool) {
	if name == "" {
		return "", false
	}

	original := name
	result := name
	modified := false

	// Apply fixes based on the issues found
	issues := analyzeWorkloadName(name)

	for _, issue := range issues {
		var wasModified bool
		result, wasModified = applySanitizationFix(result, issue.Type)
		if wasModified {
			modified = true
		}
	}

	// Ensure we don't return an empty string after sanitization
	if result == "" {
		result = "workload"
		modified = true
	}

	return result, modified || (result != original)
}

// applySanitizationFix applies a specific sanitization fix to the input string
func applySanitizationFix(input, issueType string) (string, bool) {
	switch issueType {
	case "null_bytes":
		return strings.ReplaceAll(input, "\x00", ""), true

	case "path_normalization":
		return filepath.Clean(input), true

	case "path_traversal":
		return strings.ReplaceAll(input, "..", "--"), true

	case "absolute_path":
		return strings.TrimLeft(input, "/\\"), true

	case "command_injection":
		return commandInjectionPattern.ReplaceAllString(input, "-"), true

	case "invalid_chars":
		return sanitizeInvalidChars(input)

	case "too_long":
		return truncateIfTooLong(input)

	default:
		return input, false
	}
}

// sanitizeInvalidChars sanitizes characters to only allow alphanumeric, dots, hyphens, and underscores
func sanitizeInvalidChars(input string) (string, bool) {
	var sanitized strings.Builder
	modified := false

	for _, c := range input {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' {
			sanitized.WriteRune(c)
		} else {
			sanitized.WriteRune('-')
			modified = true
		}
	}

	return sanitized.String(), modified
}

// truncateIfTooLong truncates the input if it's longer than 100 characters
func truncateIfTooLong(input string) (string, bool) {
	if len(input) > 100 {
		return input[:100], true
	}
	return input, false
}
