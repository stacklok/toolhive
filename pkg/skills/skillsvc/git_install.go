// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/storage"
)

// gitInstallOpts extends InstallOptions with resolved git files.
type gitInstallOpts struct {
	skills.InstallOptions
	gitFiles []gitresolver.FileEntry
}

// installFromGit resolves a git:// reference, clones the repository, validates
// the skill, writes files to disk, and creates a DB record.
func (s *service) installFromGit(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
) (*skills.InstallResult, error) {
	if s.gitResolver == nil {
		return nil, httperr.WithCode(
			errors.New("git resolver is not configured"),
			http.StatusInternalServerError,
		)
	}
	if s.pathResolver == nil {
		return nil, httperr.WithCode(
			errors.New("path resolver is required for git installs"),
			http.StatusInternalServerError,
		)
	}

	resolvedOpts, err := s.resolveGitReference(ctx, &opts)
	if err != nil {
		return nil, err
	}

	unlock := s.locks.lock(resolvedOpts.Name, scope, resolvedOpts.ProjectRoot)
	defer unlock()

	clientType := s.resolveClient(resolvedOpts.Client)

	targetDir, pathErr := s.pathResolver.GetSkillPath(clientType, resolvedOpts.Name, scope, resolvedOpts.ProjectRoot)
	if pathErr != nil {
		return nil, fmt.Errorf("resolving skill path: %w", pathErr)
	}

	// Write files from git to the target directory.
	if writeErr := gitresolver.WriteFiles(resolvedOpts.gitFiles, targetDir, resolvedOpts.Force); writeErr != nil {
		return nil, fmt.Errorf("writing skill files: %w", writeErr)
	}

	return s.upsertGitSkill(ctx, *resolvedOpts, scope, clientType, targetDir)
}

// resolveGitReference parses a git:// reference, clones the repo, validates
// the skill, and hydrates install options from the resolved content.
func (s *service) resolveGitReference(ctx context.Context, opts *skills.InstallOptions) (*gitInstallOpts, error) {
	originalRef := opts.Name

	gitRef, err := gitresolver.ParseGitReference(opts.Name)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("invalid git reference %q: %w", opts.Name, err),
			http.StatusBadRequest,
		)
	}

	resolved, err := s.gitResolver.Resolve(ctx, gitRef)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("resolving git skill: %w", err),
			http.StatusBadGateway,
		)
	}

	// Supply chain defense: the declared skill name must match what the reference expects.
	expectedName := gitRef.SkillName()
	if resolved.SkillConfig.Name != expectedName {
		return nil, httperr.WithCode(
			fmt.Errorf(
				"skill name %q in SKILL.md does not match expected name %q from git reference",
				resolved.SkillConfig.Name, expectedName,
			),
			http.StatusUnprocessableEntity,
		)
	}

	result := &gitInstallOpts{
		InstallOptions: *opts,
		gitFiles:       resolved.Files,
	}
	result.Name = resolved.SkillConfig.Name
	result.Digest = resolved.CommitHash
	result.Reference = originalRef
	if result.Version == "" && resolved.SkillConfig.Version != "" {
		result.Version = resolved.SkillConfig.Version
	}

	return result, nil
}

// upsertGitSkill creates or updates a DB record for a git-installed skill.
func (s *service) upsertGitSkill(
	ctx context.Context,
	opts gitInstallOpts,
	scope skills.Scope,
	clientType, targetDir string,
) (*skills.InstallResult, error) {
	existing, storeErr := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	isNotFound := errors.Is(storeErr, storage.ErrNotFound)

	switch {
	case storeErr != nil && !isNotFound:
		return nil, fmt.Errorf("checking existing skill: %w", storeErr)

	case storeErr == nil && existing.Digest == opts.Digest:
		// Same commit hash — already installed, no-op.
		return &skills.InstallResult{Skill: existing}, nil

	case storeErr == nil:
		// Different commit — upgrade.
		sk := buildInstalledSkill(opts.InstallOptions, scope, clientType, existing.Clients)
		if err := s.store.Update(ctx, sk); err != nil {
			_ = s.installer.Remove(targetDir) // rollback
			return nil, err
		}
		return &skills.InstallResult{Skill: sk}, nil

	default:
		// Not found — fresh install.
		sk := buildInstalledSkill(opts.InstallOptions, scope, clientType, nil)
		if err := s.store.Create(ctx, sk); err != nil {
			_ = s.installer.Remove(targetDir) // rollback
			return nil, err
		}
		return &skills.InstallResult{Skill: sk}, nil
	}
}
