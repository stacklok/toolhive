// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skillsvc provides the default implementation of skills.SkillService.
package skillsvc

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"sync"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/signer"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
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
// Used for user-scope operations. Project-scope mutations serialize on
// projectTx instead, to avoid nested per-skill ABBA deadlocks.
//
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

// projectTxStripes bounds the project transaction lock set. Project roots
// are request-derived in the long-running API service, so a grow-forever
// map keyed by root would leak; a fixed stripe set caps memory at a
// constant. Two projects hashing to the same stripe merely serialize
// against each other — never a correctness issue.
const projectTxStripes = 64

// projectTx serializes all project-scoped skill mutations for a given
// canonical ProjectRoot. Different projects remain concurrent (up to stripe
// collisions); Install, Uninstall, Sync, and Upgrade for the same project
// share one transaction that spans extraction through bookkeeping,
// dependency materialization, cascades, and compensation.
type projectTx struct {
	stripes [projectTxStripes]sync.Mutex
}

// lock acquires the project transaction mutex for projectRoot's stripe and
// returns a release function.
func (p *projectTx) lock(projectRoot string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(projectRoot))
	m := &p.stripes[h.Sum32()%projectTxStripes]
	m.Lock()
	return m.Unlock
}

// depState tracks dependency traversal under a held project transaction.
// Cycle detection and diamond merging key on canonical skill names after
// source resolution — not on alias/reference strings — so alias cycles are
// rejected deterministically and shared deps still merge RequiredBy.
type depState struct {
	active    map[string]struct{} // currently installing (call stack)
	completed map[string]struct{} // finished in this traversal
}

func newDepState() *depState {
	return &depState{
		active:    make(map[string]struct{}),
		completed: make(map[string]struct{}),
	}
}

// enter marks canonical as actively installing. Returns an error when the
// name is already on the active stack (an alias/reference cycle).
func (d *depState) enter(canonical string) error {
	if d == nil {
		return nil
	}
	if _, inFlight := d.active[canonical]; inFlight {
		return httperr.WithCode(
			fmt.Errorf("dependency cycle detected involving skill %q", canonical),
			http.StatusUnprocessableEntity,
		)
	}
	d.active[canonical] = struct{}{}
	return nil
}

// leave removes canonical from the active stack and records it completed.
func (d *depState) leave(canonical string) {
	if d == nil {
		return
	}
	delete(d.active, canonical)
	d.completed[canonical] = struct{}{}
}

// alreadyDone reports whether canonical was fully installed earlier in this
// traversal (diamond / shared dependency).
func (d *depState) alreadyDone(canonical string) bool {
	if d == nil {
		return false
	}
	_, ok := d.completed[canonical]
	return ok
}

// service is the default implementation of skills.SkillService.
type service struct {
	locks        skillLock
	projectTx    projectTx
	store        storage.SkillStore
	groupManager groups.Manager
	pathResolver skills.PathResolver
	installer    skills.Installer
	ociStore     *ociskills.Store
	packager     ociskills.SkillPackager
	registry     ociskills.RegistryClient
	skillLookup  SkillLookup
	gitResolver  gitresolver.Resolver
	sigVerifier  verifier.Verifier
	sigSigner    signer.Signer
}

// WithSigner sets the artifact signer used by Push. Defaults to the
// Sigstore signer with the composite registry keychain.
func WithSigner(sg signer.Signer) Option {
	return func(s *service) {
		s.sigSigner = sg
	}
}

// WithVerifier sets the signature verifier used for install-time
// verification. Defaults to the Sigstore verifier with the composite
// registry keychain.
func WithVerifier(v verifier.Verifier) Option {
	return func(s *service) {
		s.sigVerifier = v
	}
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
