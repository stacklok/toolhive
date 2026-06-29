// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package plugins provides types and interfaces for managing ToolHive plugins
// (Claude Plugin manifest format, .claude-plugin/plugin.json).
//
// A plugin is an OCI artifact containing a .claude-plugin/plugin.json manifest
// and its component directories (commands, agents, skills, hooks). The package
// mirrors pkg/skills: the scoping model (user vs. project) and install-status
// lifecycle are identical, so Scope and InstallStatus are re-exported as type
// aliases from pkg/skills to avoid conversion churn at storage boundaries.
package plugins

import (
	"time"

	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// Scope is the installation scope for a plugin. It is an alias for
// skills.Scope because the scoping model is identical: a plugin is installed
// either user-wide or project-local, and the storage layer keys both on the
// same (scope, project_root) pair.
type Scope = skills.Scope

// InstallStatus is the lifecycle status of an installed plugin. Alias for
// skills.InstallStatus (installed | pending | failed).
type InstallStatus = skills.InstallStatus

const (
	// ScopeUser indicates a plugin installed user-wide.
	ScopeUser = skills.ScopeUser
	// ScopeProject indicates a plugin installed for a specific project.
	ScopeProject = skills.ScopeProject
)

// Install lifecycle statuses. Aliased from skills.InstallStatus because the
// lifecycle model is identical.
const (
	InstallStatusInstalled = skills.InstallStatusInstalled
	InstallStatusPending   = skills.InstallStatusPending
	InstallStatusFailed    = skills.InstallStatusFailed
)

// ValidateScope validates a plugin scope. Re-exported from skills because the
// rule (empty | "user" | "project") is identical.
var ValidateScope = skills.ValidateScope

// Dependency is an external plugin dependency (OCI reference). Alias for
// skills.Dependency; the {Name, Reference, Digest} shape is identical.
type Dependency = skills.Dependency

// PluginMetadata contains metadata about a plugin, drawn from the
// .claude-plugin/plugin.json manifest.
type PluginMetadata struct {
	// Name is the unique name of the plugin (kebab-case).
	Name string `json:"name"`
	// Version is the semantic version of the plugin.
	Version string `json:"version,omitempty"`
	// Description is a human-readable description of the plugin.
	Description string `json:"description,omitempty"`
	// Author is the plugin author or maintainer.
	Author string `json:"author,omitempty"`
	// License is the SPDX license identifier for the plugin.
	License string `json:"license,omitempty"`
	// Keywords is a list of keywords for categorization/search.
	Keywords []string `json:"keywords,omitempty"`
}

// InstalledPlugin represents a plugin that has been installed locally.
type InstalledPlugin struct {
	// Metadata contains the plugin's metadata.
	Metadata PluginMetadata `json:"metadata"`
	// Scope is the installation scope (user or project).
	Scope Scope `json:"scope"`
	// ProjectRoot is the project root path for project-scoped plugins. Empty for user-scoped.
	ProjectRoot string `json:"project_root,omitempty"`
	// Reference is the full OCI reference (e.g. ghcr.io/org/plugin:v1).
	Reference string `json:"reference,omitempty"`
	// Tag is the OCI tag (e.g. v1.0.0).
	Tag string `json:"tag,omitempty"`
	// Digest is the OCI digest (sha256:...) for upgrade detection.
	Digest string `json:"digest,omitempty"`
	// Status is the current installation status.
	Status InstallStatus `json:"status"`
	// InstalledAt is the timestamp when the plugin was installed.
	InstalledAt time.Time `json:"installed_at"`
	// Clients is the list of client identifiers the plugin is installed for.
	Clients []string `json:"clients,omitempty"`
	// Components is the inventory of component types declared by the plugin
	// (e.g. {"commands": 3, "skills": 2}). Extracted from the OCI artifact.
	Components ComponentInventory `json:"components,omitempty"`
	// Signature is the optional signing signature for the plugin artifact.
	Signature string `json:"signature,omitempty"`
	// Dependencies is the list of external plugin dependencies.
	Dependencies []Dependency `json:"dependencies,omitempty"`
}

// ComponentInventory summarizes the component types declared by a plugin
// (map of component-type name to count). Alias for the toolhive-core type.
type ComponentInventory = ociplugins.ComponentInventory

// PluginFileEntry represents a single file within a plugin artifact. Alias for
// skills.SkillFileEntry (same {Path, Size} shape).
type PluginFileEntry = skills.SkillFileEntry

// PluginContent contains the manifest body and file listing extracted from an
// OCI plugin artifact without installing it.
type PluginContent struct {
	// Name is the plugin name from the OCI config labels.
	Name string `json:"name"`
	// Description is the plugin description from the OCI config labels.
	Description string `json:"description,omitempty"`
	// Version is the plugin version from the OCI config labels.
	Version string `json:"version,omitempty"`
	// License is the SPDX license identifier from the OCI config labels.
	License string `json:"license,omitempty"`
	// Manifest is the raw .claude-plugin/plugin.json body.
	Manifest string `json:"manifest"`
	// Files is the list of all files in the artifact with their sizes.
	Files []PluginFileEntry `json:"files"`
}

// LocalBuild represents a locally-built OCI plugin artifact in the local store.
// Alias for skills.LocalBuild (identical shape: Tag, Digest, Name,
// Description, Version).
type LocalBuild = skills.LocalBuild

// ValidationResult contains the outcome of a Validate operation. Alias for
// skills.ValidationResult.
type ValidationResult = skills.ValidationResult
