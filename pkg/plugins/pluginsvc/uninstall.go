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
	"github.com/stacklok/toolhive/pkg/storage"
)

// Uninstall removes an installed plugin and dematerializes it for all clients.
// Dematerialization is best-effort: errors are collected via errors.Join so a
// single client failure does not abort cleanup of the others. The DB record is
// always deleted (when present), making Uninstall idempotent. Mirror of
// skillsvc.Uninstall, substituting MaterializationAdapter.Dematerialize for
// pathResolver + installer.Remove.
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

	// Dematerialize for each client — best-effort.
	var cleanupErrs []error
	for _, clientType := range existing.Clients {
		adapter, ok := s.materializers[clientType]
		if !ok {
			continue
		}
		if dmErr := adapter.Dematerialize(ctx, plugins.DematerializeRequest{
			Name:        opts.Name,
			Scope:       scope,
			ProjectRoot: opts.ProjectRoot,
		}); dmErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("dematerializing plugin for client %q: %w", clientType, dmErr))
		}
	}

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		return err
	}

	if s.groupManager != nil {
		if groupErr := groups.RemovePluginFromAllGroups(ctx, s.groupManager, opts.Name); groupErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("removing plugin from groups: %w", groupErr))
		}
	}

	return errors.Join(cleanupErrs...)
}
