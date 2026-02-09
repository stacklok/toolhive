// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skills provides types and interfaces for managing ToolHive skills.
package skills

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

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

// StringOrSlice is a custom type that can unmarshal from either a YAML string
// (space-delimited per spec, or comma-delimited for compatibility) or a YAML array.
type StringOrSlice []string

// UnmarshalYAML implements yaml.Unmarshaler for StringOrSlice.
// Per the Agent Skills spec, allowed-tools is space-delimited, but we also
// support comma-delimited for compatibility with existing skills.
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		str := value.Value
		if str == "" {
			*s = nil
			return nil
		}

		// Delimiter precedence: if any comma is present, split on commas;
		// otherwise split on whitespace (space-delimited is the canonical
		// format per the Agent Skills spec). This means a mixed-delimiter
		// string like "Read,Glob Grep" splits on comma, yielding
		// ["Read", "Glob Grep"] — comma takes priority.
		var parts []string
		if strings.Contains(str, ",") {
			parts = strings.Split(str, ",")
		} else {
			parts = strings.Fields(str)
		}

		result := make([]string, 0, len(parts))
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
		*s = result
		return nil
	case yaml.SequenceNode:
		var arr []string
		if err := value.Decode(&arr); err != nil {
			return fmt.Errorf("decoding allowed-tools array: %w", err)
		}
		*s = arr
		return nil
	case yaml.DocumentNode, yaml.MappingNode, yaml.AliasNode:
		return fmt.Errorf("allowed-tools: expected string or array, got unsupported YAML node type")
	}
	return fmt.Errorf("allowed-tools: unexpected YAML node kind %d", value.Kind)
}

// SkillFrontmatter represents the raw YAML frontmatter from a SKILL.md file.
type SkillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	Version       string            `yaml:"version,omitempty"`
	AllowedTools  StringOrSlice     `yaml:"allowed-tools,omitempty"`
	Requires      StringOrSlice     `yaml:"toolhive.requires,omitempty"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
}

// Dependency represents an external skill dependency (OCI reference).
type Dependency struct {
	Reference string `json:"reference"`
}

// ParseResult contains the parsed contents of a SKILL.md file.
type ParseResult struct {
	Name          string
	Description   string
	Version       string
	AllowedTools  []string
	License       string
	Compatibility string
	Metadata      map[string]string
	Requires      []Dependency
	Body          []byte
}

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
	// TODO: Refactor client.ClientApp to a shared package so it can be used here instead of []string.
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
