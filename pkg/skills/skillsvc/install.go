// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// Install installs a skill. When the Name field contains an OCI reference
// (detected by the presence of '/', ':', or '@'), the artifact is pulled from
// the registry and extracted. When LayerData is provided, the skill is extracted
// to disk and a full installation record is created. Without LayerData, a
// pending record is created.
func (s *service) Install(ctx context.Context, opts skills.InstallOptions) (*skills.InstallResult, error) {
	// Captured before any internal resolution (version splicing, registry
	// lookup, git/OCI dispatch) mutates a *local* copy of opts.Name in the
	// functions below. This is the RFC THV-0080 lock entry "source": exactly
	// what the caller asked for, preserved verbatim so upgrade can re-resolve
	// the same input later.
	originalName := opts.Name

	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	scope = defaultScope(scope)
	// Canonicalize the project root so that equivalent paths produce
	// the same lock key and DB record.
	opts.ProjectRoot = projectRoot

	// Git references (git://host/owner/repo[@ref][#path]) are dispatched first;
	// the prefix is unambiguous and cannot collide with OCI references.
	if gitresolver.IsGitReference(opts.Name) {
		result, err := s.installFromGit(ctx, opts, scope)
		if err != nil {
			return nil, err
		}
		return s.installAndRegister(ctx, opts, originalName, result, opts.Group, result.Skill.Metadata.Name, scope)
	}

	// When the caller supplies `version` separately and the name is a tag-less
	// OCI-like reference (contains '/' but no ':' or '@'), splice the version
	// in as the tag. Without this, parseOCIReference + qualifiedOCIRef would
	// default the pull to ":latest" and silently drop opts.Version. An
	// explicit tag in the name still wins (we only splice when none is set).
	if opts.Version != "" &&
		strings.ContainsRune(opts.Name, '/') &&
		!strings.ContainsAny(opts.Name, ":@") {
		opts.Name = opts.Name + ":" + opts.Version
	}

	ref, isOCI, err := parseOCIReference(opts.Name)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("invalid OCI reference %q: %w", opts.Name, err),
			http.StatusBadRequest,
		)
	}
	if isOCI {
		result, ociErr := s.installFromOCI(ctx, opts, scope, ref)
		if ociErr == nil {
			return s.installAndRegister(ctx, opts, originalName, result, opts.Group, opts.Name, scope)
		}
		// OCI pull failed — fall back to registry lookup for names that look
		// like a qualified "namespace/name". Names that are unambiguously OCI
		// (digest, explicit tag, or multi-segment path) must not trigger a
		// registry search. See isUnambiguousOCIRef for the full rule set.
		if isUnambiguousOCIRef(opts.Name, ref) {
			return nil, ociErr
		}
		slog.Debug("OCI pull failed, attempting registry fallback", "name", opts.Name, "error", ociErr)
		resolved, regErr := s.resolveFromRegistry(opts.Name)
		if regErr != nil {
			return nil, regErr
		}
		if resolved != nil {
			return s.installFromResolvedRegistry(ctx, opts, originalName, scope, resolved)
		}
		return nil, ociErr
	}

	// Plain skill name — validate and proceed with existing flow.
	if err := skills.ValidateSkillName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	return s.installByName(ctx, opts, originalName, scope)
}

// installByName handles installation for a validated plain skill name. It
// checks the local OCI store and registry before falling back to an error.
func (s *service) installByName(
	ctx context.Context,
	opts skills.InstallOptions,
	originalName string,
	scope skills.Scope,
) (*skills.InstallResult, error) {
	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()

	// Without layer data, check the local OCI store for a matching tag,
	// then the registry/index, before returning an error.
	if len(opts.LayerData) == 0 {
		resolved := false
		if s.ociStore != nil {
			var resolveErr error
			// Pass pointer to hydrate opts with layer data, digest, and version.
			resolved, resolveErr = s.resolveFromLocalStore(ctx, &opts)
			if resolveErr != nil {
				return nil, resolveErr
			}
		}
		if !resolved {
			// Release lock before registry lookup -- installFromOCI
			// acquires its own lock on the artifact's skill name, which
			// could be the same key, causing deadlock since sync.Mutex
			// is not re-entrant.
			unlock()
			locked = false

			return s.installFromRegistryLookup(ctx, opts, originalName, scope)
		}
		// resolved: opts hydrated, fall through to installWithExtraction
	}

	result, err := s.installWithExtraction(ctx, opts, scope)
	if err != nil {
		return nil, err
	}
	return s.installAndRegister(ctx, opts, originalName, result, opts.Group, opts.Name, scope)
}

// installFromRegistryLookup resolves a plain skill name via the registry and
// dispatches to the appropriate installer (OCI or git).
func (s *service) installFromRegistryLookup(
	ctx context.Context,
	opts skills.InstallOptions,
	originalName string,
	scope skills.Scope,
) (*skills.InstallResult, error) {
	resolved, regErr := s.resolveFromRegistry(opts.Name)
	if regErr != nil {
		return nil, regErr
	}
	if resolved != nil {
		return s.installFromResolvedRegistry(ctx, opts, originalName, scope, resolved)
	}

	return nil, httperr.WithCode(
		fmt.Errorf("skill %q not found in local store or registry;"+
			" install by OCI reference:\n  thv skill install ghcr.io/<namespace>/%s:<version>",
			opts.Name, opts.Name),
		http.StatusNotFound,
	)
}

// installFromResolvedRegistry dispatches an install to the appropriate
// backend (OCI or git) based on the result of a registry lookup.
func (s *service) installFromResolvedRegistry(
	ctx context.Context,
	opts skills.InstallOptions,
	originalName string,
	scope skills.Scope,
	resolved *registryResolveResult,
) (*skills.InstallResult, error) {
	switch {
	case resolved.OCIRef != nil:
		slog.Info("resolved skill from registry (OCI)", "name", opts.Name, "oci_reference", resolved.OCIRef.String())
		opts.Name = resolved.OCIRef.String()
		result, ociErr := s.installFromOCI(ctx, opts, scope, resolved.OCIRef)
		if ociErr != nil {
			return nil, ociErr
		}
		// Use the skill name extracted from the artifact, not opts.Name which
		// holds the OCI ref string. installFromOCI mutates its own copy of opts
		// (Go pass-by-value), so the caller never sees the updated name.
		return s.installAndRegister(ctx, opts, originalName, result, opts.Group, result.Skill.Metadata.Name, scope)
	case resolved.GitURL != "":
		slog.Info("resolved skill from registry (git)", "name", opts.Name, "git_url", resolved.GitURL)
		opts.Name = resolved.GitURL
		result, gitErr := s.installFromGit(ctx, opts, scope)
		if gitErr != nil {
			return nil, gitErr
		}
		return s.installAndRegister(ctx, opts, originalName, result, opts.Group, result.Skill.Metadata.Name, scope)
	}
	return nil, httperr.WithCode(
		fmt.Errorf("skill %q resolved from registry but has no installable package", opts.Name),
		http.StatusUnprocessableEntity,
	)
}

// registerSkillInGroup adds the skill to the requested group when a group
// manager is configured. When groupName is empty it defaults to the
// "default" group, matching workload behavior.
func (s *service) registerSkillInGroup(ctx context.Context, groupName string, skillName string) error {
	if s.groupManager == nil {
		return nil
	}
	if groupName == "" {
		groupName = groups.DefaultGroup
	}
	return groups.AddSkillToGroup(ctx, s.groupManager, groupName, skillName)
}

// installAndRegister registers the just-installed skill in the target group
// and, for project-scope installs with the lock file feature enabled (see
// skills.LockFileFeatureEnabled), records it — and any toolhive.requires
// dependencies — in the project's toolhive.lock.yaml. If group registration
// or the lock write fails, the DB record and lock entry are rolled back to
// their pre-install state: restored when this call updated a pre-existing
// record (a --force reinstall must not be destroyed by a transient failure),
// deleted — with a dependency cascade cleaning up any freshly materialized
// orphans — when this call created them.
func (s *service) installAndRegister(
	ctx context.Context,
	opts skills.InstallOptions,
	originalName string,
	result *skills.InstallResult,
	groupName string,
	skillName string,
	scope skills.Scope,
) (*skills.InstallResult, error) {
	lockScoped := scope == skills.ScopeProject && skills.LockFileFeatureEnabled()

	// Snapshot the prior lock entry before anything below can write one, so
	// rollback can reinstate it (RequiredBy links from other parents
	// included) rather than blindly deleting it.
	var prevEntry *lockfile.Entry
	if lockScoped {
		if root, rootErr := lockfile.OpenRoot(opts.ProjectRoot); rootErr == nil {
			if lf, loadErr := lockfile.Load(root); loadErr == nil {
				if e, ok := lf.Get(skillName); ok {
					prevEntry = &e
				}
			}
		}
	}

	rollback := func() { s.rollbackInstall(ctx, opts, result, skillName, scope, lockScoped, prevEntry) }

	if err := s.registerSkillInGroup(ctx, groupName, skillName); err != nil {
		// Best-effort rollback. Files on disk are left in place; a fresh
		// install will detect them and either overwrite (force) or return a
		// conflict.
		rollback()
		return nil, fmt.Errorf("registering skill in group: %w", err)
	}

	if lockScoped {
		updated, err := s.recordLockState(ctx, opts, originalName, result.Skill)
		if err != nil {
			rollback()
			// Preserve a specific code already attached deeper in the chain
			// — dependency materialization runs inside recordLockState, so a
			// dep's 502 (git resolve) or 404 (registry miss) must reach the
			// API boundary as itself, not masked to 500. Only a code-less
			// failure (e.g. an actual lock write error) defaults to 500.
			wrapped := fmt.Errorf("recording skill in project lock file: %w", err)
			var coded *httperr.CodedError
			if !errors.As(err, &coded) {
				wrapped = httperr.WithCode(wrapped, http.StatusInternalServerError)
			}
			return nil, wrapped
		}
		result.Skill = updated
	}

	return result, nil
}

// rollbackInstall undoes installAndRegister's side effects after a failure,
// best-effort. The DB record is restored to its pre-install snapshot when
// one exists (result.PreExisting) and deleted otherwise; the lock entry is
// likewise reinstated from prevEntry or removed. When this call created the
// entry, removal runs the same dependency cascade as uninstall so that
// freshly materialized dependencies — installed, marked managed, and
// required only by the now-rolled-back skill — do not leak as orphans,
// while pre-existing dependencies with other parents (or explicit installs)
// survive with this skill stripped from their RequiredBy.
func (s *service) rollbackInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	result *skills.InstallResult,
	skillName string,
	scope skills.Scope,
	lockScoped bool,
	prevEntry *lockfile.Entry,
) {
	if result.PreExisting != nil {
		_ = s.store.Update(ctx, *result.PreExisting)
	} else {
		_ = s.store.Delete(ctx, skillName, scope, opts.ProjectRoot)
	}

	if !lockScoped {
		return
	}
	if prevEntry != nil {
		if root, err := lockfile.OpenRoot(opts.ProjectRoot); err == nil {
			_ = lockfile.UpsertEntry(root, *prevEntry)
		}
		return
	}
	uninstallOpts := skills.UninstallOptions{Name: skillName, Scope: scope, ProjectRoot: opts.ProjectRoot}
	candidates, err := removeLockEntry(uninstallOpts)
	if err != nil {
		return
	}
	visited := map[string]struct{}{skillName: {}}
	_ = s.cascadeUninstall(ctx, candidates, visited, opts.ProjectRoot, scope)
}
