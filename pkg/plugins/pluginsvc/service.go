// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pluginsvc provides the default implementation of plugins.PluginService.
//
// Phase 3 widens the Phase-2 build/validate/push/content surface with
// Install/Uninstall/List/Info and the per-client MaterializationAdapter seam.
// The New constructor returns plugins.PluginService, which now exposes the full
// lifecycle; callers pass WithStore/WithMaterializers/WithGroupManager (and the
// other Phase-3 options) to wire persistence and materialization.
package pluginsvc

import (
	"context"
	"sync"

	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/git"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/storage"
)

// Option configures the plugin service.
type Option func(*service)

// WithOCIStore sets the local OCI store for plugin artifacts.
func WithOCIStore(store *ociplugins.Store) Option {
	return func(s *service) {
		s.ociStore = store
	}
}

// WithPackager sets the plugin packager for building OCI artifacts.
func WithPackager(p ociplugins.PluginPackager) Option {
	return func(s *service) {
		s.packager = p
	}
}

// WithRegistryClient sets the registry client for push/pull operations.
func WithRegistryClient(rc ociplugins.RegistryClient) Option {
	return func(s *service) {
		s.registry = rc
	}
}

// WithStore sets the persistence store for installed plugins.
func WithStore(store storage.PluginStore) Option {
	return func(s *service) {
		s.store = store
	}
}

// WithGroupManager sets the group manager for plugin group membership.
func WithGroupManager(mgr groups.Manager) Option {
	return func(s *service) {
		s.groupManager = mgr
	}
}

// WithMaterializers sets the per-client-type materialization adapters used to
// install a plugin tree into a target client's directory layout.
func WithMaterializers(m map[string]plugins.MaterializationAdapter) Option {
	return func(s *service) {
		s.materializers = m
	}
}

// WithGitClient sets a fixed git client used by the git install flow, bypassing
// per-clone auth resolution. Primarily used for testing with mock clients;
// mirrors gitresolver.WithGitClient.
func WithGitClient(c git.Client) Option {
	return func(s *service) {
		s.gitClient = c
	}
}

// WithClientManager sets the client manager used to validate that requested
// clients support plugins. Optional: when nil, the materializers map is the
// sole source of truth for plugin-supporting clients.
func WithClientManager(cm *client.ClientManager) Option {
	return func(s *service) {
		s.clientManager = cm
	}
}

// PluginSearchHit is a single result from a registry-name plugin search. It is
// the plugin analogue of a registry skill hit, used by the registry-name
// install flow that lands in a later Phase-3 wave.
type PluginSearchHit struct {
	// Name is the plugin name (kebab-case).
	Name string `json:"name"`
	// Description is a human-readable description of the plugin.
	Description string `json:"description,omitempty"`
	// Packages lists the OCI packages that publish this plugin.
	Packages []PluginPackage `json:"packages,omitempty"`
}

// PluginPackage describes a single OCI package backing a plugin search hit.
type PluginPackage struct {
	// Reference is the full OCI reference (e.g. ghcr.io/org/plugin:v1).
	Reference string `json:"reference"`
	// Type is the package type (e.g. "oci").
	Type string `json:"type,omitempty"`
}

// PluginLookup resolves a plain plugin name against a registry/index. This seam
// stays unwired in Wave 0; the registry-name install flow lands in a later
// Phase-3 wave. It mirrors skillsvc.SkillLookup.
type PluginLookup interface {
	SearchPlugins(ctx context.Context, query string) ([]PluginSearchHit, error)
}

// WithPluginLookup sets the registry-based plugin lookup for name resolution.
func WithPluginLookup(pl PluginLookup) Option {
	return func(s *service) {
		s.pluginLookup = pl
	}
}

// pluginLock provides per-plugin mutual exclusion keyed by scope/name/projectRoot.
// Entries are never evicted. This is acceptable because the number of distinct
// plugins on a single machine is expected to remain small (< 1000). The key
// shape (scope/name/projectRoot) is identical to skillsvc.skillLock.
//
// The install/uninstall flows acquire per-key mutexes through lock() (mirroring
// skillsvc.service.install/uninstall).
type pluginLock struct {
	mu sync.Mutex
	// locks holds per-key mutexes. INVARIANT: entries must never be deleted
	// from this map. The two-phase lock() method depends on pointers remaining
	// valid after the global mutex is released. See lock() for details.
	locks map[string]*sync.Mutex
}

// lock acquires a per-plugin mutex and returns a function that releases it.
func (pl *pluginLock) lock(name string, scope plugins.Scope, projectRoot string) func() {
	pl.mu.Lock()
	key := string(scope) + "/" + name + "/" + projectRoot
	m, ok := pl.locks[key]
	if !ok {
		m = &sync.Mutex{}
		pl.locks[key] = m
	}
	pl.mu.Unlock()

	m.Lock()
	return m.Unlock
}

// service is the default implementation of plugins.PluginService. It implements
// the build/validate/push/content surface (Phase 2) and the install/uninstall/
// list/info surface (Phase 3), the latter driving per-client materialization
// through the configured MaterializationAdapters.
type service struct {
	locks         pluginLock
	store         storage.PluginStore
	groupManager  groups.Manager
	materializers map[string]plugins.MaterializationAdapter
	ociStore      *ociplugins.Store
	packager      ociplugins.PluginPackager
	registry      ociplugins.RegistryClient
	pluginLookup  PluginLookup
	gitClient     git.Client
	clientManager *client.ClientManager
}

// New creates a new plugin service and returns it as a plugins.PluginService.
// Callers wire persistence (WithStore), per-client materialization
// (WithMaterializers), group membership (WithGroupManager), git installs
// (WithGitClient), and registry-name lookup (WithPluginLookup) via options;
// the OCI store/packager/registry options carry over from Phase 2.
func New(opts ...Option) plugins.PluginService {
	s := &service{
		locks: pluginLock{locks: make(map[string]*sync.Mutex)},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}
