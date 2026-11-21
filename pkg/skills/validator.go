// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
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

// ValidateOption configures ValidateSkillDir.
type ValidateOption func(*validateConfig)

type validateConfig struct {
	pluginRoot string
}

// WithPluginRoot validates the skill as one bundled in the plugin at
// pluginDir, which must be an absolute path containing the skill directory.
// It makes three changes, all reflecting how plugins are installed (the whole
// plugin tree is extracted, with executable bits kept):
//   - references are contained by the plugin directory instead of the skill
//     directory, so "../shared/x.md" inside the plugin is not reported;
//   - the script-direct-exec check is disabled;
//   - Finding.File is relative to pluginDir instead of the skill directory.
func WithPluginRoot(pluginDir string) ValidateOption {
	return func(c *validateConfig) { c.pluginRoot = pluginDir }
}

// ValidateSkillDir validates a skill directory at the given path.
// I/O errors are returned as error; validation issues are returned in
// ValidationResult, both as legacy strings and as Findings whose File is
// relative to path (or to the plugin directory, see WithPluginRoot).
func ValidateSkillDir(path string, opts ...ValidateOption) (*ValidationResult, error) {
	// Defense-in-depth: sanitize and validate the path before any filesystem access.
	// The caller (skillsvc.Validate) also validates via validateLocalPath, but we
	// re-check here because ValidateSkillDir is exported and may be called directly.
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute, got %q", path)
	}

	var cfg validateConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	root, skillRel := path, "."
	if cfg.pluginRoot != "" {
		root = filepath.Clean(cfg.pluginRoot)
		// ValidatePluginDir only passes manifest paths that parsePluginManifest
		// has already checked for "..", so this cannot fire through it; it
		// guards direct callers.
		rel, ok := relWithin(root, path)
		if !filepath.IsAbs(root) || !ok {
			return nil, fmt.Errorf("plugin root %q must be an absolute path containing %q", cfg.pluginRoot, path)
		}
		skillRel = filepath.ToSlash(rel)
	}

	res, err := validateSkillDir(path, root, cfg.pluginRoot == "")
	if err != nil {
		return nil, err
	}
	for i := range res.Findings {
		res.Findings[i].File = pathpkg.Join(skillRel, res.Findings[i].File)
	}
	return res, nil
}

// validateSkillDir runs every check on the skill at path, with references
// contained by root. Finding.File is relative to path.
func validateSkillDir(path, root string, checkExec bool) (*ValidationResult, error) {
	res := &ValidationResult{}

	// Check SKILL.md exists
	skillMDPath := filepath.Join(path, SkillMDFile)
	content, err := os.ReadFile(skillMDPath) //#nosec G304 -- path is cleaned and validated as absolute by the caller
	if err != nil {
		if os.IsNotExist(err) {
			res.AddFinding(skillMDError(RuleSkillMDMissing, "SKILL.md not found in skill directory"))
			return res, nil
		}
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}

	// Check for symlinks and path traversal in a single walk
	res.AddFilesystemError(CheckFilesystem(path))

	// Parse frontmatter
	result, err := ParseSkillMD(content)
	if err != nil {
		res.AddFinding(skillMDError(RuleFrontmatterInvalid, fmt.Sprintf("invalid SKILL.md: %v", err)))
		return res, nil
	}

	for _, f := range validateFields(result, filepath.Base(path)) {
		res.AddFinding(f)
	}
	for _, f := range collectWarnings(result, content) {
		res.AddFinding(f)
	}
	for _, f := range newRefChecker(path, root, checkExec).checkReferences(result.Body, result.bodyLine) {
		res.AddFinding(f)
	}

	res.Valid = len(res.Errors) == 0
	return res, nil
}

// FilesystemError is returned by CheckFilesystem for a symlink or path
// traversal. Rule is RuleSymlinkNotAllowed or RulePathTraversal, and Path is
// the slash-separated offending path relative to the walked directory.
type FilesystemError struct {
	Rule string
	Path string
	msg  string
}

// Error implements error.
func (e *FilesystemError) Error() string { return e.msg }

// AddFilesystemError records err from CheckFilesystem as an error finding. A
// *FilesystemError keeps its rule and path; any other non-nil error is
// reported as a path traversal on the walked directory itself.
func (r *ValidationResult) AddFilesystemError(err error) {
	if err == nil {
		return
	}
	f := Finding{Severity: SeverityError, Rule: RulePathTraversal, File: ".", Message: err.Error()}
	var fsErr *FilesystemError
	if errors.As(err, &fsErr) {
		f.Rule = fsErr.Rule
		f.File = fsErr.Path
	}
	r.AddFinding(f)
}

// skillMDError returns an error finding located in SKILL.md.
func skillMDError(rule, msg string) Finding {
	return Finding{Severity: SeverityError, Rule: rule, File: SkillMDFile, Message: msg}
}

// validateFields checks parsed frontmatter fields against spec constraints.
func validateFields(result *ParseResult, dirName string) []Finding {
	var findings []Finding
	add := func(rule, msg string) { findings = append(findings, skillMDError(rule, msg)) }

	if result.Name == "" {
		add(RuleNameRequired, "name is required")
	} else {
		if err := ValidateSkillName(result.Name); err != nil {
			add(RuleNameInvalid, err.Error())
		}
		if result.Name != dirName {
			add(RuleNameDirMismatch,
				fmt.Sprintf("skill name %q must match directory name %q", result.Name, dirName))
		}
	}
	if result.Description == "" {
		add(RuleDescriptionRequired, "description is required")
	}
	if len(result.Description) > MaxDescriptionLength {
		add(RuleDescriptionTooLong,
			fmt.Sprintf("description exceeds maximum length of %d characters", MaxDescriptionLength))
	}
	if len(result.Compatibility) > MaxCompatibilityLength {
		add(RuleCompatibilityTooLong,
			fmt.Sprintf("compatibility field exceeds maximum length of %d characters", MaxCompatibilityLength))
	}

	return findings
}

// collectWarnings generates non-blocking warnings for spec compliance.
func collectWarnings(result *ParseResult, content []byte) []Finding {
	var findings []Finding
	add := func(rule, msg string) {
		findings = append(findings,
			Finding{Severity: SeverityWarning, Rule: rule, File: SkillMDFile, Message: msg})
	}

	if len(result.AllowedTools) > 0 && bytes.Contains(content, []byte(",")) &&
		bytes.Contains(content, []byte("allowed-tools:")) {
		add(RuleAllowedToolsComma,
			"allowed-tools uses comma-delimited format, which is a ToolHive extension; "+
				"the Agent Skills spec defines space-delimited as the canonical format")
	}
	lineCount := bytes.Count(content, []byte("\n")) + 1
	if lineCount > RecommendedMaxSkillMDLines {
		add(RuleSkillMDTooLong,
			fmt.Sprintf("SKILL.md has %d lines (recommended max: %d)", lineCount, RecommendedMaxSkillMDLines))
	}

	return findings
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

// CheckFilesystem walks the directory once, checking for symlinks and path
// traversal. A violation is returned as a *FilesystemError.
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
		slashRel := filepath.ToSlash(rel)
		for _, component := range strings.Split(slashRel, "/") {
			if component == ".." {
				return &FilesystemError{Rule: RulePathTraversal, Path: slashRel,
					msg: fmt.Sprintf("path traversal detected in %q: '..' components are not allowed", rel)}
			}
		}

		// Check for symlinks (WalkDir doesn't stat, so use Lstat)
		info, err := os.Lstat(p)
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &FilesystemError{Rule: RuleSymlinkNotAllowed, Path: slashRel,
				msg: fmt.Sprintf("symlink found at %q: symlinks are not allowed in skill directories", rel)}
		}

		return nil
	})
}
