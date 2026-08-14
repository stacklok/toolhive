// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
)

// Uninstall removes an installed plugin and dematerializes it for all clients.
// Dematerialization is best-effort for unmanaged installs: errors are collected
// via errors.Join so a single client failure does not abort cleanup of the
// others, and the DB record is still deleted. For lock-managed project-scope
// installs the lock entry is removed first; a later dematerialize or DB-delete
// failure restores the pin and aborts so the plugin is not left
// installed-but-untracked.
func (s *service) Uninstall(ctx context.Context, opts plugins.UninstallOptions) error {
	if err := plugins.ValidatePluginName(opts.Name); err != nil {
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

	existing, err := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		// Idempotent: a missing record is not an error.
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return err
	}

	restoreLock, err := removeManagedLockEntry(opts, existing, scope)
	if err != nil {
		return err
	}

	cleanupErrs := s.dematerializeClients(ctx, existing, scope, opts.ProjectRoot)
	if len(cleanupErrs) > 0 && restoreLock != nil {
		restoreLock()
		return errors.Join(cleanupErrs...)
	}

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		if restoreLock != nil {
			restoreLock()
		}
		return err
	}

	if s.groupManager != nil {
		if groupErr := groups.RemovePluginFromAllGroups(ctx, s.groupManager, opts.Name); groupErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing plugin from groups: %w", groupErr))
		}
	}

	return errors.Join(cleanupErrs...)
}

// removeManagedLockEntry removes the plugins: lock entry for a lock-managed
// project-scope install, returning a restore func that reinstates the
// snapshotted entry. The restore func is nil when no entry was removed.
func removeManagedLockEntry(
	opts plugins.UninstallOptions,
	existing plugins.InstalledPlugin,
	scope plugins.Scope,
) (restore func(), err error) {
	if scope != plugins.ScopeProject || !existing.Managed {
		return nil, nil
	}

	var prevLock *lockfile.Entry
	if root, rootErr := lockfile.OpenRoot(opts.ProjectRoot); rootErr == nil {
		if lf, loadErr := lockfile.Load(root); loadErr == nil {
			if e, ok := lf.GetPlugin(opts.Name); ok {
				prevLock = &e
			}
		}
	}
	if lockErr := removeLockEntry(opts); lockErr != nil {
		return nil, fmt.Errorf("updating project lock file: %w", lockErr)
	}
	if prevLock == nil {
		return func() {}, nil
	}
	return func() {
		if root, rootErr := lockfile.OpenRoot(opts.ProjectRoot); rootErr == nil {
			_ = lockfile.UpsertPluginEntry(root, *prevLock)
		}
	}, nil
}

// dematerializeClients best-effort removes on-disk copies for each client the
// plugin was installed into. Missing adapters are skipped.
func (s *service) dematerializeClients(
	ctx context.Context,
	existing plugins.InstalledPlugin,
	scope plugins.Scope,
	projectRoot string,
) []error {
	var cleanupErrs []error
	for _, clientType := range existing.Clients {
		adapter, ok := s.materializers[clientType]
		if !ok {
			continue
		}
		if dmErr := adapter.Dematerialize(ctx, plugins.DematerializeRequest{
			Name:        existing.Metadata.Name,
			Scope:       scope,
			ProjectRoot: projectRoot,
		}); dmErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("dematerializing plugin for client %q: %w", clientType, dmErr))
		}
	}
	return cleanupErrs
}
