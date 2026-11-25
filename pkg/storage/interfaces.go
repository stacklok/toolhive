// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package storage provides domain-specific storage interfaces for ToolHive.
package storage

import (
	"context"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

//go:generate mockgen -destination=mocks/mock_skill_store.go -package=mocks -source=interfaces.go
// Note: -source mode generates mocks for ALL interfaces in this file
// (SkillStore and PluginStore) into mock_skill_store.go. The interface-name
// argument to -source is ignored; both mocks land in the same file.

// SkillStore defines the interface for managing skill persistence.
type SkillStore interface {
	// Create stores a new installed skill.
	Create(ctx context.Context, skill skills.InstalledSkill) error
	// Get retrieves an installed skill by name, scope, and project root.
	Get(ctx context.Context, name string, scope skills.Scope, projectRoot string) (skills.InstalledSkill, error)
	// List returns all installed skills matching the given filter.
	List(ctx context.Context, filter ListFilter) ([]skills.InstalledSkill, error)
	// Update modifies an existing installed skill.
	Update(ctx context.Context, skill skills.InstalledSkill) error
	// Delete removes an installed skill by name, scope, and project root.
	Delete(ctx context.Context, name string, scope skills.Scope, projectRoot string) error
	// Close releases any resources held by the store.
	Close() error
}

// PluginStore defines the interface for managing plugin persistence. Mirrors
// SkillStore; the two share the ListFilter type and the entries table but
// store plugin-specific rows in installed_plugins/plugin_dependencies.
type PluginStore interface {
	// Create stores a new installed plugin.
	Create(ctx context.Context, plugin plugins.InstalledPlugin) error
	// Get retrieves an installed plugin by name, scope, and project root.
	Get(ctx context.Context, name string, scope plugins.Scope, projectRoot string) (plugins.InstalledPlugin, error)
	// List returns all installed plugins matching the given filter.
	List(ctx context.Context, filter ListFilter) ([]plugins.InstalledPlugin, error)
	// Update modifies an existing installed plugin.
	Update(ctx context.Context, plugin plugins.InstalledPlugin) error
	// Delete removes an installed plugin by name, scope, and project root.
	Delete(ctx context.Context, name string, scope plugins.Scope, projectRoot string) error
	// Close releases any resources held by the store.
	Close() error
}

// ListFilter configures filtering for List operations. Shared by SkillStore
// and PluginStore (the scope/project-root/client-app filtering shape is
// identical; ListFilter.Scope is skills.Scope, which plugins.Scope aliases).
type ListFilter struct {
	// Scope filters by installation scope. Empty matches all scopes.
	Scope skills.Scope
	// ProjectRoot filters by project root path. Empty matches all projects.
	ProjectRoot string
	// ClientApp filters by client application. Empty matches all clients.
	ClientApp string
}
