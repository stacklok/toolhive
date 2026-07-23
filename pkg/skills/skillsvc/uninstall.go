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

// uninstallOne performs a single skill's lock-entry removal, file/DB/group
// cleanup and, when applicable, its dependency cascade. It is called both
// for the top-level Uninstall request and recursively for cascade candidates.
//
// The lock entry is removed FIRST, and its failure aborts the uninstall
// while everything is still intact. The reverse order (files/DB first, lock
// entry best-effort) had a resurrection hazard: a lock-write failure after
// the record and files were gone left a stale entry that the next sync
// silently reinstalled. Failing after the entry is removed leaves the
// opposite, safe inconsistency — an installed-but-unlocked skill that sync
// reports as removed-from-lock and prune can clean up.
func (s *service) uninstallOne(ctx context.Context, opts skills.UninstallOptions, scope skills.Scope) error {
	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	// Look up the existing record to find which clients have files.
	existing, err := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return err
	}

	visited := opts.Visited
	if visited == nil {
		visited = make(map[string]struct{})
	}
	visited[opts.Name] = struct{}{}

	var cascadeCandidates []string
	if scope == skills.ScopeProject && existing.Managed && skills.LockFileFeatureEnabled() {
		cascadeCandidates, err = removeLockEntry(opts)
		if err != nil {
			return fmt.Errorf("updating project lock file: %w", err)
		}
	}

	cleanupErrs := s.removeClientFiles(existing, opts, scope)

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		cleanupErrs = append(cleanupErrs, err)
		return errors.Join(cleanupErrs...)
	}

	// Remove the skill from all groups — best-effort, same pattern as file cleanup.
	if s.groupManager != nil {
		if groupErr := groups.RemoveSkillFromAllGroups(ctx, s.groupManager, opts.Name); groupErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing skill from groups: %w", groupErr))
		}
	}

	if cascadeErr := s.cascadeUninstall(ctx, cascadeCandidates, visited, opts.ProjectRoot, scope); cascadeErr != nil {
		cleanupErrs = append(cleanupErrs, cascadeErr)
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

// removeLockEntry removes opts.Name's lock entry and strips it from every
// other entry's RequiredBy list, returning the names of dependencies that
// consequently lost their last requiring parent and are not explicit — the
// cascade-removal candidates.
func removeLockEntry(opts skills.UninstallOptions) ([]string, error) {
	root, err := lockfile.OpenRoot(opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	var cascadeCandidates []string
	if err := lockfile.Update(root, func(lf *lockfile.Lockfile) error {
		lf.Remove(opts.Name)
		cascadeCandidates = lf.RemoveParentFromRequiredBy(opts.Name)
		return nil
	}); err != nil {
		return nil, err
	}
	return cascadeCandidates, nil
}

// cascadeUninstall uninstalls each candidate dependency that has not
// already been visited. The visited set prevents infinite recursion on a
// requiredBy cycle in a hand-edited lock.
func (s *service) cascadeUninstall(
	ctx context.Context, candidates []string, visited map[string]struct{}, projectRoot string, scope skills.Scope,
) error {
	var errs []error
	for _, dep := range candidates {
		if _, seen := visited[dep]; seen {
			continue
		}
		visited[dep] = struct{}{}
		// A lock entry can reference a dependency whose install failed
		// partway (or was already removed by hand); skip it rather than
		// erroring on a missing DB record.
		if _, getErr := s.store.Get(ctx, dep, scope, projectRoot); getErr != nil {
			continue
		}
		if uninstallErr := s.uninstallOne(ctx, skills.UninstallOptions{
			Name:        dep,
			Scope:       scope,
			ProjectRoot: projectRoot,
			Visited:     visited,
		}, scope); uninstallErr != nil {
			errs = append(errs, fmt.Errorf("cascade-removing dependency %q: %w", dep, uninstallErr))
		}
	}
	return errors.Join(errs...)
}
