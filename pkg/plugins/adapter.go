// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import "context"

//go:generate mockgen -destination=mocks/mock_adapter.go -package=mocks -source=adapter.go MaterializationAdapter

// ComponentType enumerates plugin component classes. The string values match
// the keys of ociplugins.ComponentInventory (commands/agents/skills/hooks/
// mcpServers/lspServers).
type ComponentType string

// Component type constants. The string values match the keys of
// ociplugins.ComponentInventory (commands/agents/skills/hooks/mcpServers/
// lspServers).
const (
	ComponentCommands ComponentType = "commands"
	ComponentAgents   ComponentType = "agents"
	ComponentSkills   ComponentType = "skills"
	ComponentHooks    ComponentType = "hooks"
	ComponentMCP      ComponentType = "mcpServers"
	ComponentLSP      ComponentType = "lspServers"
)

// MaterializeRequest carries everything an adapter needs to install a plugin
// into a target client's directory layout.
type MaterializeRequest struct {
	// Name is the plugin name (kebab-case).
	Name string
	// LayerData is the OCI tar.gz layer containing the whole plugin tree
	// (commands/, agents/, skills/, hooks/, .claude-plugin/). Produced by
	// OCI pull or built in-memory from a git clone.
	LayerData []byte
	// Scope is the installation scope (user or project).
	Scope Scope
	// ProjectRoot is the project root path for project-scoped installs.
	// Empty for user-scoped.
	ProjectRoot string
	// Components is the plugin's component inventory, used to warn on
	// component types the adapter does not materialize (e.g. Codex drops
	// commands/agents).
	Components ComponentInventory
}

// DematerializeRequest carries everything an adapter needs to revert a plugin
// installation. It mirrors MaterializeRequest so the two sides of the seam stay
// symmetric — the first client that needs more context to revert cleanly gets it
// without forcing an interface break.
type DematerializeRequest struct {
	// Name is the plugin name (kebab-case).
	Name string
	// Scope is the installation scope (user or project).
	Scope Scope
	// ProjectRoot is the project root path for project-scoped installs.
	// Empty for user-scoped.
	ProjectRoot string
}

// MaterializeResult reports what was written and what was deliberately dropped.
type MaterializeResult struct {
	// InstalledComponents lists the component types the adapter materialized.
	InstalledComponents []ComponentType
	// DroppedComponents lists component types the plugin declares that this
	// adapter does NOT materialize.
	DroppedComponents []ComponentType
	// InstallPath is the root directory the adapter wrote (informational).
	InstallPath string
	// ProjectScopeDegraded is true when a project-scope install degrades
	// because the target client only supports user-scope materialization.
	ProjectScopeDegraded bool
}

// ScopeSupport lets Info report project-scope degradation without re-running
// Materialize. A single client-wide boolean can't express "degrades only for
// these components" or carry a reason, so the descriptor generalizes the former
// DegradesOnProjectScope() bool.
type ScopeSupport struct {
	// DegradesOnProjectScope is true when a project-scoped install degrades
	// for this client (typically because the adapter mutates a user-scoped
	// config file).
	DegradesOnProjectScope bool
	// Reason is an optional human-readable explanation intended for future
	// surfacing via Info (not yet consumed by Info — it currently reads only
	// DegradesOnProjectScope). Empty when the client does not degrade.
	Reason string
}

// MaterializationAdapter materializes a plugin into a target client's layout
// and reverts its own mutations. It generalizes skills.PathResolver: instead
// of resolving a single skill path, it owns extraction + (optional) config
// mutation for a multi-component plugin tree, because the materialization
// strategy differs per client (Claude Code = pure filesystem; Codex = FS
// cache + TOML mutation).
type MaterializationAdapter interface {
	// Materialize extracts the plugin into the client's directory layout and,
	// for config-based clients, mutates the client config. Must be idempotent
	// for re-installs under the same (name, scope, projectRoot) — a force
	// reinstall overwrites the prior install.
	Materialize(ctx context.Context, req MaterializeRequest) (*MaterializeResult, error)
	// Dematerialize removes the plugin's filesystem footprint and reverts any
	// config mutations the adapter itself made. Must be idempotent: a missing
	// install is not an error.
	Dematerialize(ctx context.Context, req DematerializeRequest) error
	// SupportedComponents returns the component types this adapter loads.
	SupportedComponents() []ComponentType
	// ScopeSupport reports whether a project-scoped install degrades for this
	// client — i.e. the adapter can only materialize at user scope (typically
	// because it mutates a user-scoped config file). The returned struct also
	// carries an optional reason (not yet consumed by Info). Used by Info to
	// surface ProjectScopeDegradedClients without re-running Materialize.
	ScopeSupport() ScopeSupport
}
