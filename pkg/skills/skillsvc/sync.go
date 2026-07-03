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
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
)

// Sync installs the exact name/digest pinned in the project's lock file for
// every entry, restoring skills that are missing or have drifted from their
// pinned digest. Project-scoped skills that are installed but absent from
// the lock file are reported as unmanaged, or removed when opts.Prune is set.
func (s *service) Sync(ctx context.Context, opts skills.SyncOptions) (*skills.SyncResult, error) {
	projectRoot, err := skills.ValidateProjectRoot(opts.ProjectRoot)
	if err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	lf, err := lockfile.Load(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("loading lock file: %w", err)
	}

	result := &skills.SyncResult{}
	locked := make(map[string]struct{}, len(lf.Skills))
	for _, entry := range lf.Skills {
		locked[entry.Name] = struct{}{}
		s.syncEntry(ctx, projectRoot, entry, opts.Clients, result)
	}

	if err := s.syncUnmanaged(ctx, projectRoot, locked, opts.Prune, result); err != nil {
		return nil, err
	}

	return result, nil
}

// syncEntry restores a single lock entry, appending its outcome to result.
func (s *service) syncEntry(
	ctx context.Context, projectRoot string, entry lockfile.Entry, clients []string, result *skills.SyncResult,
) {
	existing, storeErr := s.store.Get(ctx, entry.Name, skills.ScopeProject, projectRoot)
	if storeErr == nil && existing.Digest == entry.Digest {
		result.UpToDate = append(result.UpToDate, entry.Name)
		return
	}

	pinnedRef, err := buildPinnedReference(entry)
	if err != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{Name: entry.Name, Error: err.Error()})
		return
	}

	// installInternal (not Install) is used deliberately: pinnedRef is a
	// digest-pinned reference derived from the entry, not the user-facing
	// source, so it must not overwrite the lock entry's Source field. Since
	// we install exactly the pinned digest, the entry itself never needs to
	// change as a result of a sync.
	if _, err := s.installInternal(ctx, skills.InstallOptions{
		Name:        pinnedRef,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     clients,
		Force:       true,
	}); err != nil {
		result.Failed = append(result.Failed, skills.SyncFailure{Name: entry.Name, Error: err.Error()})
		return
	}
	result.Installed = append(result.Installed, entry.Name)
}

// syncUnmanaged finds project-scoped skills installed outside of the lock
// file and either reports or prunes them.
func (s *service) syncUnmanaged(
	ctx context.Context, projectRoot string, locked map[string]struct{}, prune bool, result *skills.SyncResult,
) error {
	installed, err := s.store.List(ctx, storage.ListFilter{Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	if err != nil {
		return fmt.Errorf("listing installed skills: %w", err)
	}

	for _, sk := range installed {
		name := sk.Metadata.Name
		if _, ok := locked[name]; ok {
			continue
		}
		if !prune {
			result.Unmanaged = append(result.Unmanaged, name)
			continue
		}
		if err := s.Uninstall(ctx, skills.UninstallOptions{
			Name: name, Scope: skills.ScopeProject, ProjectRoot: projectRoot,
		}); err != nil && !errors.Is(err, storage.ErrNotFound) {
			result.Failed = append(result.Failed, skills.SyncFailure{Name: name, Error: err.Error()})
			continue
		}
		result.Pruned = append(result.Pruned, name)
	}
	return nil
}
