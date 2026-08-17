// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

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

	_, unlock := s.lockPlugin(ctx, opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	return s.uninstallLocked(ctx, opts, scope)
}

// uninstallLocked performs Uninstall assuming the per-plugin lock is already
// held (e.g. by Sync prune). opts must already be normalized.
func (s *service) uninstallLocked(
	ctx context.Context, opts plugins.UninstallOptions, scope plugins.Scope,
) error {
	existing, err := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		// Idempotent: a missing record is not an error.
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return err
	}
	return s.uninstallExisting(ctx, opts, scope, existing)
}

// uninstallExisting performs uninstall for a looked-up store record under the
// per-plugin lock.
func (s *service) uninstallExisting(
	ctx context.Context,
	opts plugins.UninstallOptions,
	scope plugins.Scope,
	existing plugins.InstalledPlugin,
) error {
	if scope == plugins.ScopeProject && existing.Managed {
		if err := s.requireMaterializers(existing.Clients); err != nil {
			return err
		}
	}

	restoreLock, err := removeManagedLockEntry(opts, existing, scope)
	if err != nil {
		return err
	}

	// Tree snapshots need a ClientManager for path resolution; without one
	// (WithClientManager is optional) managed compensation degrades to
	// restoring the lock pin only, matching materializeAndPersist's policy.
	var backups map[string]clientTreeBackup
	if restoreLock != nil && s.clientManager != nil {
		var snapErr error
		backups, snapErr = s.snapshotClientTrees(ctx, opts.Name, scope, opts.ProjectRoot, existing.Clients)
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

	restoreGroups, groupErr := s.removePluginGroups(ctx, opts.Name)
	if groupErr != nil {
		if restoreLock != nil {
			return errors.Join(groupErr, s.compensateManagedUninstall(
				ctx, restoreLock, opts.Name, scope, opts.ProjectRoot, backups, existing.Clients,
			))
		}
		return errors.Join(append(cleanupErrs, groupErr)...)
	}

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		restoreErrs := []error{err}
		if restoreGroups != nil {
			restoreErrs = append(restoreErrs, restoreGroups(ctx))
		}
		if restoreLock != nil {
			restoreErrs = append(restoreErrs, s.compensateManagedUninstall(
				ctx, restoreLock, opts.Name, scope, opts.ProjectRoot, backups, existing.Clients,
			))
		}
		return errors.Join(restoreErrs...)
	}
	return errors.Join(cleanupErrs...)
}

// removePluginGroups removes the plugin from every group that references it,
// before the DB delete so a failed cleanup remains retryable. Group updates
// run sequentially and can fail midway, so memberships are snapshotted first:
// a mid-removal failure re-adds the memberships already removed, and the
// returned restore func lets a later DB-delete failure reinstate all of them.
// The restore func is nil when the plugin belonged to no group.
func (s *service) removePluginGroups(
	ctx context.Context, name string,
) (restore func(context.Context) error, err error) {
	if s.groupManager == nil {
		return nil, nil
	}
	all, err := s.groupManager.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("removing plugin from groups: listing groups: %w", err)
	}
	var members []string
	for _, g := range all {
		if slices.Contains(g.Plugins, name) {
			members = append(members, g.Name)
		}
	}
	if len(members) == 0 {
		return nil, nil
	}
	restoreUpTo := func(ctx context.Context, upTo int) error {
		var errs []error
		for _, groupName := range members[:upTo] {
			if _, addErr := groups.AddPluginToGroup(ctx, s.groupManager, groupName, name); addErr != nil {
				errs = append(errs, fmt.Errorf("restoring plugin membership in group %q: %w", groupName, addErr))
			}
		}
		return errors.Join(errs...)
	}
	for i, groupName := range members {
		if removeErr := groups.RemovePluginFromGroup(ctx, s.groupManager, groupName, name); removeErr != nil {
			return nil, errors.Join(
				fmt.Errorf("removing plugin from groups: group %q: %w", groupName, removeErr),
				restoreUpTo(ctx, i),
			)
		}
	}
	return func(ctx context.Context) error { return restoreUpTo(ctx, len(members)) }, nil
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
// client tree after a failed managed uninstall step. Without a ClientManager
// no snapshots were taken, so only the lock pin is restored.
func (s *service) compensateManagedUninstall(
	ctx context.Context,
	restoreLock func() error,
	name string,
	scope plugins.Scope,
	projectRoot string,
	backups map[string]clientTreeBackup,
	clients []string,
) error {
	if s.clientManager == nil {
		return restoreLock()
	}
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
