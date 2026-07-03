// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import "github.com/stacklok/toolhive/pkg/skills"

// ListOptions configures the behavior of the List operation. Alias for
// skills.ListOptions (identical shape: Scope, ClientApp, ProjectRoot, Group).
type ListOptions = skills.ListOptions

// InstallOptions configures the behavior of the Install operation. Mirrors
// skills.InstallOptions with the plugin-specific addition of Reference/Digest
// passthrough fields used by install-from-OCI flows.
type InstallOptions struct {
	// Name is the plugin name or OCI reference to install.
	Name string `json:"name"`
	// Version is the specific version to install. Empty means latest.
	Version string `json:"version,omitempty"`
	// Scope is the installation scope.
	Scope Scope `json:"scope,omitempty"`
	// Clients lists target clients (e.g., "claude-code").
	Clients []string `json:"clients,omitempty"`
	// Force allows overwriting unmanaged plugin directories.
	Force bool `json:"force,omitempty"`
	// ProjectRoot is the project root path for project-scoped installs.
	ProjectRoot string `json:"project_root,omitempty"`
	// Group is the group name to add the plugin to after installation.
	Group string `json:"group,omitempty"`
	// LayerData is the tar.gz content from an OCI layer. Internal use only — NOT exposed via HTTP API.
	LayerData []byte `json:"-"`
	// Reference is the full OCI reference (e.g. ghcr.io/org/plugin:v1).
	Reference string `json:"-"`
	// Digest is the OCI digest for upgrade detection.
	Digest string `json:"-"`
	// Components is the plugin's component inventory, hydrated from the OCI
	// artifact config by install-from-OCI/git flows. Internal use only.
	Components ComponentInventory `json:"-"`
	// Dependencies is the plugin's external dependency list, hydrated from the
	// OCI artifact config. Internal use only.
	Dependencies []Dependency `json:"-"`
	// Tag is the OCI tag, hydrated from the resolved reference. Internal use only.
	Tag string `json:"-"`
	// Description is the plugin description, hydrated from the OCI artifact
	// config or git manifest. Internal use only.
	Description string `json:"-"`
}

// InstallResult contains the outcome of an Install operation.
type InstallResult struct {
	// Plugin is the installed plugin.
	Plugin InstalledPlugin `json:"plugin"`
}

// UninstallOptions configures the behavior of the Uninstall operation. Alias
// for skills.UninstallOptions (identical shape).
type UninstallOptions = skills.UninstallOptions

// InfoOptions configures the behavior of the Info operation. Alias for
// skills.InfoOptions.
type InfoOptions = skills.InfoOptions

// PluginInfo contains detailed information about an installed plugin.
type PluginInfo struct {
	// Metadata contains the plugin's metadata.
	Metadata PluginMetadata `json:"metadata"`
	// InstalledPlugin contains the full installation record.
	InstalledPlugin *InstalledPlugin `json:"installed_plugin,omitempty"`
	// UnmaterializedComponents lists, per client type, the component types the
	// plugin declares that the installed client adapter does NOT load. Populated
	// by Info by diffing InstalledPlugin.Components against each installed
	// client adapter's SupportedComponents.
	UnmaterializedComponents map[string][]ComponentType `json:"unmaterialized_components,omitempty"`
	// ProjectScopeDegradedClients lists the client types for which a
	// project-scoped install degraded (the adapter could only materialize at
	// user scope — e.g. Codex always writes to the user-scoped config.toml).
	// Populated by Info; empty for user-scoped installs. Recomputed at read
	// time from the stored scope + each adapter's capability, mirroring the
	// UnmaterializedComponents pattern (no persistence needed — the degradation
	// is deterministic from scope + client type).
	ProjectScopeDegradedClients []string `json:"project_scope_degraded_clients,omitempty"`
}

// ContentOptions configures the behavior of the GetContent operation. Alias
// for skills.ContentOptions.
type ContentOptions = skills.ContentOptions

// BuildOptions configures the behavior of the Build operation. Alias for
// skills.BuildOptions (Path, Tag).
type BuildOptions = skills.BuildOptions

// BuildResult contains the outcome of a Build operation. Alias for
// skills.BuildResult (Reference).
type BuildResult = skills.BuildResult

// PushOptions configures the behavior of the Push operation. Alias for
// skills.PushOptions (Reference).
type PushOptions = skills.PushOptions
