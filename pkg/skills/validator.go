// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// skillNameRegex validates skill names: 2-64 chars, lowercase alphanumeric and hyphens,
// must start and end with alphanumeric.
var skillNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`)

// MaxCompatibilityLength is the maximum allowed length for the compatibility field.
const MaxCompatibilityLength = 500

// MaxDescriptionLength is the maximum allowed length for the description field.
const MaxDescriptionLength = 1024

// RecommendedMaxSkillMDLines is the recommended maximum number of lines in SKILL.md.
// Exceeding this generates a warning, not an error.
const RecommendedMaxSkillMDLines = 500

// ValidateSkillDir validates a skill directory at the given path.
// I/O errors are returned as error; validation issues are returned in ValidationResult.
func ValidateSkillDir(path string) (*ValidationResult, error) {
	var errs []string
	var warnings []string

	// Check SKILL.md exists
	skillMDPath := filepath.Join(filepath.Clean(path), "SKILL.md")
	content, err := os.ReadFile(skillMDPath) //nolint:gosec // path is validated by caller
	if err != nil {
		if os.IsNotExist(err) {
			return &ValidationResult{
				Valid:  false,
				Errors: []string{"SKILL.md not found in skill directory"},
			}, nil
		}
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}

	// Check for symlinks
	if err := checkSymlinks(path); err != nil {
		errs = append(errs, err.Error())
	}

	// Check for path traversal
	if err := checkPathTraversal(path); err != nil {
		errs = append(errs, err.Error())
	}

	// Parse frontmatter
	result, err := ParseSkillMD(content)
	if err != nil {
		errs = append(errs, fmt.Sprintf("invalid SKILL.md: %v", err))
		return &ValidationResult{
			Valid:  false,
			Errors: errs,
		}, nil
	}

	// Validate name
	if err := validateName(result.Name); err != nil {
		errs = append(errs, err.Error())
	}

	// Validate name matches directory
	dirName := filepath.Base(filepath.Clean(path))
	if result.Name != "" && result.Name != dirName {
		errs = append(errs,
			fmt.Sprintf("skill name %q must match directory name %q", result.Name, dirName))
	}

	// Validate required fields
	if result.Name == "" {
		errs = append(errs, "name is required")
	}
	if result.Description == "" {
		errs = append(errs, "description is required")
	}

	// Validate field constraints
	if len(result.Description) > MaxDescriptionLength {
		errs = append(errs,
			fmt.Sprintf("description exceeds maximum length of %d characters", MaxDescriptionLength))
	}
	if len(result.Compatibility) > MaxCompatibilityLength {
		errs = append(errs,
			fmt.Sprintf("compatibility field exceeds maximum length of %d characters", MaxCompatibilityLength))
	}

	// Warnings (non-blocking)
	lineCount := bytes.Count(content, []byte("\n")) + 1
	if lineCount > RecommendedMaxSkillMDLines {
		warnings = append(warnings,
			fmt.Sprintf("SKILL.md has %d lines (recommended max: %d)", lineCount, RecommendedMaxSkillMDLines))
	}

	return &ValidationResult{
		Valid:    len(errs) == 0,
		Errors:   errs,
		Warnings: warnings,
	}, nil
}

// validateName checks that a skill name matches the required pattern.
func validateName(name string) error {
	if name == "" {
		return nil // Caught by required fields check
	}
	if !skillNameRegex.MatchString(name) {
		return fmt.Errorf("invalid skill name %q: must be 2-64 lowercase alphanumeric characters or hyphens, "+
			"starting and ending with alphanumeric", name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("invalid skill name %q: must not contain consecutive hyphens", name)
	}
	return nil
}

// checkSymlinks walks the directory and checks for symbolic links.
func checkSymlinks(path string) error {
	return filepath.Walk(path, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}

		info, err := os.Lstat(p)
		if err != nil {
			return nil
		}

		if info.Mode()&os.ModeSymlink != 0 {
			rel, _ := filepath.Rel(path, p)
			return fmt.Errorf("symlink found at %q: symlinks are not allowed in skill directories", rel)
		}
		return nil
	})
}

// checkPathTraversal walks the directory and checks for path traversal patterns.
func checkPathTraversal(path string) error {
	return filepath.Walk(path, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(path, p)
		if err != nil {
			return nil
		}

		for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
			if component == ".." {
				return fmt.Errorf("path traversal detected in %q: '..' components are not allowed", rel)
			}
		}
		return nil
	})
}
