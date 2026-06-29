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
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
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

// WithStore sets the persistent plugin store. Phase 2 does not read the store
// (its methods use only ociStore/packager/registry), so this is optional and
// nil by default; Phase 3 (#5527) install/uninstall/list/info flows will use
// it. Passing it now is a no-op but keeps the field populated for Phase 3.
func WithStore(store storage.PluginStore) Option {
	return func(s *service) {
		s.store = store
	}
}

// Phase 2 does NOT include WithPathResolver/WithInstaller/WithGroupManager/
// WithSkillLookup/WithGitResolver — those are Phase 3 (install/uninstall/info
// flows). Adding them later is a non-breaking change (new options only).
//
// The per-(scope,name,projectRoot) lock (mirroring skillsvc.skillLock) is also
// Phase 3: Phase 2 has no install/uninstall path that needs mutual exclusion.

// service is the default implementation of the Phase-2 plugin surface.
// It implements Validate/Build/Push/ListBuilds/DeleteBuild/GetContent, which
// together satisfy the narrowed plugins.PluginService interface. Phase 3 will
// add the install/uninstall/list/info methods and widen the interface.
//
// The store field is retained for Phase 3 (install/uninstall/list/info) but is
// unused by Phase-2 logic, hence nil by default.
type service struct {
	store    storage.PluginStore //nolint:unused // retained for Phase 3 install/uninstall flows
	ociStore *ociplugins.Store
	packager ociplugins.PluginPackager
	registry ociplugins.RegistryClient
}

// New creates a new plugin service and returns it as a plugins.PluginService
// (the narrowed Phase-2 interface exposing only
// Validate/Build/Push/ListBuilds/DeleteBuild/GetContent). Phase 3 (#5527)
// widens the interface and concrete type together to add install/uninstall/
// list/info; callers that need persistence then pass WithStore.
func New(opts ...Option) plugins.PluginService {
	s := &service{}
	for _, o := range opts {
		o(s)
	}
	return s
}
