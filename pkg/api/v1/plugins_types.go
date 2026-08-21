// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import "github.com/stacklok/toolhive/pkg/plugins"

// pluginListResponse represents the response for listing plugins.
//
//	@Description	Response containing a list of installed plugins
type pluginListResponse struct {
	// List of installed plugins
	Plugins []plugins.InstalledPlugin `json:"plugins"`
}

// installPluginRequest represents the request to install a plugin.
//
//	@Description	Request to install a plugin
type installPluginRequest struct {
	// Name or OCI reference of the plugin to install
	Name string `json:"name"`
	// Version to install (empty means latest)
	Version string `json:"version,omitempty"`
	// Scope for the installation
	Scope plugins.Scope `json:"scope,omitempty"`
	// ProjectRoot is the project root path for project-scoped installs
	ProjectRoot string `json:"project_root,omitempty"`
	// Clients lists target client identifiers (e.g., "claude-code"),
	// or ["all"] to target every plugin-supporting client.
	// Omitting this field installs to all available clients.
	Clients []string `json:"clients,omitempty"`
	// Force allows overwriting unmanaged plugin directories
	Force bool `json:"force,omitempty"`
	// Group is the group name to add the plugin to after installation
	Group string `json:"group,omitempty"`
}

// installPluginResponse represents the response after installing a plugin.
//
//	@Description	Response after successfully installing a plugin
type installPluginResponse struct {
	// The installed plugin
	Plugin plugins.InstalledPlugin `json:"plugin"`
}

// validatePluginRequest represents the request to validate a plugin.
//
//	@Description	Request to validate a plugin definition
type validatePluginRequest struct {
	// Path to the plugin definition directory
	Path string `json:"path"`
}

// buildPluginRequest represents the request to build a plugin.
//
//	@Description	Request to build a plugin from a local directory
type buildPluginRequest struct {
	// Path to the plugin definition directory
	Path string `json:"path"`
	// OCI tag for the built artifact
	Tag string `json:"tag,omitempty"`
}

// pushPluginRequest represents the request to push a plugin.
//
//	@Description	Request to push a built plugin artifact
type pushPluginRequest struct {
	// OCI reference to push
	Reference string `json:"reference"`
}

// pluginBuildListResponse represents the response for listing locally-built OCI plugin artifacts.
//
//	@Description	Response containing a list of locally-built OCI plugin artifacts
type pluginBuildListResponse struct {
	// List of locally-built OCI plugin artifacts
	Builds []plugins.LocalBuild `json:"builds"`
}

// syncPluginsRequest represents the request to sync a project's plugins.
//
//	@Description	Request to restore a project's installed plugins to match its lock file
type syncPluginsRequest struct {
	// ProjectRoot is the project root path whose lock file should be synced
	ProjectRoot string `json:"project_root"`
	// Clients lists target client identifiers. Empty means every
	// plugin-supporting client detected on this host.
	Clients []string `json:"clients,omitempty"`
	// Prune removes project-scoped plugins installed but not present in the lock file
	Prune bool `json:"prune,omitempty"`
	// Check verifies on-disk content against the lock file without installing or writing anything
	Check bool `json:"check,omitempty"`
	// Adopt writes lock entries for existing unmanaged project-scope installs
	Adopt bool `json:"adopt,omitempty"`
}

// upgradePluginsRequest represents the request to upgrade a project's plugins.
//
//	@Description	Request to re-resolve a project's lock entries and install newer content
type upgradePluginsRequest struct {
	// ProjectRoot is the project root path whose lock file should be upgraded
	ProjectRoot string `json:"project_root"`
	// Names restricts the upgrade to specific plugin names. Empty means every entry.
	Names []string `json:"names,omitempty"`
	// Preview reports what would change without installing (still fetches to compare digests)
	Preview bool `json:"preview,omitempty"`
	// FailOnChanges exits with an error when any mutable source would upgrade
	FailOnChanges bool `json:"fail_on_changes,omitempty"`
	// AllowRefChange permits resolvedReference changes during upgrade
	AllowRefChange bool `json:"allow_ref_change,omitempty"`
	// Clients lists target client identifiers. Empty means every
	// plugin-supporting client detected on this host.
	Clients []string `json:"clients,omitempty"`
}
