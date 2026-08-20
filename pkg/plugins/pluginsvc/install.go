// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// Install installs a plugin. When the Name field contains a git reference
// (git://...), the repo is cloned and the plugin tree is built in memory. When
// it contains an OCI reference, the artifact is pulled and extracted. A plain
// name is resolved against the local OCI store, then the registry lookup.
// Structural mirror of skillsvc.Install, substituting the plugin install
// backends — but the failure semantics deliberately diverge: skills discards
// rollback errors and fails forward, while plugins joins every compensation
// error with the trigger and can abort (see rollbackInstall).
func (s *service) Install(ctx context.Context, opts plugins.InstallOptions) (*plugins.InstallResult, error) {
	return s.install(ctx, opts, false)
}

// installAlreadyLocked is for sync/upgrade while the per-plugin lock is held.
func (s *service) installAlreadyLocked(ctx context.Context, opts plugins.InstallOptions) (*plugins.InstallResult, error) {
	return s.install(ctx, opts, true)
}

func (s *service) install(
	ctx context.Context, opts plugins.InstallOptions, alreadyLocked bool,
) (*plugins.InstallResult, error) {
	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	scope = defaultScope(scope)
	opts.ProjectRoot = projectRoot
	if opts.LockSource == "" {
		opts.LockSource = opts.Name
	}

	// Git references are dispatched first; the prefix is unambiguous and
	// cannot collide with OCI references. installFromGit holds the per-plugin
	// lock across extraction, DB, group, lock-file, and rollback unless the
	// caller already holds it (alreadyLocked).
	if gitresolver.IsGitReference(opts.Name) {
		return s.installFromGit(ctx, opts, scope, alreadyLocked)
	}

	// Splice opts.Version as the tag for tag-less OCI-like references.
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
		// installFromOCI holds the per-plugin lock across extraction, DB,
		// group, lock-file, and rollback unless the caller already holds it.
		return s.installFromOCI(ctx, opts, scope, ref, alreadyLocked)
	}

	// Plain plugin name.
	if err := plugins.ValidatePluginName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	return s.installByName(ctx, opts, scope, alreadyLocked)
}

// validateExpectedCanonicalName rejects an install whose resolved plugin name
// differs from the lock entry identity Sync/Upgrade is repairing.
func validateExpectedCanonicalName(opts plugins.InstallOptions) error {
	if opts.ExpectedCanonicalName == "" || opts.Name == opts.ExpectedCanonicalName {
		return nil
	}
	return httperr.WithCode(
		fmt.Errorf(
			"plugin name %q does not match lock entry name %q",
			opts.Name, opts.ExpectedCanonicalName,
		),
		http.StatusUnprocessableEntity,
	)
}

// installByName handles installation for a validated plain plugin name. It
// checks the local OCI store, then the registry lookup, before returning an
// error. Structural mirror of skillsvc.installByName (failure semantics
// diverge — see Install).
func (s *service) installByName(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	alreadyLocked bool,
) (*plugins.InstallResult, error) {
	if !alreadyLocked {
		var unlock func()
		ctx, unlock = s.lockPlugin(ctx, opts.Name, scope, opts.ProjectRoot)
		defer unlock()
	}
	// Lock is held from here (by us or the caller). Nested OCI/registry
	// backends must not re-acquire.
	const lockHeld = true

	if len(opts.LayerData) == 0 {
		resolved := false
		if s.ociStore != nil {
			var resolveErr error
			resolved, resolveErr = s.resolveFromLocalStore(ctx, &opts)
			if resolveErr != nil {
				return nil, resolveErr
			}
		}
		if !resolved {
			return s.installFromRegistryLookup(ctx, opts, scope, lockHeld)
		}
	}

	result, err := s.installWithExtraction(ctx, opts, scope)
	if err != nil {
		return nil, err
	}
	return s.installAndRegister(ctx, opts, result, scope)
}

// installFromRegistryLookup resolves a plain plugin name via the registry
// lookup and dispatches to the appropriate installer. When no lookup is
// configured or it returns no hits, returns a 404 with an install hint.
//
// Resolution mirrors skillsvc.resolveFromRegistry exactly:
//   - a lookup error is logged and treated as "not found" (fall back to the
//     404 hint) rather than propagated, and
//   - search hits are post-filtered for an exact, case-insensitive name match
//     (PluginSearchHit carries Name only, so there is no namespace filter).
//
// When opts.Version is set, exact-name matches are further narrowed to those
// whose Version equals opts.Version; no version match falls through to the 404
// not-found path (which includes the requested version in the message).
//
// Multiple exact matches are ambiguous and produce a 409 listing candidates.
// Package selection mirrors skillsvc.resolveRegistryPackages: the first OCI
// package (Type == "oci") is chosen; when none exists, a 422 is returned.
func (s *service) installFromRegistryLookup(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	alreadyLocked bool,
) (*plugins.InstallResult, error) {
	if s.pluginLookup != nil {
		// Use the last path segment as the search query (matching
		// skillsvc.resolveFromRegistry's splitQualifiedName), since
		// SearchPlugins matches on name substring.
		namespace, searchName := splitQualifiedName(opts.Name)

		hits, err := s.pluginLookup.SearchPlugins(ctx, searchName)
		if err != nil {
			slog.Warn("registry plugin lookup failed, falling back to not-found", "name", opts.Name, "error", err)
			hits = nil
		}

		// Filter for exact match. Case-insensitive because registry data
		// may not be normalized to lowercase even though local plugin names are.
		var matches []PluginSearchHit
		for _, hit := range hits {
			if !strings.EqualFold(hit.Name, searchName) {
				continue
			}
			if namespace != "" && !strings.EqualFold(hit.Namespace, namespace) {
				continue
			}
			matches = append(matches, hit)
		}

		// When a version was requested, narrow the exact-name matches to those
		// whose Version equals opts.Version (string equality). A hit that does
		// not carry a Version cannot satisfy a versioned request. No match
		// falls through to the 404 not-found path below.
		if opts.Version != "" {
			var versioned []PluginSearchHit
			for _, m := range matches {
				if m.Version == opts.Version {
					versioned = append(versioned, m)
				}
			}
			matches = versioned
		}

		if len(matches) == 1 {
			return s.installFromRegistryHit(ctx, opts, scope, matches[0], alreadyLocked)
		}

		if len(matches) > 1 {
			return nil, ambiguousPluginNameError(opts.Name, matches)
		}
	}

	if opts.Version != "" {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin %q version %q not found in local store or registry;"+
				" install by OCI reference:\n  thv ai-plugin install ghcr.io/<namespace>/%s:%s",
				opts.Name, opts.Version, opts.Name, opts.Version),
			http.StatusNotFound,
		)
	}
	return nil, httperr.WithCode(
		fmt.Errorf("plugin %q not found in local store or registry;"+
			" install by OCI reference:\n  thv ai-plugin install ghcr.io/<namespace>/%s:<version>",
			opts.Name, opts.Name),
		http.StatusNotFound,
	)
}

// installFromRegistryHit selects the OCI package from a single resolved hit,
// parses its reference, and dispatches to installFromOCI. It guards against a
// malformed catalog package whose Reference is not structurally an OCI
// reference (parseOCIReference returns (nil, false, nil) for such input),
// which would otherwise nil-deref in validateOCIRegistryHost.
func (s *service) installFromRegistryHit(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	hit PluginSearchHit,
	alreadyLocked bool,
) (*plugins.InstallResult, error) {
	pkg, pkgErr := selectOCIPluginPackage(opts.Name, hit.Packages)
	if pkgErr != nil {
		return nil, pkgErr
	}
	slog.Info("resolved plugin from registry", "name", opts.Name, "reference", pkg.Reference)
	opts.Name = pkg.Reference
	ref, isOCIRef, parseErr := parseOCIReference(pkg.Reference)
	if parseErr != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("registry returned invalid OCI reference %q: %w", pkg.Reference, parseErr),
			http.StatusUnprocessableEntity,
		)
	}
	if !isOCIRef || ref == nil {
		return nil, httperr.WithCode(
			fmt.Errorf("registry returned invalid OCI reference %q", pkg.Reference),
			http.StatusUnprocessableEntity,
		)
	}
	return s.installFromOCI(ctx, opts, scope, ref, alreadyLocked)
}

// selectOCIPluginPackage selects the first OCI package from a registry entry's
// package list. Mirror of skillsvc.resolveRegistryPackages' OCI branch: OCI
// packages are preferred, and a missing OCI package yields a 422 error.
func selectOCIPluginPackage(name string, packages []PluginPackage) (PluginPackage, error) {
	for _, pkg := range packages {
		if pkg.Type == "oci" && pkg.Reference != "" {
			return pkg, nil
		}
	}
	return PluginPackage{}, httperr.WithCode(
		fmt.Errorf("plugin %q found in registry but has no installable OCI package", name),
		http.StatusUnprocessableEntity,
	)
}

// ambiguousPluginNameError builds a 409 error listing each ambiguous match,
// capped at 5 candidates with an "and N more" suffix. Mirror of
// skillsvc.resolveFromRegistry's ambiguity path.
func ambiguousPluginNameError(name string, matches []PluginSearchHit) error {
	const maxCandidates = 5
	candidates := make([]string, 0, len(matches))
	for _, m := range matches {
		candidates = append(candidates, m.Name)
	}
	suffix := ""
	if len(candidates) > maxCandidates {
		suffix = fmt.Sprintf(" and %d more", len(candidates)-maxCandidates)
		candidates = candidates[:maxCandidates]
	}
	return httperr.WithCode(
		fmt.Errorf("ambiguous plugin name %q matches multiple registry entries: %s%s; install by full OCI reference instead",
			name, strings.Join(candidates, ", "), suffix),
		http.StatusConflict,
	)
}

// splitQualifiedName splits "namespace/name" into (namespace, name). If the
// input has no "/" it returns ("", name) unchanged. Plugin names validated by
// ValidatePluginName never contain "/", so this is a structural mirror of
// skillsvc.splitQualifiedName that is a no-op for valid plugin names.
func splitQualifiedName(s string) (namespace, name string) {
	idx := strings.LastIndex(s, "/")
	if idx < 0 {
		return "", s
	}
	return s[:idx], s[idx+1:]
}

// registerPluginInGroup adds the plugin to the requested group when a group
// manager is configured. When groupName is empty it defaults to the "default"
// group, matching workload behavior. The bool reports whether this call
// inserted the name, so rollback can remove it only then.
func (s *service) registerPluginInGroup(ctx context.Context, groupName string, pluginName string) (bool, error) {
	if s.groupManager == nil {
		return false, nil
	}
	if groupName == "" {
		groupName = groups.DefaultGroup
	}
	return groups.AddPluginToGroup(ctx, s.groupManager, groupName, pluginName)
}

func resolvedGroupName(groupName string) string {
	if groupName == "" {
		return groups.DefaultGroup
	}
	return groupName
}

// installAndRegister registers the just-installed plugin in the target group
// and, for project-scope installs with the lock file feature enabled (see
// plugins.LockFileFeatureEnabled), records it in the project's
// toolhive.lock.yaml plugins: key. If group registration or the lock write
// fails, the DB record, on-disk files, group membership (only when this call
// added it), and lock entry are rolled back to their pre-install state:
// restored when this call updated a pre-existing record (a --force reinstall
// must not be destroyed by a transient failure), deleted/dematerialized when
// this call created them. Callers must hold the per-plugin lock for the
// duration of this call.
func (s *service) installAndRegister(
	ctx context.Context,
	opts plugins.InstallOptions,
	result *plugins.InstallResult,
	scope plugins.Scope,
) (*plugins.InstallResult, error) {
	pluginName := result.Plugin.Metadata.Name
	lockScoped := scope == plugins.ScopeProject && plugins.LockFileFeatureEnabled()

	// Snapshot the prior plugins: lock entry before anything below can write
	// one, so rollback can reinstate it rather than blindly deleting it.
	// OpenRoot/Load failures are fatal: treating them as "no previous pin"
	// would delete a pre-existing entry on compensation. Extraction has
	// already mutated DB/files, so compensate those even when the lock
	// snapshot itself fails.
	var prevEntry *lockfile.Entry
	if lockScoped {
		root, rootErr := lockfile.OpenRoot(opts.ProjectRoot)
		if rootErr != nil {
			return nil, errors.Join(
				fmt.Errorf("opening lock file root: %w", rootErr),
				s.rollbackInstall(ctx, result, rollbackParams{}),
			)
		}
		lf, loadErr := lockfile.Load(root)
		if loadErr != nil {
			return nil, errors.Join(
				fmt.Errorf("loading lock file: %w", loadErr),
				s.rollbackInstall(ctx, result, rollbackParams{}),
			)
		}
		if e, ok := lf.GetPlugin(pluginName); ok {
			prevEntry = &e
		}
	}

	var addedToGroup bool
	groupName := resolvedGroupName(opts.Group)
	rollback := func() error {
		return s.rollbackInstall(ctx, result, rollbackParams{
			lockScoped:   lockScoped,
			prevEntry:    prevEntry,
			addedToGroup: addedToGroup,
			groupName:    groupName,
		})
	}

	added, err := s.registerPluginInGroup(ctx, opts.Group, pluginName)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("registering plugin in group: %w", err), rollback())
	}
	addedToGroup = added

	if lockScoped {
		updated, err := s.recordLockState(ctx, opts, result.Plugin, result.ContentDigest)
		if err != nil {
			return nil, httperr.WithCode(
				errors.Join(fmt.Errorf("recording plugin in project lock file: %w", err), rollback()),
				http.StatusInternalServerError,
			)
		}
		result.Plugin = updated
	}

	return result, nil
}

// rollbackParams carries the compensation state rollbackInstall needs beyond
// what the install result itself provides. Name, scope, and project root are
// derived from result.Plugin.
type rollbackParams struct {
	lockScoped   bool
	prevEntry    *lockfile.Entry
	addedToGroup bool
	groupName    string
}

// rollbackInstall undoes installAndRegister's side effects after a failure.
// Every compensation error is returned so the caller can join it with the
// original failure; discarding it can hide a partial restore after a
// destructive rewrite.
func (s *service) rollbackInstall(
	ctx context.Context,
	result *plugins.InstallResult,
	params rollbackParams,
) error {
	pluginName := result.Plugin.Metadata.Name
	scope := result.Plugin.Scope
	projectRoot := result.Plugin.ProjectRoot

	var errs []error
	if result.PreExisting != nil {
		if err := s.store.Update(ctx, *result.PreExisting); err != nil {
			errs = append(errs, fmt.Errorf("restoring pre-existing DB record: %w", err))
		}
	} else if err := s.store.Delete(ctx, pluginName, scope, projectRoot); err != nil {
		errs = append(errs, fmt.Errorf("deleting rolled-back DB record: %w", err))
	}

	if result.RestoreFiles != nil {
		if err := result.RestoreFiles(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if params.addedToGroup && s.groupManager != nil {
		if err := groups.RemovePluginFromGroup(ctx, s.groupManager, params.groupName, pluginName); err != nil {
			errs = append(errs, fmt.Errorf("removing plugin from group: %w", err))
		}
	}

	if !params.lockScoped {
		return errors.Join(errs...)
	}
	if params.prevEntry != nil {
		root, err := lockfile.OpenRoot(projectRoot)
		if err != nil {
			return errors.Join(append(errs, fmt.Errorf("reopening lock file: %w", err))...)
		}
		if err := lockfile.UpsertPluginEntry(root, *params.prevEntry); err != nil {
			errs = append(errs, fmt.Errorf("restoring lock entry: %w", err))
		}
		return errors.Join(errs...)
	}
	if err := removeLockEntry(plugins.UninstallOptions{
		Name: pluginName, Scope: scope, ProjectRoot: projectRoot,
	}); err != nil {
		errs = append(errs, fmt.Errorf("removing rolled-back lock entry: %w", err))
	}
	return errors.Join(errs...)
}
