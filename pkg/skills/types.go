// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skills provides types and interfaces for managing ToolHive skills.
package skills

import "time"

// Scope represents the scope at which a skill is installed.
type Scope string

const (
	// ScopeUser indicates a skill installed for the current user.
	ScopeUser Scope = "user"
	// ScopeSystem indicates a skill installed system-wide.
	ScopeSystem Scope = "system"
)

// InstallStatus represents the current status of a skill installation.
type InstallStatus string

const (
	// InstallStatusInstalled indicates a skill is fully installed and ready.
	InstallStatusInstalled InstallStatus = "installed"
	// InstallStatusPending indicates a skill installation is in progress.
	InstallStatusPending InstallStatus = "pending"
	// InstallStatusFailed indicates a skill installation has failed.
	InstallStatusFailed InstallStatus = "failed"
)

// SkillMetadata contains metadata about a skill.
type SkillMetadata struct {
	// Name is the unique name of the skill.
	Name string `json:"name"`
	// Version is the semantic version of the skill.
	Version string `json:"version"`
	// Description is a human-readable description of the skill.
	Description string `json:"description"`
	// Author is the skill author or maintainer.
	Author string `json:"author"`
	// Tags is a list of tags for categorization.
	Tags []string `json:"tags,omitempty"`
}

// InstalledSkill represents a skill that has been installed locally.
type InstalledSkill struct {
	// Metadata contains the skill's metadata.
	Metadata SkillMetadata `json:"metadata"`
	// Scope is the installation scope (user or system).
	Scope Scope `json:"scope"`
	// Status is the current installation status.
	Status InstallStatus `json:"status"`
	// InstalledAt is the timestamp when the skill was installed.
	InstalledAt time.Time `json:"installed_at"`
	// Clients is the list of client identifiers the skill is installed for.
	// TODO: Refactor client.MCPClient to a shared package so it can be used here instead of []string.
	Clients []string `json:"clients,omitempty"`
}

// SkillIndexEntry represents a single skill entry in a remote skill index.
type SkillIndexEntry struct {
	// Metadata contains the skill's metadata.
	Metadata SkillMetadata `json:"metadata"`
	// Repository is the OCI repository reference for the skill.
	Repository string `json:"repository"`
}

// SkillIndex represents a collection of available skills from a remote index.
type SkillIndex struct {
	// Skills is the list of available skills.
	Skills []SkillIndexEntry `json:"skills"`
}
