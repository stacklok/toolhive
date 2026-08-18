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

	nameref "github.com/google/go-containerregistry/pkg/name"

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
//
// Project-scope installs acquire the project transaction for opts.ProjectRoot
// before any resolve/mutate work and hold it through bookkeeping,
// dependencies, and compensation. User-scope installs acquire a per-skill
// lock after the canonical name is known (inside git/OCI/by-name backends).
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

	// When the caller supplies `version` separately and the name is a tag-less
	// OCI-like reference (contains '/' but no ':' or '@'), splice the version
	// in as the tag. Without this, parseOCIReference + qualifiedOCIRef would
	// default the pull to ":latest" and silently drop opts.Version. An
	// explicit tag in the name still wins (we only splice when none is set).
	// Git references are unaffected: the git:// prefix contains "://".
	if opts.Version != "" &&
		!gitresolver.IsGitReference(opts.Name) &&
		strings.ContainsRune(opts.Name, '/') &&
		!strings.ContainsAny(opts.Name, ":@") {
		opts.Name = opts.Name + ":" + opts.Version
	}

	if scope == skills.ScopeProject {
		unlock := s.projectTx.lock(opts.ProjectRoot)
		defer unlock()
		return s.installLocked(ctx, opts, originalName, scope, newDepState())
	}
	return s.installLocked(ctx, opts, originalName, scope, nil)
}

// installLocked performs Install assuming the appropriate lock is already
// held: the project transaction for ScopeProject, or nothing yet for
// ScopeUser (backends acquire the per-skill lock after resolving the
// canonical name). Sync, Upgrade, and dependency materialization call this
// directly so they never reacquire.
func (s *service) installLocked(
	ctx context.Context,
	opts skills.InstallOptions,
	originalName string,
	scope skills.Scope,
	deps *depState,
) (*skills.InstallResult, error) {
	alreadyLocked := scope == skills.ScopeProject
	return dispatchSource(ctx, s, opts.Name, sourceOps[*skills.InstallResult]{
		git: func(ctx context.Context, _ string) (*skills.InstallResult, error) {
			return s.installFromGit(ctx, &opts, scope, originalName, deps, alreadyLocked)
		},
		oci: func(ctx context.Context, ref nameref.Reference) (*skills.InstallResult, error) {
			result, err := s.installFromOCI(ctx, &opts, scope, originalName, deps, alreadyLocked, ref)
			if err != nil {
				slog.Debug("OCI pull failed, registry fallback may apply", "name", opts.Name, "error", err)
				return nil, err
			}
			return result, nil
		},
		registry: func(ctx context.Context, resolved *registryResolveResult) (*skills.InstallResult, error) {
			return s.installFromResolvedRegistry(ctx, opts, originalName, scope, resolved, deps, alreadyLocked)
		},
		plainName: func(ctx context.Context, _ string) (*skills.InstallResult, error) {
			return s.installByName(ctx, opts, originalName, scope, deps, alreadyLocked)
		},
	})
}

// installByName handles installation for a validated plain skill name. It
// checks the local OCI store and registry before falling back to an error.
func (s *service) installByName(
	ctx context.Context,
	opts skills.InstallOptions,
	originalName string,
	scope skills.Scope,
	deps *depState,
	alreadyLocked bool,
) (*skills.InstallResult, error) {
	if !alreadyLocked {
		unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
		defer unlock()
		alreadyLocked = true
	}

	// Without layer data, check the local OCI store for a matching tag,
	// then the registry/index, before returning an error. Registry/git/OCI
	// backends enter depState after resolving the canonical name — do not
	// enter here or a registry-resolved install would re-enter itself.
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
			return s.installFromRegistryLookup(ctx, opts, originalName, scope, deps, alreadyLocked)
		}
		// resolved: opts hydrated, fall through to installWithExtraction
	}

	if deps != nil {
		if deps.alreadyDone(opts.Name) {
			return s.mergeRequiredByOnly(ctx, opts, opts.Name, scope)
		}
		if err := deps.enter(opts.Name); err != nil {
			return nil, err
		}
		defer deps.leave(opts.Name)
	}

	if err := validateExpectedCanonicalName(opts); err != nil {
		return nil, err
	}

	// Local-store artifacts and raw layer data carry no registry signature
	// to verify — installing them project-scoped is an unsigned trust
	// decision that must be explicit.
	if shouldVerifyInstall(opts, scope) {
		decision, verifyErr := verifyLocalInstall(opts, opts.Name)
		if verifyErr != nil {
			return nil, verifyErr
		}
		applyDecisionToOpts(&opts, decision)
	}

	result, err := s.installWithExtraction(ctx, opts, scope)
	if err != nil {
		return nil, err
	}
	return s.installAndRegister(ctx, opts, originalName, result, opts.Group, opts.Name, scope, deps)
}

// installFromRegistryLookup resolves a plain skill name via the registry and
// dispatches to the appropriate installer (OCI or git).
func (s *service) installFromRegistryLookup(
	ctx context.Context,
	opts skills.InstallOptions,
	originalName string,
	scope skills.Scope,
	deps *depState,
	alreadyLocked bool,
) (*skills.InstallResult, error) {
	resolved, regErr := s.resolveFromRegistry(opts.Name)
	if regErr != nil {
		return nil, regErr
	}
	if resolved != nil {
		return s.installFromResolvedRegistry(ctx, opts, originalName, scope, resolved, deps, alreadyLocked)
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
	deps *depState,
	alreadyLocked bool,
) (*skills.InstallResult, error) {
	switch {
	case resolved.OCIRef != nil:
		slog.Info("resolved skill from registry (OCI)", "name", opts.Name, "oci_reference", resolved.OCIRef.String())
		opts.Name = resolved.OCIRef.String()
		return s.installFromOCI(ctx, &opts, scope, originalName, deps, alreadyLocked, resolved.OCIRef)
	case resolved.GitURL != "":
		slog.Info("resolved skill from registry (git)", "name", opts.Name, "git_url", resolved.GitURL)
		opts.Name = resolved.GitURL
		return s.installFromGit(ctx, &opts, scope, originalName, deps, alreadyLocked)
	}
	return nil, httperr.WithCode(
		fmt.Errorf("skill %q resolved from registry but has no installable package", opts.Name),
		http.StatusUnprocessableEntity,
	)
}

func resolvedGroupName(groupName string) string {
	if groupName == "" {
		return groups.DefaultGroup
	}
	return groupName
}

// registerSkillInGroup adds the skill to the requested group when a group
// manager is configured. When groupName is empty it defaults to the
// "default" group, matching workload behavior. The bool reports whether this
// call inserted the name, so rollback can remove it only then.
func (s *service) registerSkillInGroup(ctx context.Context, groupName string, skillName string) (bool, error) {
	if s.groupManager == nil {
		return false, nil
	}
	if groupName == "" {
		groupName = groups.DefaultGroup
	}
	return groups.AddSkillToGroup(ctx, s.groupManager, groupName, skillName)
}

// validateExpectedCanonicalName rejects installs whose resolved manifest
// name does not match a caller-supplied ExpectedCanonicalName (sync/upgrade
// pins). Call after opts.Name has been hydrated from the artifact/manifest.
func validateExpectedCanonicalName(opts skills.InstallOptions) error {
	if opts.ExpectedCanonicalName == "" || opts.Name == opts.ExpectedCanonicalName {
		return nil
	}
	return httperr.WithCode(
		fmt.Errorf("skill name %q does not match expected canonical name %q",
			opts.Name, opts.ExpectedCanonicalName),
		http.StatusUnprocessableEntity,
	)
}

// mergeRequiredByOnly updates the lock entry so RequiredBy includes the
// current parent without re-extracting an already-materialized dependency
// (diamond / shared-dep within one traversal).
func (s *service) mergeRequiredByOnly(
	ctx context.Context,
	opts skills.InstallOptions,
	skillName string,
	scope skills.Scope,
) (*skills.InstallResult, error) {
	existing, err := s.store.Get(ctx, skillName, scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	if opts.RequiredByParent == "" || scope != skills.ScopeProject {
		return &skills.InstallResult{Skill: existing}, nil
	}
	root, rootErr := lockfile.OpenRoot(opts.ProjectRoot)
	if rootErr != nil {
		return nil, rootErr
	}
	lf, loadErr := lockfile.Load(root)
	if loadErr != nil {
		return nil, loadErr
	}
	e, ok := lf.Get(skillName)
	if !ok {
		return &skills.InstallResult{Skill: existing}, nil
	}
	if err := recordLockEntry(opts.ProjectRoot, lockEntryInput{
		Name:              e.Name,
		Version:           e.Version,
		Source:            e.Source,
		ResolvedReference: e.ResolvedReference,
		Digest:            e.Digest,
		ContentDigest:     e.ContentDigest,
		Provenance:        e.Provenance,
		Unsigned:          e.Unsigned,
		RequiredByParent:  opts.RequiredByParent,
		PreserveExplicit:  true,
	}); err != nil {
		return nil, fmt.Errorf("merging RequiredBy for shared dependency %q: %w", skillName, err)
	}
	return &skills.InstallResult{Skill: existing}, nil
}

// installAndRegister registers the just-installed skill in the target group
// and, for project-scope installs with the lock file feature enabled (see
// records it — and any toolhive.requires
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
	deps *depState,
) (*skills.InstallResult, error) {
	lockScoped := scope == skills.ScopeProject
	// Surface the verification decision on the result so callers can show
	// what trust state this install recorded.
	result.Provenance = opts.Provenance
	result.Unsigned = opts.Unsigned

	// Snapshot the prior lock entry before anything below can write one, so
	// rollback can reinstate it (RequiredBy links from other parents
	// included) rather than blindly deleting it. OpenRoot/Load failures are
	// fatal: treating them as "no previous pin" would delete a pre-existing
	// entry on compensation.
	var prevEntry *lockfile.Entry
	if lockScoped {
		root, rootErr := lockfile.OpenRoot(opts.ProjectRoot)
		if rootErr != nil {
			return nil, errors.Join(
				fmt.Errorf("opening lock file root: %w", rootErr),
				s.rollbackInstall(ctx, opts, result, skillName, scope, false, nil, false, ""),
			)
		}
		lf, loadErr := lockfile.Load(root)
		if loadErr != nil {
			return nil, errors.Join(
				fmt.Errorf("loading lock file: %w", loadErr),
				s.rollbackInstall(ctx, opts, result, skillName, scope, false, nil, false, ""),
			)
		}
		if e, ok := lf.Get(skillName); ok {
			prevEntry = &e
		}
	}

	resolvedGroup := resolvedGroupName(groupName)
	var addedToGroup bool
	rollback := func() error {
		return s.rollbackInstall(ctx, opts, result, skillName, scope, lockScoped, prevEntry, addedToGroup, resolvedGroup)
	}

	added, err := s.registerSkillInGroup(ctx, groupName, skillName)
	if err != nil {
		// Rollback restores files from the pre-write snapshot and removes
		// freshly created trees; its errors join the trigger error so a
		// partial restore is never reported as clean.
		return nil, errors.Join(fmt.Errorf("registering skill in group: %w", err), rollback())
	}
	addedToGroup = added

	if lockScoped {
		updated, err := s.recordLockState(ctx, opts, originalName, result.Skill, deps)
		if err != nil {
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
			return nil, errors.Join(wrapped, rollback())
		}
		result.Skill = updated
	}

	return result, nil
}

// rollbackInstall undoes installAndRegister's side effects after a failure,
// best-effort. The DB record is restored to its pre-install snapshot when
// one exists (result.PreExisting) and deleted otherwise; overwritten files
// are restored when result.RestoreFiles is set; group membership added by
// this call is removed; the lock entry is likewise reinstated from prevEntry
// or removed. When this call created the entry, removal runs the same
// dependency cascade as uninstall so that freshly materialized dependencies
// — installed, marked managed, and required only by the now-rolled-back
// skill — do not leak as orphans, while pre-existing dependencies with other
// parents (or explicit installs) survive with this skill stripped from their
// RequiredBy.
func (s *service) rollbackInstall(
	ctx context.Context,
	opts skills.InstallOptions,
	result *skills.InstallResult,
	skillName string,
	scope skills.Scope,
	lockScoped bool,
	prevEntry *lockfile.Entry,
	addedToGroup bool,
	groupName string,
) error {
	var errs []error
	if result.PreExisting != nil {
		if err := s.store.Update(ctx, *result.PreExisting); err != nil {
			errs = append(errs, fmt.Errorf("restoring pre-existing DB record: %w", err))
		}
	} else {
		if err := s.store.Delete(ctx, skillName, scope, opts.ProjectRoot); err != nil {
			errs = append(errs, fmt.Errorf("deleting rolled-back DB record: %w", err))
		}
	}

	if result.RestoreFiles != nil {
		if err := result.RestoreFiles(); err != nil {
			errs = append(errs, err)
		}
	}

	if addedToGroup && s.groupManager != nil {
		if err := groups.RemoveSkillFromGroup(ctx, s.groupManager, groupName, skillName); err != nil {
			errs = append(errs, fmt.Errorf("removing skill from group: %w", err))
		}
	}

	if !lockScoped {
		return errors.Join(errs...)
	}
	if prevEntry != nil {
		if root, err := lockfile.OpenRoot(opts.ProjectRoot); err == nil {
			if err := lockfile.UpsertEntry(root, *prevEntry); err != nil {
				errs = append(errs, fmt.Errorf("restoring lock entry: %w", err))
			}
		} else {
			errs = append(errs, fmt.Errorf("reopening lock file: %w", err))
		}
		return errors.Join(errs...)
	}
	uninstallOpts := skills.UninstallOptions{Name: skillName, Scope: scope, ProjectRoot: opts.ProjectRoot}
	candidates, err := removeLockEntry(uninstallOpts)
	if err != nil {
		errs = append(errs, err)
		return errors.Join(errs...)
	}
	visited := map[string]struct{}{skillName: {}}
	if err := s.cascadeUninstall(ctx, candidates, visited, opts.ProjectRoot, scope); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
