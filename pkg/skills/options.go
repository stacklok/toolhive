// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

// ListOptions configures the behavior of the List operation.
type ListOptions struct {
	// Scope filters results by installation scope.
	Scope Scope `json:"scope,omitempty"`
}

// InstallOptions configures the behavior of the Install operation.
type InstallOptions struct {
	// Name is the skill name or OCI reference to install.
	Name string `json:"name"`
	// Version is the specific version to install. Empty means latest.
	Version string `json:"version,omitempty"`
	// Scope is the installation scope.
	Scope Scope `json:"scope,omitempty"`
}

// InstallResult contains the outcome of an Install operation.
type InstallResult struct {
	// Skill is the installed skill.
	Skill InstalledSkill `json:"skill"`
}

// UninstallOptions configures the behavior of the Uninstall operation.
type UninstallOptions struct {
	// Name is the skill name to uninstall.
	Name string `json:"name"`
	// Scope is the scope from which to uninstall.
	Scope Scope `json:"scope,omitempty"`
}

// InfoOptions configures the behavior of the Info operation.
type InfoOptions struct {
	// Name is the skill name to look up.
	Name string `json:"name"`
}

// SkillInfo contains detailed information about a skill.
type SkillInfo struct {
	// Metadata contains the skill's metadata.
	Metadata SkillMetadata `json:"metadata"`
	// Installed indicates whether the skill is currently installed.
	Installed bool `json:"installed"`
	// InstalledSkill is set if the skill is installed.
	InstalledSkill *InstalledSkill `json:"installed_skill,omitempty"`
}

// ValidationResult contains the outcome of a Validate operation.
type ValidationResult struct {
	// Valid indicates whether the skill definition is valid.
	Valid bool `json:"valid"`
	// Errors is a list of validation errors, if any.
	Errors []string `json:"errors,omitempty"`
	// Warnings is a list of non-blocking validation warnings, if any.
	Warnings []string `json:"warnings,omitempty"`
}

// BuildOptions configures the behavior of the Build operation.
type BuildOptions struct {
	// Path is the local directory path containing the skill definition.
	Path string `json:"path"`
	// Tag is the OCI tag to use for the built artifact.
	Tag string `json:"tag,omitempty"`
}

// BuildResult contains the outcome of a Build operation.
type BuildResult struct {
	// Reference is the OCI reference of the built skill artifact.
	Reference string `json:"reference"`
}

// PushOptions configures the behavior of the Push operation.
type PushOptions struct {
	// Reference is the OCI reference to push.
	Reference string `json:"reference"`
}
