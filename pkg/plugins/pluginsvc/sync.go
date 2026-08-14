// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
)

// var _ ensures *service satisfies the lock service surface. Upgrade is a
// stub until the next PR in this stack lands the real implementation; the
// compile-time check still requires both methods so PluginsRouter's type
// assert succeeds and /sync can be served.
var _ plugins.PluginLockService = (*service)(nil)

// Sync restores a project's installed plugins to match its lock file: missing
// or drifted entries are reinstalled at their pinned digest (never
// re-resolved from source — see buildPinnedReference), unmanaged installs are
// reported (or adopted with Adopt), and lock-managed installs no longer in
// the lock file are reported (or removed with Prune). Check performs the
// same reconciliation read-only: nothing is installed, written, or removed.
func (s *service) Sync(ctx context.Context, opts plugins.SyncOptions) (*plugins.SyncResult, error) {
	if !plugins.LockFileFeatureEnabled() {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin lock file is not enabled; set %s=true", plugins.LockFileEnvVar),
			http.StatusForbidden,
		)
	}

	_, projectRoot, err := normalizeProjectRoot(plugins.ScopeProject, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	opts.ProjectRoot = projectRoot

	root, err := lockfile.OpenRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return nil, err
	}

	installed, err := s.store.List(ctx, storage.ListFilter{Scope: plugins.ScopeProject, ProjectRoot: projectRoot})
	if err != nil {
		return nil, fmt.Errorf("listing installed plugins: %w", err)
	}
	installedByName := make(map[string]plugins.InstalledPlugin, len(installed))
	for _, pl := range installed {
		installedByName[pl.Metadata.Name] = pl
	}

	result := &plugins.SyncResult{}
	for _, entry := range lf.Plugins {
		pl, dbOK := installedByName[entry.Name]
		s.syncLockedEntry(ctx, opts, entry, pl, dbOK, result)
	}
	for _, pl := range installed {
		if _, ok := lf.GetPlugin(pl.Metadata.Name); ok {
			continue // handled by the loop above
		}
		s.syncUnlockedInstall(ctx, opts, pl, result)
	}

	return result, nil
}

// Upgrade is implemented in the next PR of this stack. The stub exists so
// *service satisfies PluginLockService (and /sync can be type-asserted)
// without exposing a half-built upgrade path.
func (*service) Upgrade(_ context.Context, _ plugins.UpgradeOptions) (*plugins.UpgradeResult, error) {
	return nil, httperr.WithCode(errors.New("plugin upgrade is not implemented"), http.StatusNotImplemented)
}

// syncLockedEntry reconciles one lock file entry against installed state,
// appending its outcome to result. Missing (dbOK false) and drifted (digest
// or contentDigest mismatch) entries are reinstalled at the pinned reference
// unless opts.Check is set, in which case nothing is written — both states
// are still reported (Missing/Drifted), never as failures.
func (s *service) syncLockedEntry(
	ctx context.Context,
	opts plugins.SyncOptions,
	entry lockfile.Entry,
	pl plugins.InstalledPlugin,
	dbOK bool,
	result *plugins.SyncResult,
) {
	if dbOK && pl.Managed && s.entryMatchesInstalled(ctx, entry, pl, opts.Clients) {
		result.AlreadyCurrent = append(result.AlreadyCurrent, entry.Name)
		return
	}
	if dbOK {
		result.Drifted = append(result.Drifted, entry.Name)
	} else {
		result.Missing = append(result.Missing, entry.Name)
	}
	if opts.Check {
		return
	}
	if err := s.reinstallPinned(ctx, opts, entry, pl, dbOK); err != nil {
		result.Failed = append(result.Failed, plugins.SyncFailure{
			Name: entry.Name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}
	result.Installed = append(result.Installed, entry.Name)
}

// entryMatchesInstalled reports whether the installed plugin is lock-managed,
// its pinned digest still matches the lock entry, every requested client is
// present, EVERY client directory's on-disk contentDigest matches, and each
// adapter reports the plugin as healthy (marketplace/settings present).
// Checking only one client's copy would leave tampering with any other
// client's materialized files invisible to --check — and which directory got
// checked would depend on install order. Shared registration files are
// validated via adapter Health, not folded into contentDigest.
func (s *service) entryMatchesInstalled(
	ctx context.Context,
	entry lockfile.Entry,
	pl plugins.InstalledPlugin,
	requestedClients []string,
) bool {
	if !pl.Managed {
		return false
	}
	if pl.Digest != entry.Digest {
		return false
	}
	if len(pl.Clients) == 0 {
		return false
	}
	if len(requestedClients) > 0 && !clientsContainAll(pl.Clients, requestedClients) {
		return false
	}
	for _, clientType := range pl.Clients {
		dir, err := s.pluginInstallPath(clientType, pl.Metadata.Name, pl.Scope, pl.ProjectRoot)
		if err != nil {
			return false
		}
		contentDigest, err := lockfile.ContentDigestFromDir(dir)
		if err != nil || contentDigest != entry.ContentDigest {
			return false
		}
		adapter, ok := s.materializers[clientType]
		if !ok {
			return false
		}
		if err := adapter.Health(ctx, plugins.DematerializeRequest{
			Name:        pl.Metadata.Name,
			Scope:       pl.Scope,
			ProjectRoot: pl.ProjectRoot,
		}); err != nil {
			return false
		}
	}
	return true
}

// reinstallPinned reinstalls entry at its pinned reference, preserving its
// recorded Source (never re-resolving) and the clients it was previously
// installed for unless the caller overrides them.
func (s *service) reinstallPinned(
	ctx context.Context, opts plugins.SyncOptions, entry lockfile.Entry, existing plugins.InstalledPlugin, dbOK bool,
) error {
	pinnedRef, err := buildPinnedReference(entry)
	if err != nil {
		return fmt.Errorf("pinning %q: %w", entry.Name, err)
	}
	clients := opts.Clients
	if len(clients) == 0 && dbOK {
		clients = existing.Clients
	}
	_, err = s.Install(ctx, plugins.InstallOptions{
		Name:                  pinnedRef,
		Scope:                 plugins.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               clients,
		Force:                 true, // sync restores exactly the pinned content over any drifted files
		LockSource:            entry.Source,
		LockResolvedReference: entry.ResolvedReference, // preserve — pinnedRef is a restore form
		SyncRestore:           true,                    // reinstall despite unchanged Digest — drift is on disk, not the pin
	})
	return err
}

// syncUnlockedInstall classifies a project-scope install that has no lock
// entry: NeverManaged (optionally adopted) or RemovedFromLock (optionally
// pruned), appending the outcome to result.
func (s *service) syncUnlockedInstall(
	ctx context.Context, opts plugins.SyncOptions, pl plugins.InstalledPlugin, result *plugins.SyncResult,
) {
	if !pl.Managed {
		result.NeverManaged = append(result.NeverManaged, pl.Metadata.Name)
		if opts.Adopt && !opts.Check {
			if err := s.adoptPlugin(ctx, pl); err != nil {
				result.Failed = append(result.Failed, plugins.SyncFailure{
					Name: pl.Metadata.Name, Reason: classifySyncFailure(err), Error: err.Error(),
				})
			}
		}
		return
	}

	result.RemovedFromLock = append(result.RemovedFromLock, pl.Metadata.Name)
	if opts.Prune && !opts.Check {
		if err := s.Uninstall(ctx, plugins.UninstallOptions{
			Name: pl.Metadata.Name, Scope: plugins.ScopeProject, ProjectRoot: opts.ProjectRoot,
		}); err != nil {
			result.Failed = append(result.Failed, plugins.SyncFailure{
				Name: pl.Metadata.Name, Reason: classifySyncFailure(err), Error: err.Error(),
			})
			return
		}
		result.Pruned = append(result.Pruned, pl.Metadata.Name)
	}
}

// adoptPlugin writes a lock entry for an existing, unmanaged project-scope
// install, pinning its current on-disk state. The install's own Reference is
// used as Source: an adopted install predates (or never went through) lock
// tracking, so the original user-typed request is not recoverable — the
// concrete resolved reference is the closest available fact to pin against.
// Adoption is rejected when that reference is not a restorable git:// or OCI
// pin (a bare local-store tag cannot be re-fetched later).
//
// Trust state is left unset (no provenance, not unsigned). Plugin Sigstore
// verification lands in a later PR; requiring --allow-unsigned here would
// make every adopt fail until then. Lock validation permits an entry with
// neither provenance nor unsigned.
func (s *service) adoptPlugin(ctx context.Context, pl plugins.InstalledPlugin) error {
	unlock := s.locks.lock(pl.Metadata.Name, plugins.ScopeProject, pl.ProjectRoot)
	defer unlock()

	current, err := s.store.Get(ctx, pl.Metadata.Name, plugins.ScopeProject, pl.ProjectRoot)
	if err != nil {
		return fmt.Errorf("re-reading plugin before adopt: %w", err)
	}
	if current.Managed {
		return nil
	}
	pl = current

	contentDigest, err := s.computeInstalledContentDigest(pl)
	if err != nil {
		return fmt.Errorf("computing content digest: %w", err)
	}
	source := pl.Reference
	if source == "" {
		source = pl.Metadata.Name
	}
	resolved := lockableResolvedReference(pl.Reference)
	if resolved == "" {
		return httperr.WithCode(
			fmt.Errorf("cannot adopt %q: reference %q is not a restorable git or OCI pin",
				pl.Metadata.Name, pl.Reference),
			http.StatusUnprocessableEntity,
		)
	}

	root, err := lockfile.OpenRoot(pl.ProjectRoot)
	if err != nil {
		return fmt.Errorf("opening lock file root: %w", err)
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		return fmt.Errorf("loading lock file: %w", err)
	}
	var prevEntry *lockfile.Entry
	if e, ok := lf.GetPlugin(pl.Metadata.Name); ok {
		prevEntry = &e
	}

	if err := recordLockEntry(pl.ProjectRoot, lockEntryInput{
		Name:              pl.Metadata.Name,
		Version:           pl.Metadata.Version,
		Source:            source,
		ResolvedReference: resolved,
		Digest:            pl.Digest,
		ContentDigest:     contentDigest,
	}); err != nil {
		return fmt.Errorf("writing lock entry: %w", errors.Join(errLockWrite, err))
	}
	pl.Managed = true
	if err := s.store.Update(ctx, pl); err != nil {
		if restoreErr := restoreAdoptedLockEntry(pl, prevEntry); restoreErr != nil {
			return fmt.Errorf("marking plugin as lock-managed: %w", errors.Join(err, restoreErr))
		}
		return fmt.Errorf("marking plugin as lock-managed: %w", err)
	}
	return nil
}

// restoreAdoptedLockEntry undoes adoptPlugin's lock write: reinstates the
// entry observed before adoption, or removes the name if none existed.
func restoreAdoptedLockEntry(pl plugins.InstalledPlugin, prevEntry *lockfile.Entry) error {
	if prevEntry != nil {
		root, err := lockfile.OpenRoot(pl.ProjectRoot)
		if err != nil {
			return err
		}
		return lockfile.UpsertPluginEntry(root, *prevEntry)
	}
	return removeLockEntry(plugins.UninstallOptions{
		Name: pl.Metadata.Name, Scope: plugins.ScopeProject, ProjectRoot: pl.ProjectRoot,
	})
}

// computeInstalledContentDigest hashes every client directory the plugin is
// installed into. All copies must agree — a mismatch is drift, not a pin.
func (s *service) computeInstalledContentDigest(pl plugins.InstalledPlugin) (string, error) {
	if len(pl.Clients) == 0 {
		return "", fmt.Errorf("plugin %q has no clients", pl.Metadata.Name)
	}
	var last string
	for _, clientType := range pl.Clients {
		dir, err := s.pluginInstallPath(clientType, pl.Metadata.Name, pl.Scope, pl.ProjectRoot)
		if err != nil {
			return "", err
		}
		digest, err := lockfile.ContentDigestFromDir(dir)
		if err != nil {
			return "", fmt.Errorf("hashing %s copy of %q: %w", clientType, pl.Metadata.Name, err)
		}
		if last != "" && digest != last {
			return "", fmt.Errorf("content digest mismatch across clients for %q", pl.Metadata.Name)
		}
		last = digest
	}
	return last, nil
}

// lockableResolvedReference returns ref when it is a valid git:// or OCI
// reference suitable for a lock entry's resolvedReference, and empty otherwise.
// Adopted installs from a LayerData/plain-name path may only have the plugin
// name recorded as Reference, which lock validation would reject.
func lockableResolvedReference(ref string) string {
	if gitresolver.IsGitReference(ref) {
		if _, err := gitresolver.ParseGitReference(ref); err == nil {
			return ref
		}
		return ""
	}
	parsed, isOCI, err := parseOCIReference(ref)
	if err != nil || !isOCI || parsed == nil {
		return ""
	}
	return ref
}

// classifySyncFailure maps an error from the install/uninstall path to an
// RFC THV-0080 typed failure reason using structured signals those paths
// already attach — the errLockWrite sentinel and httperr status codes —
// rather than matching on error message text.
func classifySyncFailure(err error) plugins.FailureReason {
	if errors.Is(err, errLockWrite) {
		return plugins.FailureReasonLockWriteFailed
	}
	switch httperr.Code(err) {
	case http.StatusNotFound:
		return plugins.FailureReasonDigestMissing
	case http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusTooManyRequests:
		return plugins.FailureReasonRegistryUnreachable
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusConflict:
		return plugins.FailureReasonValidationRejected
	default:
		return plugins.FailureReasonUnknown
	}
}
