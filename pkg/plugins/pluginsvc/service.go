// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pluginsvc provides the default implementation of plugins.PluginService.
//
// Phase 2 (this package) implements only the build/validate/push/list-builds/
// delete-build/get-content surface. Install/uninstall/list/info land in Phase 3
// (#5527); the New constructor returns the narrowed plugins.PluginService
// interface (which exposes only the Phase-2 methods), so Phase 3 widens both
// the interface and the concrete type together.
package pluginsvc

import (
	"context"
	"sync"

	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
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

// WithInstaller sets the installer for filesystem operations. Plugins reuse
// the skills Installer (the Extract/Remove helpers) because the extraction
// and safe-removal logic is identical for both artifact types.
func WithInstaller(inst skills.Installer) Option {
	return func(s *service) {
		s.installer = inst
	}
}

// WithGitResolver sets the git resolver for git:// plugin installations. Reuses
// the skills git resolver because the clone/extract shape is identical.
func WithGitResolver(gr gitresolver.Resolver) Option {
	return func(s *service) {
		s.gitResolver = gr
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
// The lock/unlock methods are currently unused: Wave 0 declares the seam, and
// Wave 2's install/uninstall flows will acquire per-key mutexes through it
// (mirroring skillsvc.service.install/uninstall). The nolint:unused directives
// are intentional and should be removed once Wave 2 lands.
type pluginLock struct {
	mu sync.Mutex //nolint:unused // Wave 2 seam
	// locks holds per-key mutexes. INVARIANT: entries must never be deleted
	// from this map. The two-phase lock() method depends on pointers remaining
	// valid after the global mutex is released. See lock() for details.
	locks map[string]*sync.Mutex //nolint:unused // Wave 2 seam
}

// lock acquires a per-plugin mutex and returns a function that releases it.
//
//nolint:unused // Wave 2 seam: install/uninstall flows will call this.
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

// service is the default implementation of the Phase-2 plugin surface.
// It implements Validate/Build/Push/ListBuilds/DeleteBuild/GetContent, which
// together satisfy the narrowed plugins.PluginService interface. Phase 3 will
// add the install/uninstall/list/info methods and widen the interface.
type service struct {
	locks         pluginLock
	store         storage.PluginStore
	groupManager  groups.Manager
	materializers map[string]plugins.MaterializationAdapter
	installer     skills.Installer
	ociStore      *ociplugins.Store
	packager      ociplugins.PluginPackager
	registry      ociplugins.RegistryClient
	pluginLookup  PluginLookup
	gitResolver   gitresolver.Resolver
}

// New creates a new plugin service and returns it as a plugins.PluginService
// (the narrowed Phase-2 interface exposing only
// Validate/Build/Push/ListBuilds/DeleteBuild/GetContent). Phase 3 (#5527)
// widens the interface and concrete type together to add install/uninstall/
// list/info; callers that need persistence then pass a WithStore option (added
// in that PR).
func New(opts ...Option) plugins.PluginService {
	s := &service{
		locks: pluginLock{locks: make(map[string]*sync.Mutex)},
	}
	for _, o := range opts {
		o(s)
	}
	if s.installer == nil {
		s.installer = skills.NewInstaller()
	}
	if s.gitResolver == nil {
		s.gitResolver = gitresolver.NewResolver()
	}
	return s
}
