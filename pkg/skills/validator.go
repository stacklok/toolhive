// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"bytes"
	"fmt"
	"io/fs"
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
	// Defense-in-depth: sanitize and validate the path before any filesystem access.
	// The caller (skillsvc.Validate) also validates via validateLocalPath, but we
	// re-check here because ValidateSkillDir is exported and may be called directly.
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute, got %q", path)
	}

	var errs []string
	var warnings []string

	// Check SKILL.md exists
	skillMDPath := filepath.Join(path, "SKILL.md")
	content, err := os.ReadFile(skillMDPath) //#nosec G304 -- path is cleaned and validated as absolute above
	if err != nil {
		if os.IsNotExist(err) {
			return &ValidationResult{
				Valid:  false,
				Errors: []string{"SKILL.md not found in skill directory"},
			}, nil
		}
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}

	// Check for symlinks and path traversal in a single walk
	if err := CheckFilesystem(path); err != nil {
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

	// Validate parsed fields
	errs = append(errs, validateFields(result, filepath.Base(path))...)

	// Collect warnings
	warnings = append(warnings, collectWarnings(result, content)...)

	return &ValidationResult{
		Valid:    len(errs) == 0,
		Errors:   errs,
		Warnings: warnings,
	}, nil
}

// validateFields checks parsed frontmatter fields against spec constraints.
func validateFields(result *ParseResult, dirName string) []string {
	var errs []string

	if result.Name == "" {
		errs = append(errs, "name is required")
	} else {
		if err := ValidateSkillName(result.Name); err != nil {
			errs = append(errs, err.Error())
		}
		if result.Name != dirName {
			errs = append(errs,
				fmt.Sprintf("skill name %q must match directory name %q", result.Name, dirName))
		}
	}
	if result.Description == "" {
		errs = append(errs, "description is required")
	}
	if len(result.Description) > MaxDescriptionLength {
		errs = append(errs,
			fmt.Sprintf("description exceeds maximum length of %d characters", MaxDescriptionLength))
	}
	if len(result.Compatibility) > MaxCompatibilityLength {
		errs = append(errs,
			fmt.Sprintf("compatibility field exceeds maximum length of %d characters", MaxCompatibilityLength))
	}

	return errs
}

// collectWarnings generates non-blocking warnings for spec compliance.
func collectWarnings(result *ParseResult, content []byte) []string {
	var warnings []string

	if len(result.AllowedTools) > 0 && bytes.Contains(content, []byte(",")) &&
		bytes.Contains(content, []byte("allowed-tools:")) {
		warnings = append(warnings,
			"allowed-tools uses comma-delimited format, which is a ToolHive extension; "+
				"the Agent Skills spec defines space-delimited as the canonical format")
	}
	lineCount := bytes.Count(content, []byte("\n")) + 1
	if lineCount > RecommendedMaxSkillMDLines {
		warnings = append(warnings,
			fmt.Sprintf("SKILL.md has %d lines (recommended max: %d)", lineCount, RecommendedMaxSkillMDLines))
	}

	return warnings
}

// ValidateSkillName checks that a skill name conforms to the Agent Skills specification.
// Names must be 2-64 lowercase alphanumeric characters or hyphens, starting and ending
// with alphanumeric, with no consecutive hyphens.
// See: https://agentskills.io/specification
func ValidateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("invalid skill name: must not be empty")
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

// CheckFilesystem walks the directory once, checking for symlinks and path traversal.
func CheckFilesystem(path string) error {
	return filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}

		rel, err := filepath.Rel(path, p)
		if err != nil {
			return nil
		}

		// Check for path traversal
		for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
			if component == ".." {
				return fmt.Errorf("path traversal detected in %q: '..' components are not allowed", rel)
			}
		}

		// Check for symlinks (WalkDir doesn't stat, so use Lstat)
		info, err := os.Lstat(p)
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink found at %q: symlinks are not allowed in skill directories", rel)
		}

		return nil
	})
}
