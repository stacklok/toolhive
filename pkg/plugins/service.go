// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import "context"

//go:generate mockgen -destination=mocks/mock_service.go -package=mocks -source=service.go PluginService

// PluginService declares the plugin lifecycle surface (mirrors skills.SkillService).
//
// Phase 3 widens the Phase-2 surface with Install, Uninstall, List, and Info —
// the install/materialization lifecycle. The build/validate/push/content methods
// from Phase 2 stay stable; this is purely additive.
type PluginService interface {
	// Validate checks whether a plugin definition is valid.
	Validate(ctx context.Context, path string) (*ValidationResult, error)
	// Build builds a plugin from a local directory into an OCI artifact.
	Build(ctx context.Context, opts BuildOptions) (*BuildResult, error)
	// Push pushes a built plugin artifact to a remote registry.
	Push(ctx context.Context, opts PushOptions) error
	// ListBuilds returns all locally-built OCI plugin artifacts in the local store.
	ListBuilds(ctx context.Context) ([]LocalBuild, error)
	// DeleteBuild removes a locally-built OCI plugin artifact from the local store.
	DeleteBuild(ctx context.Context, tag string) error
	// GetContent retrieves the plugin.json body and file listing from an OCI
	// artifact without installing it. Works for both remote registry references
	// and local build tags.
	GetContent(ctx context.Context, opts ContentOptions) (*PluginContent, error)
	// Install resolves a plugin reference (OCI, git, or registry name), extracts
	// it, materializes it into each target client's directory layout via the
	// configured MaterializationAdapters, and persists the install record.
	Install(ctx context.Context, opts InstallOptions) (*InstallResult, error)
	// Uninstall removes a plugin from all target clients (dematerialize) and
	// deletes the install record. Idempotent for already-removed plugins.
	Uninstall(ctx context.Context, opts UninstallOptions) error
	// List returns installed plugins, optionally filtered by scope, client, or
	// group membership.
	List(ctx context.Context, opts ListOptions) ([]InstalledPlugin, error)
	// Info returns details for a single installed plugin, including the
	// component types each installed client adapter does NOT load.
	Info(ctx context.Context, opts InfoOptions) (*PluginInfo, error)
}
