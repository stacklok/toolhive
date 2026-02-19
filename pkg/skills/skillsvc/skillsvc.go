// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skillsvc provides the default implementation of skills.SkillService.
package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
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
	pathResolver skills.PathResolver
	installer    skills.Installer
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
	return s
}

// List returns all installed skills matching the given options.
func (s *service) List(ctx context.Context, opts skills.ListOptions) ([]skills.InstalledSkill, error) {
	filter := storage.ListFilter{
		Scope: opts.Scope,
	}
	return s.store.List(ctx, filter)
}

// Install installs a skill. When LayerData is provided, the skill is extracted
// to disk and a full installation record is created. Without LayerData, a
// pending record is created (backward-compatible with the OCI pull flow in #3650).
func (s *service) Install(ctx context.Context, opts skills.InstallOptions) (*skills.InstallResult, error) {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	scope := defaultScope(opts.Scope)

	// Canonicalize the project root so that equivalent paths
	// (e.g. trailing slash, ".." segments) produce the same lock key
	// and DB record.
	if opts.ProjectRoot != "" {
		opts.ProjectRoot = filepath.Clean(opts.ProjectRoot)
	}

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	// Without layer data, fall back to creating a pending record.
	if len(opts.LayerData) == 0 {
		return s.installPending(ctx, opts, scope)
	}

	return s.installWithExtraction(ctx, opts, scope)
}

// Uninstall removes an installed skill and cleans up files for all clients.
func (s *service) Uninstall(ctx context.Context, opts skills.UninstallOptions) error {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return httperr.WithCode(err, http.StatusBadRequest)
	}

	scope := defaultScope(opts.Scope)

	if opts.ProjectRoot != "" {
		opts.ProjectRoot = filepath.Clean(opts.ProjectRoot)
	}

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	// Look up the existing record to find which clients have files.
	existing, err := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return err
	}

	// Remove files for each client — best-effort: collect errors but don't
	// abort on the first failure so we clean up as much as possible.
	var cleanupErrs []error
	if s.pathResolver != nil {
		for _, clientType := range existing.Clients {
			skillPath, pathErr := s.pathResolver.GetSkillPath(clientType, opts.Name, scope, opts.ProjectRoot)
			if pathErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("resolving path for client %q: %w", clientType, pathErr))
				continue
			}
			if rmErr := s.installer.Remove(skillPath); rmErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("removing files for client %q: %w", clientType, rmErr))
			}
		}
	}

	if err := s.store.Delete(ctx, opts.Name, scope, opts.ProjectRoot); err != nil {
		return err
	}

	return errors.Join(cleanupErrs...)
}

// Info returns detailed information about a skill.
func (s *service) Info(ctx context.Context, opts skills.InfoOptions) (*skills.SkillInfo, error) {
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	skill, err := s.store.Get(ctx, opts.Name, defaultScope(opts.Scope), "")
	if err != nil {
		return nil, err
	}

	return &skills.SkillInfo{
		Metadata:       skill.Metadata,
		InstalledSkill: &skill,
	}, nil
}

// Validate checks whether a skill definition is valid.
func (*service) Validate(_ context.Context, path string) (*skills.ValidationResult, error) {
	return skills.ValidateSkillDir(path)
}

// Build is not yet implemented.
func (*service) Build(_ context.Context, _ skills.BuildOptions) (*skills.BuildResult, error) {
	return nil, httperr.WithCode(fmt.Errorf("not implemented"), http.StatusNotImplemented)
}

// Push is not yet implemented.
func (*service) Push(_ context.Context, _ skills.PushOptions) error {
	return httperr.WithCode(fmt.Errorf("not implemented"), http.StatusNotImplemented)
}

// installPending creates a pending skill record (no extraction).
func (s *service) installPending(
	ctx context.Context, opts skills.InstallOptions, scope skills.Scope,
) (*skills.InstallResult, error) {
	sk := skills.InstalledSkill{
		Metadata: skills.SkillMetadata{
			Name:    opts.Name,
			Version: opts.Version,
		},
		Scope:       scope,
		Status:      skills.InstallStatusPending,
		InstalledAt: time.Now().UTC(),
	}
	if err := s.store.Create(ctx, sk); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
}

// installWithExtraction handles the full install flow: managed/unmanaged
// detection, extraction, and DB record creation or update.
func (s *service) installWithExtraction(
	ctx context.Context, opts skills.InstallOptions, scope skills.Scope,
) (*skills.InstallResult, error) {
	if s.pathResolver == nil {
		return nil, httperr.WithCode(
			fmt.Errorf("path resolver is required for extraction-based installs"),
			http.StatusInternalServerError,
		)
	}

	clientType := s.resolveClient(opts.Client)

	targetDir, err := s.pathResolver.GetSkillPath(clientType, opts.Name, scope, opts.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving skill path: %w", err)
	}

	// Check store for existing managed record.
	existing, storeErr := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	isNotFound := errors.Is(storeErr, storage.ErrNotFound)

	switch {
	case storeErr != nil && !isNotFound:
		// Unexpected store error.
		return nil, fmt.Errorf("checking existing skill: %w", storeErr)

	case storeErr == nil && existing.Digest == opts.Digest:
		// Same digest — already installed, no-op.
		return &skills.InstallResult{Skill: existing}, nil

	case storeErr == nil:
		// Different digest — upgrade path.
		return s.upgradeSkill(ctx, opts, scope, clientType, targetDir, existing)

	default:
		// Not found in store — check for unmanaged directory.
		return s.freshInstall(ctx, opts, scope, clientType, targetDir)
	}
}

// upgradeSkill handles re-extraction when the digest differs from the stored record.
func (s *service) upgradeSkill(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	clientType, targetDir string,
	existing skills.InstalledSkill,
) (*skills.InstallResult, error) {
	if _, err := s.installer.Extract(opts.LayerData, targetDir, true); err != nil {
		return nil, fmt.Errorf("extracting skill upgrade: %w", err)
	}

	sk := buildInstalledSkill(opts, scope, clientType, existing.Clients)
	if err := s.store.Update(ctx, sk); err != nil {
		// Rollback: clean up extracted files since the store record wasn't updated.
		_ = s.installer.Remove(targetDir)
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
}

// freshInstall handles first-time installation when no store record exists.
func (s *service) freshInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	clientType, targetDir string,
) (*skills.InstallResult, error) {
	// Check for unmanaged directory on disk.
	if _, statErr := os.Stat(targetDir); statErr == nil && !opts.Force {
		return nil, httperr.WithCode(
			fmt.Errorf("directory %q exists but is not managed by ToolHive; use force to overwrite", targetDir),
			http.StatusConflict,
		)
	}

	if _, err := s.installer.Extract(opts.LayerData, targetDir, opts.Force); err != nil {
		return nil, fmt.Errorf("extracting skill: %w", err)
	}

	sk := buildInstalledSkill(opts, scope, clientType, nil)
	if err := s.store.Create(ctx, sk); err != nil {
		// Rollback: clean up extracted files since the store record wasn't created.
		_ = s.installer.Remove(targetDir)
		return nil, err
	}
	return &skills.InstallResult{Skill: sk}, nil
}

// resolveClient returns the provided client type, or falls back to the first
// skill-supporting client from the path resolver.
func (s *service) resolveClient(clientType string) string {
	if clientType != "" {
		return clientType
	}
	if s.pathResolver != nil {
		clients := s.pathResolver.ListSkillSupportingClients()
		if len(clients) > 0 {
			return clients[0]
		}
	}
	return ""
}

// buildInstalledSkill constructs an InstalledSkill from install options.
func buildInstalledSkill(
	opts skills.InstallOptions,
	scope skills.Scope,
	clientType string,
	existingClients []string,
) skills.InstalledSkill {
	clients := func() []string {
		if len(existingClients) > 0 {
			for _, c := range existingClients {
				if c == clientType {
					return existingClients
				}
			}
			// Defensive copy to avoid mutating the caller's slice.
			newClients := make([]string, len(existingClients), len(existingClients)+1)
			copy(newClients, existingClients)
			return append(newClients, clientType)
		}
		if clientType != "" {
			return []string{clientType}
		}
		return nil
	}()

	return skills.InstalledSkill{
		Metadata: skills.SkillMetadata{
			Name:    opts.Name,
			Version: opts.Version,
		},
		Scope:       scope,
		ProjectRoot: opts.ProjectRoot,
		Reference:   opts.Reference,
		Digest:      opts.Digest,
		Status:      skills.InstallStatusInstalled,
		InstalledAt: time.Now().UTC(),
		Clients:     clients,
	}
}

// defaultScope returns ScopeUser when s is empty, otherwise returns s unchanged.
func defaultScope(s skills.Scope) skills.Scope {
	if s == "" {
		return skills.ScopeUser
	}
	return s
}
