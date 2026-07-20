// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skillsvc provides the default implementation of skills.SkillService.
package skillsvc

import (
	"sync"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/storage"
)

// Option configures the skill service.
type Option func(*service)

// WithPathResolver sets the path resolver for skill installations.
func WithPathResolver(pr skills.PathResolver) Option {
	return func(s *service) {
		s.pathResolver = pr
	}
}

// WithInstaller sets the installer for filesystem operations.
func WithInstaller(inst skills.Installer) Option {
	return func(s *service) {
		s.installer = inst
	}
}

// WithOCIStore sets the local OCI store for skill artifacts.
func WithOCIStore(store *ociskills.Store) Option {
	return func(s *service) {
		s.ociStore = store
	}
}

// WithPackager sets the skill packager for building OCI artifacts.
func WithPackager(p ociskills.SkillPackager) Option {
	return func(s *service) {
		s.packager = p
	}
}

// WithRegistryClient sets the registry client for push/pull operations.
func WithRegistryClient(rc ociskills.RegistryClient) Option {
	return func(s *service) {
		s.registry = rc
	}
}

// WithGroupManager sets the group manager for skill group membership.
func WithGroupManager(mgr groups.Manager) Option {
	return func(s *service) {
		s.groupManager = mgr
	}
}

// SkillLookup resolves a plain skill name against a registry/index.
// registry.Provider implicitly satisfies this interface.
type SkillLookup interface {
	SearchSkills(query string) ([]regtypes.Skill, error)
}

// WithSkillLookup sets the registry-based skill lookup for name resolution.
func WithSkillLookup(sl SkillLookup) Option {
	return func(s *service) {
		s.skillLookup = sl
	}
}

// WithGitResolver sets the git resolver for git:// skill installations.
func WithGitResolver(gr gitresolver.Resolver) Option {
	return func(s *service) {
		s.gitResolver = gr
	}
}

// skillLock provides per-skill mutual exclusion keyed by scope/name/projectRoot.
// Entries are never evicted. This is acceptable because the number of distinct
// skills on a single machine is expected to remain small (< 1000).
type skillLock struct {
	mu sync.Mutex
	// locks holds per-key mutexes. INVARIANT: entries must never be deleted
	// from this map. The two-phase lock() method depends on pointers remaining
	// valid after the global mutex is released. See lock() for details.
	locks map[string]*sync.Mutex
}

// lock acquires a per-skill mutex and returns a function that releases it.
func (sl *skillLock) lock(name string, scope skills.Scope, projectRoot string) func() {
	sl.mu.Lock()
	key := string(scope) + "/" + name + "/" + projectRoot
	m, ok := sl.locks[key]
	if !ok {
		m = &sync.Mutex{}
		sl.locks[key] = m
	}
	sl.mu.Unlock()

	m.Lock()
	return m.Unlock
}

// service is the default implementation of skills.SkillService.
type service struct {
	locks        skillLock
	store        storage.SkillStore
	groupManager groups.Manager
	pathResolver skills.PathResolver
	installer    skills.Installer
	ociStore     *ociskills.Store
	packager     ociskills.SkillPackager
	registry     ociskills.RegistryClient
	skillLookup  SkillLookup
	gitResolver  gitresolver.Resolver
}

// New creates a new SkillService backed by the given store.
func New(store storage.SkillStore, opts ...Option) skills.SkillService {
	s := &service{
		store: store,
		locks: skillLock{locks: make(map[string]*sync.Mutex)},
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
