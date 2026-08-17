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
// installs the lock entry is removed first after snapshotting every client
// tree; a later dematerialize, group-cleanup, or DB-delete failure restores
// the pin, the plugin trees, and adapter registration so the plugin is not
// left installed-but-untracked or half-removed across clients.
//
// Tree snapshots run only when managed rollback is available: unmanaged and
// user-scope uninstalls must not require a ClientManager just to take unused
// backups.
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

	managed := scope == plugins.ScopeProject && existing.Managed
	if managed {
		if err := s.requireMaterializers(existing.Clients); err != nil {
			return err
		}
	}

	restoreLock, err := removeManagedLockEntry(opts, existing, scope)
	if err != nil {
		return err
	}

	var backups map[string]map[string]fileSnapshot
	if restoreLock != nil {
		var snapErr error
		backups, snapErr = s.snapshotClientTrees(opts.Name, scope, opts.ProjectRoot, existing.Clients)
		if snapErr != nil {
			return errors.Join(
				fmt.Errorf("snapshotting plugin trees before uninstall: %w", snapErr),
				restoreLock(),
			)
		}
	}

	cleanupErrs := s.dematerializeClients(ctx, existing, scope, opts.ProjectRoot)
	if len(cleanupErrs) > 0 && restoreLock != nil {
		return errors.Join(append(cleanupErrs, s.compensateManagedUninstall(
			ctx, restoreLock, opts.Name, scope, opts.ProjectRoot, backups, existing.Clients,
		))...)
	}

	// Remove group membership before deleting the DB row so a failed group
	// cleanup remains retryable (Uninstall is a no-op once the record is gone).
	if s.groupManager != nil {
		if groupErr := groups.RemovePluginFromAllGroups(ctx, s.groupManager, opts.Name); groupErr != nil {
			groupErr = fmt.Errorf("removing plugin from groups: %w", groupErr)
			if restoreLock != nil {
				return errors.Join(groupErr, s.compensateManagedUninstall(
					ctx, restoreLock, opts.Name, scope, opts.ProjectRoot, backups, existing.Clients,
				))
			}
			cleanupErrs = append(cleanupErrs, groupErr)
			return errors.Join(cleanupErrs...)
		}
	}

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		if restoreLock != nil {
			return errors.Join(err, s.compensateManagedUninstall(
				ctx, restoreLock, opts.Name, scope, opts.ProjectRoot, backups, existing.Clients,
			))
		}
		return err
	}

	return errors.Join(cleanupErrs...)
}

// requireMaterializers fails closed when a managed uninstall would delete the
// lock/DB while leaving an executable tree behind because no adapter can
// dematerialize a recorded client. Unmanaged uninstall keeps the historical
// skip-missing-adapter behavior.
func (s *service) requireMaterializers(clients []string) error {
	for _, clientType := range clients {
		if _, ok := s.materializers[clientType]; !ok {
			return httperr.WithCode(
				fmt.Errorf("no materializer configured for client %q; refusing managed uninstall", clientType),
				http.StatusInternalServerError,
			)
		}
	}
	return nil
}

// compensateManagedUninstall restores the lock pin and every snapshotted
// client tree after a failed managed uninstall step.
func (s *service) compensateManagedUninstall(
	ctx context.Context,
	restoreLock func() error,
	name string,
	scope plugins.Scope,
	projectRoot string,
	backups map[string]map[string]fileSnapshot,
	clients []string,
) error {
	return errors.Join(
		restoreLock(),
		s.restoreClientTrees(ctx, name, scope, projectRoot, backups, clients),
	)
}

// removeManagedLockEntry removes the plugins: lock entry for a lock-managed
// project-scope install, returning a restore func that reinstates the
// snapshotted entry. The restore func is nil when no entry was removed.
func removeManagedLockEntry(
	opts plugins.UninstallOptions,
	existing plugins.InstalledPlugin,
	scope plugins.Scope,
) (restore func() error, err error) {
	if scope != plugins.ScopeProject || !existing.Managed {
		return nil, nil
	}

	root, err := lockfile.OpenRoot(opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("opening lock file root: %w", err)
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return nil, fmt.Errorf("loading lock file: %w", err)
	}
	prev, hasPrev := lf.GetPlugin(opts.Name)
	if lockErr := removeLockEntry(opts); lockErr != nil {
		return nil, fmt.Errorf("updating project lock file: %w", lockErr)
	}
	if !hasPrev {
		return func() error { return nil }, nil
	}
	return func() error {
		root, err := lockfile.OpenRoot(opts.ProjectRoot)
		if err != nil {
			return fmt.Errorf("restoring lock entry: %w", err)
		}
		if err := lockfile.UpsertPluginEntry(root, prev); err != nil {
			return fmt.Errorf("restoring lock entry: %w", err)
		}
		return nil
	}, nil
}

// dematerializeClients best-effort removes on-disk copies for each client the
// plugin was installed into. Missing adapters are skipped (unmanaged path).
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
