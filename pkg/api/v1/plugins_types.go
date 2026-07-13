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
