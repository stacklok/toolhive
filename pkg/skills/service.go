// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import "context"

//go:generate mockgen -destination=mocks/mock_service.go -package=mocks -source=service.go SkillService

// SkillService defines the interface for managing skills.
type SkillService interface {
	// List returns all installed skills matching the given options.
	List(ctx context.Context, opts ListOptions) ([]InstalledSkill, error)
	// Install installs a skill from a remote source.
	Install(ctx context.Context, opts InstallOptions) (*InstallResult, error)
	// Uninstall removes an installed skill.
	Uninstall(ctx context.Context, opts UninstallOptions) error
	// Info returns detailed information about a skill.
	Info(ctx context.Context, opts InfoOptions) (*SkillInfo, error)
	// Validate checks whether a skill definition is valid.
	Validate(ctx context.Context, path string) (*ValidationResult, error)
	// Build builds a skill from a local directory into an OCI artifact.
	Build(ctx context.Context, opts BuildOptions) (*BuildResult, error)
	// Push pushes a built skill artifact to a remote registry.
	Push(ctx context.Context, opts PushOptions) error
}
