// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

// SkillPackage represents a distribution package (OCI or Git) in request/response payloads.
type SkillPackage struct {
	// RegistryType is the type of registry the package is from.
	// Can be "oci" or "git".
	RegistryType string `json:"registryType"` // "oci" or "git"
	// Identifier is the OCI identifier of the package.
	Identifier string `json:"identifier,omitempty"`
	// Digest is the digest of the package.
	Digest string `json:"digest,omitempty"`
	// MediaType is the media type of the package.
	MediaType string `json:"mediaType,omitempty"`
	// URL is the URL of the package.
	URL string `json:"url,omitempty"`
	// Ref is the reference of the package.
	Ref string `json:"ref,omitempty"`
	// Commit is the commit of the package.
	Commit string `json:"commit,omitempty"`
	// Subfolder is the subfolder of the package.
	Subfolder string `json:"subfolder,omitempty"`
}

// SkillIcon represents a display icon for a skill.
type SkillIcon struct {
	// Src is the source of the icon.
	Src string `json:"src"`
	// Size is the size of the icon.
	Size string `json:"size,omitempty"`
	// Type is the type of the icon.
	Type string `json:"type,omitempty"`
	// Label is the label of the icon.
	Label string `json:"label,omitempty"`
}

// SkillRepository represents source repository metadata.
type SkillRepository struct {
	// URL is the URL of the repository.
	URL string `json:"url,omitempty"`
	// Type is the type of the repository.
	Type string `json:"type,omitempty"`
}

// Skill is a single skill in list and get responses or publish requests.
type Skill struct {
	// Namespace is the namespace of the skill.
	// The format is reverse-DNS, e.g. "io.github.user".
	Namespace string `json:"namespace"`
	// Name is the name of the skill.
	// The format is that of identifiers, e.g. "my-skill".
	Name string `json:"name"`
	// Description is the description of the skill.
	Description string `json:"description"`
	// Version is the version of the skill.
	// Any non-empty string is valid, but ideally it should be either a
	// semantic version or a commit hash.
	Version string `json:"version"`
	// Status is the status of the skill.
	// Can be one of "active", "deprecated", or "archived".
	Status string `json:"status,omitempty"` // active, deprecated, archived
	// Title is the title of the skill.
	// This is for human consumption, not an identifier.
	Title string `json:"title,omitempty"`
	// License is the SPDX license identifier of the skill.
	License string `json:"license,omitempty"`
	// Compatibility is the environment requirements of the skill.
	Compatibility string `json:"compatibility,omitempty"`
	// AllowedTools is the list of tools that the skill is compatible with.
	// This is experimental.
	AllowedTools []string `json:"allowedTools,omitempty"`
	// Repository is the source repository of the skill.
	Repository *SkillRepository `json:"repository,omitempty"`
	// Icons is the list of icons for the skill.
	Icons []SkillIcon `json:"icons,omitempty"`
	// Packages is the list of packages for the skill.
	Packages []SkillPackage `json:"packages,omitempty"`
	// Metadata is the official metadata of the skill as reported in the
	// SKILL.md file.
	Metadata map[string]any `json:"metadata,omitempty"`
	// Meta is an opaque payload with extended meta data details of the skill.
	Meta map[string]any `json:"_meta,omitempty"`
}
