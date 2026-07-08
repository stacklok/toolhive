// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/storage"
)

// List returns all installed plugins matching the given options. Mirror of
// skillsvc.List, substituting group.Plugins for group.Skills in the
// group-membership filter.
func (s *service) List(ctx context.Context, opts plugins.ListOptions) ([]plugins.InstalledPlugin, error) {
	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	filter := storage.ListFilter{
		Scope:       scope,
		ClientApp:   opts.ClientApp,
		ProjectRoot: projectRoot,
	}
	all, err := s.store.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	if opts.Group == "" {
		return all, nil
	}

	if s.groupManager == nil {
		return nil, httperr.WithCode(
			fmt.Errorf("group filtering is not available: group manager is not configured"),
			http.StatusInternalServerError,
		)
	}

	group, err := s.groupManager.Get(ctx, opts.Group)
	if err != nil {
		return nil, fmt.Errorf("getting group %q: %w", opts.Group, err)
	}

	// Build a lookup set of plugin names in the group.
	groupPlugins := make(map[string]struct{}, len(group.Plugins))
	for _, name := range group.Plugins {
		groupPlugins[name] = struct{}{}
	}

	filtered := make([]plugins.InstalledPlugin, 0, len(all))
	for _, p := range all {
		if _, ok := groupPlugins[p.Metadata.Name]; ok {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}
