// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package adapters contains MaterializationAdapter implementations for
// each MCP client that supports plugins.
package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// ClaudeCodeAdapter materializes plugins into Claude Code's
// ~/.claude/plugins/<name> directory. Claude Code loads commands, agents,
// skills, and hooks from the plugin tree — no config-file mutation is needed.
type ClaudeCodeAdapter struct {
	cm        *client.ClientManager
	installer skills.Installer
}

// NewClaudeCodeAdapter returns a ClaudeCodeAdapter backed by the given
// ClientManager and a production skills.Installer.
func NewClaudeCodeAdapter(cm *client.ClientManager) *ClaudeCodeAdapter {
	return &ClaudeCodeAdapter{
		cm:        cm,
		installer: skills.NewInstaller(),
	}
}

var claudeCodeSupported = []plugins.ComponentType{
	plugins.ComponentCommands,
	plugins.ComponentAgents,
	plugins.ComponentSkills,
	plugins.ComponentHooks,
}

// Materialize extracts the plugin layer into the Claude Code plugins directory.
func (a *ClaudeCodeAdapter) Materialize(_ context.Context, req plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
	dir, err := a.cm.GetPluginPath(client.ClaudeCode, req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving plugin path: %w", err)
	}

	if _, err := a.installer.Extract(req.LayerData, dir, true); err != nil {
		return nil, fmt.Errorf("extracting plugin: %w", err)
	}

	return &plugins.MaterializeResult{
		InstalledComponents:  claudeCodeSupported,
		DroppedComponents:    droppedComponents(req.Components, claudeCodeSupported),
		InstallPath:          dir,
		ProjectScopeDegraded: false,
	}, nil
}

// Dematerialize removes the plugin directory and cleans up empty parents.
func (a *ClaudeCodeAdapter) Dematerialize(_ context.Context, req plugins.DematerializeRequest) error {
	dir, err := a.cm.GetPluginPath(client.ClaudeCode, req.Name, req.Scope, req.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolving plugin path: %w", err)
	}

	if err := a.installer.Remove(dir); err != nil {
		return fmt.Errorf("removing plugin directory: %w", err)
	}

	// Best-effort empty-parent cleanup.
	// Mirror the skillsvc uninstall pattern: walk up to the home/project root.
	stopAt := req.ProjectRoot
	if req.Scope == plugins.ScopeUser {
		if homeDir, homeErr := os.UserHomeDir(); homeErr == nil {
			stopAt = homeDir
		}
	}
	if stopAt != "" {
		skills.RemoveEmptyParents(filepath.Dir(dir), stopAt)
	}

	return nil
}

// SupportedComponents returns the component types Claude Code loads.
func (*ClaudeCodeAdapter) SupportedComponents() []plugins.ComponentType {
	return claudeCodeSupported
}

// ScopeSupport returns false for Claude Code: it supports both user and
// project plugin directories, so a project-scoped install lands in the project
// directory without degradation.
func (*ClaudeCodeAdapter) ScopeSupport() plugins.ScopeSupport {
	return plugins.ScopeSupport{DegradesOnProjectScope: false}
}
