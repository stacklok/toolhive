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
)

// Uninstall removes an installed skill and cleans up files for all clients.
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

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	// Look up the existing record to find which clients have files.
	existing, err := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return err
	}

	// Determine the boundary directory for empty-parent cleanup.
	stopDir := opts.ProjectRoot
	if scope == skills.ScopeUser {
		if homeDir, err := os.UserHomeDir(); err == nil {
			stopDir = homeDir
		}
	}

	// Remove files for each client — best-effort: collect errors but don't
	// abort on the first failure so we clean up as much as possible.
	var cleanupErrs []error
	if s.pathResolver != nil {
		for _, clientType := range existing.Clients {
			skillPath, pathErr := s.pathResolver.GetSkillPath(clientType, opts.Name, scope, opts.ProjectRoot)
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
	}

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		return err
	}

	// Remove the skill from all groups — best-effort, same pattern as file cleanup.
	if s.groupManager != nil {
		if groupErr := groups.RemoveSkillFromAllGroups(ctx, s.groupManager, opts.Name); groupErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing skill from groups: %w", groupErr))
		}
	}

	return errors.Join(cleanupErrs...)
}
