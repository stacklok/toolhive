// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
)

// Uninstall removes an installed skill and cleans up files for all clients.
// For project scope, it updates the lock file and cascade-uninstalls orphaned deps.
func (s *service) Uninstall(ctx context.Context, opts skills.UninstallOptions) error {
	_, err := s.uninstallInternal(ctx, opts, true)
	return err
}

func (s *service) uninstallInternal(
	ctx context.Context, opts skills.UninstallOptions, allowCascade bool,
) ([]string, error) {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	scope = defaultScope(scope)
	opts.ProjectRoot = projectRoot

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	existing, err := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}

	cleanupErrs := s.cleanupSkillFiles(existing, opts.Name, scope, opts.ProjectRoot)

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		return nil, err
	}

	cascaded, lockErrs := s.uninstallLockAndCascade(ctx, scope, projectRoot, opts.Name, allowCascade)
	cleanupErrs = append(cleanupErrs, lockErrs...)

	if s.groupManager != nil {
		if groupErr := groups.RemoveSkillFromAllGroups(ctx, s.groupManager, opts.Name); groupErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing skill from groups: %w", groupErr))
		}
	}

	return cascaded, errors.Join(cleanupErrs...)
}

func (s *service) cleanupSkillFiles(
	existing skills.InstalledSkill, name string, scope skills.Scope, projectRoot string,
) []error {
	if s.pathResolver == nil {
		return nil
	}
	stopDir := projectRoot
	if scope == skills.ScopeUser {
		if homeDir, err := os.UserHomeDir(); err == nil {
			stopDir = homeDir
		}
	}
	var cleanupErrs []error
	for _, clientType := range existing.Clients {
		skillPath, pathErr := s.pathResolver.GetSkillPath(clientType, name, scope, projectRoot)
		if pathErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("resolving path for client %q: %w", clientType, pathErr))
			continue
		}
		if rmErr := s.installer.Remove(skillPath); rmErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing files for client %q: %w", clientType, rmErr))
			continue
		}
		if stopDir != "" {
			skills.RemoveEmptyParents(filepath.Dir(skillPath), stopDir)
		}
	}
	return cleanupErrs
}

func (s *service) uninstallLockAndCascade(
	ctx context.Context, scope skills.Scope, projectRoot, name string, allowCascade bool,
) ([]string, []error) {
	if scope != skills.ScopeProject || projectRoot == "" {
		return nil, nil
	}
	candidates, err := updateLockAfterUninstall(projectRoot, name)
	if err != nil {
		return nil, []error{err}
	}
	if !allowCascade {
		return nil, nil
	}
	var cascaded []string
	var cleanupErrs []error
	for _, depName := range candidates {
		more, cascadeErr := s.uninstallInternal(ctx, skills.UninstallOptions{
			Name: depName, Scope: skills.ScopeProject, ProjectRoot: projectRoot,
		}, true)
		if cascadeErr != nil && !errors.Is(cascadeErr, storage.ErrNotFound) {
			slog.Warn("cascade uninstall failed", "skill", depName, "error", cascadeErr)
			continue
		}
		cascaded = append(cascaded, depName)
		cascaded = append(cascaded, more...)
	}
	return cascaded, cleanupErrs
}

func updateLockAfterUninstall(projectRoot, name string) ([]string, error) {
	var candidates []string
	err := lockfile.UpdateEntry(projectRoot, func(lf *lockfile.Lockfile) error {
		lf.Remove(name)
		candidates = lf.RemoveParentFromRequiredBy(name)
		return nil
	})
	return candidates, err
}
