// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"net/http"
	"slices"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// Info returns detailed information about an installed plugin, including the
// per-client set of component types the plugin declares that the installed
// client adapter does NOT load (UnmaterializedComponents).
func (s *service) Info(ctx context.Context, opts plugins.InfoOptions) (*plugins.PluginInfo, error) {
	if err := plugins.ValidatePluginName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	scope = defaultScope(scope)

	stored, err := s.store.Get(ctx, opts.Name, scope, projectRoot)
	if err != nil {
		return nil, err
	}

	degraded := s.computeProjectScopeDegraded(stored)
	return &plugins.PluginInfo{
		Metadata:                    stored.Metadata,
		InstalledPlugin:             &stored,
		UnmaterializedComponents:    s.computeUnmaterialized(stored),
		ProjectScopeDegradedClients: degraded,
	}, nil
}

// computeProjectScopeDegraded returns the client types for which a
// project-scoped install degraded. A client degrades iff the install is
// project-scoped AND its adapter reports ScopeSupport.DegradesOnProjectScope.
// Empty for user-scoped installs. Recomputed at read time (deterministic from
// scope + adapter capability); no persistence needed.
func (s *service) computeProjectScopeDegraded(p plugins.InstalledPlugin) []string {
	if p.Scope != plugins.ScopeProject {
		return nil
	}
	var degraded []string
	for _, ct := range p.Clients {
		adapter, ok := s.materializers[ct]
		if !ok {
			continue
		}
		if adapter.ScopeSupport().DegradesOnProjectScope {
			degraded = append(degraded, ct)
		}
	}
	return degraded
}

// computeUnmaterialized diffs the plugin's declared Components against each
// installed client adapter's SupportedComponents. For each client, the result
// lists component types the plugin declares that the adapter does NOT load.
func (s *service) computeUnmaterialized(p plugins.InstalledPlugin) map[string][]plugins.ComponentType {
	if len(p.Components) == 0 || len(s.materializers) == 0 {
		return nil
	}
	out := make(map[string][]plugins.ComponentType, len(p.Clients))
	for _, ct := range p.Clients {
		adapter, ok := s.materializers[ct]
		if !ok {
			continue
		}
		supported := adapter.SupportedComponents()
		var dropped []plugins.ComponentType
		for componentType := range p.Components {
			ct := plugins.ComponentType(componentType)
			if !slices.Contains(supported, ct) {
				dropped = append(dropped, ct)
			}
		}
		if len(dropped) > 0 {
			slices.Sort(dropped)
			out[ct] = dropped
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
