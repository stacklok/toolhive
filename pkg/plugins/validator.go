// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/skills"
)

// ValidatePluginName checks that a plugin name conforms to the kebab-case rule
// shared with skills (2-64 lowercase alphanumeric/hyphens, no consecutive
// hyphens). Re-exported from skills because the rule is identical.
func ValidatePluginName(name string) error { return skills.ValidateSkillName(name) }

// CheckFilesystem walks a plugin directory checking for symlinks and path
// traversal. Re-exported from skills (the filesystem safety check is generic).
var CheckFilesystem = skills.CheckFilesystem

// ValidatePluginDir validates a plugin directory at the given path. It is the
// plugin analogue of skills.ValidateSkillDir. Steps:
//  1. Clean + absolute path check.
//  2. CheckFilesystem (symlink + traversal walk).
//  3. ParsePluginManifest (strict keywords + component-path checks).
//  4. Validate name (ValidatePluginName) and assert it matches the dir basename.
//  5. For each manifest.Skills entry, reuse skills.ValidateSkillDir on the
//     bundled skill at <pluginDir>/<path-without-leading-./> — this reuses the
//     pkg/skills validator for bundled skills rather than duplicating it.
//     Both errors and warnings of bundled skills are reported.
//
// I/O errors are returned as error; validation issues are returned in
// ValidationResult, both as legacy strings and as Findings whose File is
// relative to path.
func ValidatePluginDir(path string) (*ValidationResult, error) {
	// Defense-in-depth: sanitize and validate the path before any filesystem
	// access. The caller (pluginsvc.Validate) also validates via
	// validateLocalPath, but we re-check here because ValidatePluginDir is
	// exported and may be called directly.
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute, got %q", path)
	}

	res := &ValidationResult{}

	// Check for symlinks and path traversal in a single walk.
	res.AddFilesystemError(CheckFilesystem(path))

	// Parse the manifest.
	manifest, err := ParsePluginManifest(path)
	if err != nil {
		res.AddFinding(manifestError(skills.RuleManifestInvalid,
			fmt.Sprintf("invalid plugin manifest: %v", err)))
		return res, nil
	}

	// Validate name + match directory basename.
	dirName := filepath.Base(path)
	if manifest.Name == "" {
		res.AddFinding(manifestError(skills.RuleNameRequired, "plugin name is required in manifest"))
	} else {
		if err := ValidatePluginName(manifest.Name); err != nil {
			res.AddFinding(manifestError(skills.RuleNameInvalid, err.Error()))
		}
		if manifest.Name != dirName {
			res.AddFinding(manifestError(skills.RuleNameDirMismatch,
				fmt.Sprintf("plugin name %q must match directory name %q", manifest.Name, dirName)))
		}
	}

	// Reuse pkg/skills.Validator for bundled skills. Each manifest.Skills entry
	// is a relative "./..." path; strip the leading "./" to resolve it under
	// the plugin directory. WithPluginRoot contains references by the plugin
	// directory, because the whole plugin tree is installed. Bundled findings
	// (errors and warnings) are reported with File relative to the plugin
	// directory and with the legacy strings prefixed by the bundled skill path.
	for _, skillPath := range manifest.Skills {
		relDir := strings.TrimPrefix(skillPath, "./")
		bundledPath := filepath.Join(path, relDir)
		prefix := fmt.Sprintf("bundled skill %q: ", skillPath)
		skillResult, skillErr := skills.ValidateSkillDir(bundledPath, skills.WithPluginRoot(path))
		if skillErr != nil {
			// I/O error (e.g. bundled path missing) — surface as a validation error,
			// not a hard error, so the caller still gets a ValidationResult.
			res.AddPrefixedFindings(&ValidationResult{Findings: []skills.Finding{{
				Severity: skills.SeverityError,
				Rule:     skills.RuleBundledSkillUnreadable,
				File:     ".",
				Message:  skillErr.Error(),
			}}}, relDir, prefix)
			continue
		}
		// WithPluginRoot already reports File relative to the plugin directory.
		res.AddPrefixedFindings(skillResult, "", prefix)
	}

	res.Valid = len(res.Errors) == 0
	return res, nil
}

// manifestError returns an error finding located in the plugin manifest.
func manifestError(rule, msg string) skills.Finding {
	return skills.Finding{Severity: skills.SeverityError, Rule: rule, File: ManifestPath, Message: msg}
}
