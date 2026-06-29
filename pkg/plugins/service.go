// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import "context"

//go:generate mockgen -destination=mocks/mock_service.go -package=mocks -source=service.go PluginService

// PluginService declares the plugin lifecycle surface (mirrors skills.SkillService).
//
// Phase 2 implements ONLY: Validate, Build, Push, ListBuilds, DeleteBuild, and
// GetContent — the build/validate/push/content surface. The install/uninstall/
// list/info methods are NOT declared on the interface yet: they land in Phase 3
// (#5527), which will widen this interface (the only change is the addition;
// existing method signatures stay stable). The Phase-3 option/result types they
// will use (InstallOptions, InstallResult, UninstallOptions, InfoOptions,
// PluginInfo, ListOptions) are already declared in options.go as forward
// declarations of the Phase-3 contract.
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
}
