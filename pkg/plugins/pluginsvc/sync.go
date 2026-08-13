// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
)

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

	names := make([]string, 0, len(lf.Plugins)+len(installed))
	seen := make(map[string]struct{}, len(lf.Plugins)+len(installed))
	for _, entry := range lf.Plugins {
		names = append(names, entry.Name)
		seen[entry.Name] = struct{}{}
	}
	for _, pl := range installed {
		if _, ok := seen[pl.Metadata.Name]; ok {
			continue
		}
		names = append(names, pl.Metadata.Name)
	}

	result := &plugins.SyncResult{}
	for _, name := range names {
		s.syncOne(ctx, opts, name, result)
	}

	return result, nil
}

// syncOne re-reads the lock entry and DB row under the per-plugin lock, then
// reconciles that fresh state. The initial Sync snapshot is only used to
// discover names; mutation from a stale view would resurrect an uninstall or
// prune a concurrent install.
func (s *service) syncOne(
	ctx context.Context, opts plugins.SyncOptions, name string, result *plugins.SyncResult,
) {
	_, unlock := s.lockPlugin(ctx, name, plugins.ScopeProject, opts.ProjectRoot)
	defer unlock()

	targetClients, err := s.resolveSyncTargetClients(opts.Clients)
	if err != nil {
		result.Failed = append(result.Failed, plugins.SyncFailure{
			Name: name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}

	root, err := lockfile.OpenRoot(opts.ProjectRoot)
	if err != nil {
		result.Failed = append(result.Failed, plugins.SyncFailure{
			Name: name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}
	lf, err := lockfile.Load(root)
	if err != nil {
		result.Failed = append(result.Failed, plugins.SyncFailure{
			Name: name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}
	entry, hasEntry := lf.GetPlugin(name)

	pl, err := s.store.Get(ctx, name, plugins.ScopeProject, opts.ProjectRoot)
	dbOK := err == nil
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		result.Failed = append(result.Failed, plugins.SyncFailure{
			Name: name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}

	if hasEntry {
		s.syncLockedEntry(ctx, opts, entry, pl, dbOK, targetClients, result)
		return
	}
	if !dbOK {
		return
	}
	s.syncUnlockedInstall(ctx, opts, pl, result)
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
	targetClients []string,
	result *plugins.SyncResult,
) {
	if dbOK && pl.Managed && s.entryMatchesInstalled(ctx, entry, pl, targetClients) {
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
	if err := s.reinstallPinned(ctx, opts, entry, targetClients); err != nil {
		result.Failed = append(result.Failed, plugins.SyncFailure{
			Name: entry.Name, Reason: classifySyncFailure(err), Error: err.Error(),
		})
		return
	}
	result.Installed = append(result.Installed, entry.Name)
}

// resolveSyncTargetClients returns the client set Sync should check and
// restore. Empty requested — or the sole sentinel value "all", matching
// Install and the CLI help text — expands to availableMaterializerClients
// (detected + plugin-supporting). Explicit names are validated like Install
// (materializer + SupportsPlugins) but do not require IsClientInstalled —
// the caller asked for them.
func (s *service) resolveSyncTargetClients(requested []string) ([]string, error) {
	if len(requested) == 0 ||
		(len(requested) == 1 && strings.EqualFold(requested[0], clientsAllSentinel)) {
		return s.availableMaterializerClients(), nil
	}
	for _, c := range requested {
		if c == "" {
			return nil, httperr.WithCode(
				errors.New("clients entries must be non-empty strings"),
				http.StatusBadRequest,
			)
		}
		if strings.EqualFold(c, clientsAllSentinel) {
			return nil, httperr.WithCode(
				fmt.Errorf("%q cannot be combined with other client names", clientsAllSentinel),
				http.StatusBadRequest,
			)
		}
	}
	requested = dedupeStringsPreserveOrder(requested)
	for _, ct := range requested {
		if _, ok := s.materializers[ct]; !ok {
			return nil, httperr.WithCode(
				fmt.Errorf("invalid client %q: no materializer configured", ct),
				http.StatusBadRequest,
			)
		}
		if s.clientManager != nil && !s.clientManager.SupportsPlugins(client.ClientApp(ct)) {
			return nil, httperr.WithCode(
				fmt.Errorf("invalid client %q: %w", ct, client.ErrPluginsNotSupported),
				http.StatusBadRequest,
			)
		}
	}
	return requested, nil
}

// entryMatchesInstalled reports whether the installed plugin is lock-managed,
// its pinned digest still matches the lock entry, every expected client is
// present, every expected client directory's on-disk contentDigest matches,
// and each adapter reports the plugin as healthy. With no --clients override,
// expected clients are every detected plugin-supporting client so a newly
// installed client is not treated as current. Shared registration files are
// validated via adapter Health, not folded into contentDigest.
func (s *service) entryMatchesInstalled(
	ctx context.Context,
	entry lockfile.Entry,
	pl plugins.InstalledPlugin,
	expected []string,
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
	if len(expected) == 0 || !clientsContainAll(pl.Clients, expected) {
		return false
	}
	for _, clientType := range expected {
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
// recorded Source (never re-resolving). targetClients is the resolved sync
// client set (empty opts.Clients → detected clients). Callers must hold the
// per-plugin lock.
func (s *service) reinstallPinned(
	ctx context.Context, opts plugins.SyncOptions, entry lockfile.Entry, targetClients []string,
) error {
	pinnedRef, err := buildPinnedReference(entry)
	if err != nil {
		return fmt.Errorf("pinning %q: %w", entry.Name, err)
	}
	_, err = s.installAlreadyLocked(ctx, plugins.InstallOptions{
		Name:                  pinnedRef,
		Scope:                 plugins.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               targetClients,
		Force:                 true, // sync restores exactly the pinned content over any drifted files
		LockSource:            entry.Source,
		LockResolvedReference: entry.ResolvedReference, // preserve — pinnedRef is a restore form
		SyncRestore:           true,                    // reinstall despite unchanged Digest — drift is on disk, not the pin
		ExpectedCanonicalName: entry.Name,
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
			if err := s.adoptLocked(ctx, pl); err != nil {
				result.Failed = append(result.Failed, plugins.SyncFailure{
					Name: pl.Metadata.Name, Reason: classifySyncFailure(err), Error: err.Error(),
				})
			}
		}
		return
	}

	result.RemovedFromLock = append(result.RemovedFromLock, pl.Metadata.Name)
	if opts.Prune && !opts.Check {
		if err := s.uninstallLocked(ctx, plugins.UninstallOptions{
			Name: pl.Metadata.Name, Scope: plugins.ScopeProject, ProjectRoot: opts.ProjectRoot,
		}, plugins.ScopeProject); err != nil {
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
// adoptLocked writes a lock entry for an unmanaged install assuming the
// per-plugin lock is already held.
func (s *service) adoptLocked(ctx context.Context, pl plugins.InstalledPlugin) error {
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
