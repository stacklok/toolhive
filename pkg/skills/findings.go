// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import "path"

// Severity classifies a validation finding.
type Severity string

const (
	// SeverityError marks a finding that makes the result invalid.
	SeverityError Severity = "error"
	// SeverityWarning marks a non-blocking finding.
	SeverityWarning Severity = "warning"
)

// Rule IDs reported in Finding.Rule. They are part of the public validation
// contract (CLI JSON output and API): a rule may be added, but an existing ID
// is never renamed or removed. Skills and plugins share IDs; Finding.File
// tells them apart.
const (
	RuleSkillMDMissing         = "skill-md-missing"
	RuleFrontmatterInvalid     = "frontmatter-invalid"
	RuleNameRequired           = "name-required"
	RuleNameInvalid            = "name-invalid"
	RuleNameDirMismatch        = "name-dir-mismatch"
	RuleDescriptionRequired    = "description-required"
	RuleDescriptionTooLong     = "description-too-long"
	RuleCompatibilityTooLong   = "compatibility-too-long"
	RuleSymlinkNotAllowed      = "symlink-not-allowed"
	RulePathTraversal          = "path-traversal"
	RuleManifestInvalid        = "manifest-invalid"
	RuleBundledSkillUnreadable = "bundled-skill-unreadable"
	RuleAllowedToolsComma      = "allowed-tools-comma"
	RuleSkillMDTooLong         = "skill-md-too-long"
)

// SkillMDFile is the Finding.File value for findings about a skill's SKILL.md.
const SkillMDFile = "SKILL.md"

// Finding is one structured validation result. Every entry in
// ValidationResult.Errors or ValidationResult.Warnings corresponds to exactly
// one Finding with the same severity.
type Finding struct {
	// Severity is "error" or "warning".
	Severity Severity `json:"severity" enums:"error,warning"`
	// Rule is the stable ID of the check that produced the finding.
	Rule string `json:"rule"`
	// File is the slash-separated path of the affected file, relative to the
	// validated directory.
	File string `json:"file"`
	// Line is the 1-based line in File, or 0 when unknown.
	Line int `json:"line"`
	// Message describes the problem.
	Message string `json:"message"`
}

// AddFinding records f in Findings and appends its message to Errors or
// Warnings according to its severity. It does not update Valid; callers set
// Valid once all findings are recorded.
func (r *ValidationResult) AddFinding(f Finding) {
	r.addFinding(f, f.Message)
}

// addFinding records f and appends legacy (the string reported in Errors or
// Warnings) according to f's severity.
func (r *ValidationResult) addFinding(f Finding, legacy string) {
	r.Findings = append(r.Findings, f)
	if f.Severity == SeverityError {
		r.Errors = append(r.Errors, legacy)
		return
	}
	r.Warnings = append(r.Warnings, legacy)
}

// AddPrefixedFindings copies every finding of other into r, joining dir in
// front of each finding's File and prefix in front of each legacy Errors or
// Warnings string. It is used to report a nested validation (a skill bundled
// in a plugin) relative to the outer directory.
func (r *ValidationResult) AddPrefixedFindings(other *ValidationResult, dir, prefix string) {
	for _, f := range other.Findings {
		f.File = path.Join(dir, f.File)
		r.addFinding(f, prefix+f.Message)
	}
}
