// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import "github.com/stacklok/toolhive/pkg/skills"

// skillListResponse represents the response for listing skills.
//
//	@Description	Response containing a list of installed skills
type skillListResponse struct {
	// List of installed skills
	Skills []skills.InstalledSkill `json:"skills"`
}

// installSkillRequest represents the request to install a skill.
//
//	@Description	Request to install a skill
type installSkillRequest struct {
	// Name or OCI reference of the skill to install
	Name string `json:"name"`
	// Version to install (empty means latest)
	Version string `json:"version,omitempty"`
	// Scope for the installation
	Scope skills.Scope `json:"scope,omitempty"`
	// Client is the target client (e.g., "claude-code")
	Client string `json:"client,omitempty"`
	// Force allows overwriting unmanaged skill directories
	Force bool `json:"force,omitempty"`
}

// installSkillResponse represents the response after installing a skill.
//
//	@Description	Response after successfully installing a skill
type installSkillResponse struct {
	// The installed skill
	Skill skills.InstalledSkill `json:"skill"`
}

// validateSkillRequest represents the request to validate a skill.
//
//	@Description	Request to validate a skill definition
type validateSkillRequest struct {
	// Path to the skill definition directory
	Path string `json:"path"`
}

// buildSkillRequest represents the request to build a skill.
//
//	@Description	Request to build a skill from a local directory
type buildSkillRequest struct {
	// Path to the skill definition directory
	Path string `json:"path"`
	// OCI tag for the built artifact
	Tag string `json:"tag,omitempty"`
}

// pushSkillRequest represents the request to push a skill.
//
//	@Description	Request to push a built skill artifact
type pushSkillRequest struct {
	// OCI reference to push
	Reference string `json:"reference"`
}
