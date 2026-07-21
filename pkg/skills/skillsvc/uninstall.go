// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// Uninstall removes an installed skill and cleans up files for all clients.
// For a project-scope, lock-managed skill (see skills.LockFileFeatureEnabled),
// it also removes the skill's lock entry and cascades to any dependency that
// loses its last requiring parent as a result.
func (s *service) Uninstall(ctx context.Context, opts skills.UninstallOptions) error {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return err
	}
	scope = defaultScope(scope)
	opts.ProjectRoot = projectRoot

	return s.uninstallOne(ctx, opts, scope)
}

// uninstallOne performs a single skill's file/DB/group cleanup and, when
// applicable, its lock entry removal and cascade. It is called both for the
// top-level Uninstall request and recursively for cascade candidates.
func (s *service) uninstallOne(ctx context.Context, opts skills.UninstallOptions, scope skills.Scope) error {
	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	// Look up the existing record to find which clients have files.
	existing, err := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return err
	}

	cleanupErrs := s.removeClientFiles(existing, opts, scope)

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		return err
	}

	// Remove the skill from all groups — best-effort, same pattern as file cleanup.
	if s.groupManager != nil {
		if groupErr := groups.RemoveSkillFromAllGroups(ctx, s.groupManager, opts.Name); groupErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing skill from groups: %w", groupErr))
		}
	}

	// Lock file cleanup is best-effort, matching every other cleanup step
	// here: the DB record and files are already gone by this point, so there
	// is nothing left to roll back to on failure.
	if scope == skills.ScopeProject && existing.Managed && skills.LockFileFeatureEnabled() {
		if cascadeErr := s.removeLockEntryAndCascade(ctx, opts, scope); cascadeErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("updating project lock file: %w", cascadeErr))
		}
	}

	return errors.Join(cleanupErrs...)
}

// removeClientFiles removes on-disk files for every client existing is
// installed for. It is best-effort: errors are collected rather than
// aborting, so cleanup proceeds as far as possible.
func (s *service) removeClientFiles(
	existing skills.InstalledSkill, opts skills.UninstallOptions, scope skills.Scope,
) []error {
	if s.pathResolver == nil {
		return nil
	}

	// Determine the boundary directory for empty-parent cleanup.
	stopDir := opts.ProjectRoot
	if scope == skills.ScopeUser {
		if homeDir, err := os.UserHomeDir(); err == nil {
			stopDir = homeDir
		}
	}

	var errs []error
	for _, clientType := range existing.Clients {
		skillPath, pathErr := s.pathResolver.GetSkillPath(clientType, opts.Name, scope, opts.ProjectRoot)
		if pathErr != nil {
			errs = append(errs, fmt.Errorf("resolving path for client %q: %w", clientType, pathErr))
			continue
		}
		if rmErr := s.installer.Remove(skillPath); rmErr != nil {
			errs = append(errs, fmt.Errorf("removing files for client %q: %w", clientType, rmErr))
			continue
		}
		if stopDir != "" {
			skills.RemoveEmptyParents(filepath.Dir(skillPath), stopDir)
		}
	}
	return errs
}

// removeLockEntryAndCascade removes opts.Name's lock entry and, for any
// dependency that consequently loses its last requiring parent (and is not
// itself explicit), uninstalls it too. A Visited set threaded through opts
// prevents infinite recursion on a requiredBy cycle in a hand-edited lock.
func (s *service) removeLockEntryAndCascade(ctx context.Context, opts skills.UninstallOptions, scope skills.Scope) error {
	visited := opts.Visited
	if visited == nil {
		visited = make(map[string]struct{})
	}
	visited[opts.Name] = struct{}{}

	root, err := lockfile.OpenRoot(opts.ProjectRoot)
	if err != nil {
		return err
	}
	var cascadeCandidates []string
	if err := lockfile.Update(root, func(lf *lockfile.Lockfile) error {
		lf.Remove(opts.Name)
		cascadeCandidates = lf.RemoveParentFromRequiredBy(opts.Name)
		return nil
	}); err != nil {
		return err
	}

	var errs []error
	for _, dep := range cascadeCandidates {
		if _, seen := visited[dep]; seen {
			continue
		}
		visited[dep] = struct{}{}
		// A lock entry can reference a dependency whose install failed
		// partway (or was already removed by hand); skip it rather than
		// erroring on a missing DB record.
		if _, getErr := s.store.Get(ctx, dep, scope, opts.ProjectRoot); getErr != nil {
			continue
		}
		if uninstallErr := s.uninstallOne(ctx, skills.UninstallOptions{
			Name:        dep,
			Scope:       scope,
			ProjectRoot: opts.ProjectRoot,
			Visited:     visited,
		}, scope); uninstallErr != nil {
			errs = append(errs, fmt.Errorf("cascade-removing dependency %q: %w", dep, uninstallErr))
		}
	}
	return errors.Join(errs...)
}
