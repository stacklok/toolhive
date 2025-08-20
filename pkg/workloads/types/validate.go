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

// validateWorkloadName validates workload names to prevent path traversal attacks
// and other security issues. Workload names should only contain alphanumeric
// characters, hyphens, underscores, and dots.
var workloadNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidateWorkloadName checks if the provided workload name is valid.
func ValidateWorkloadName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: workload name cannot be empty", ErrInvalidWorkloadName)
	}

	// Use filepath.Clean to normalize the path
	cleanName := filepath.Clean(name)

	// Check if the cleaned path tries to escape current directory using filepath.Rel
	if rel, err := filepath.Rel(".", cleanName); err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%w: workload name contains path traversal", ErrInvalidWorkloadName)
	}

	// Check for absolute paths
	if filepath.IsAbs(cleanName) {
		return fmt.Errorf("%w: workload name cannot be an absolute path", ErrInvalidWorkloadName)
	}

	// Check for command injection patterns (similar to permissions package)
	commandInjectionPattern := regexp.MustCompile(`[$&;|]|\$\(|\` + "`")
	if commandInjectionPattern.MatchString(name) {
		return fmt.Errorf("%w: workload name contains potentially dangerous characters", ErrInvalidWorkloadName)
	}

	// Check for null bytes
	if strings.Contains(name, "\x00") {
		return fmt.Errorf("%w: workload name contains null bytes", ErrInvalidWorkloadName)
	}

	// Validate against allowed pattern
	if !workloadNamePattern.MatchString(name) {
		return fmt.Errorf("%w: workload name can only contain alphanumeric characters, dots, hyphens, and underscores",
			ErrInvalidWorkloadName)
	}

	// Reasonable length limit
	if len(name) > 100 {
		return fmt.Errorf("%w: workload name too long (max 100 characters)", ErrInvalidWorkloadName)
	}

	return nil
}
