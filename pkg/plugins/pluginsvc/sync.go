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

	"github.com/opencontainers/go-digest"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
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
// appending its outcome to result. Missing (dbOK false), drifted (digest or
// contentDigest mismatch) and signature-drifted (the stored bundle no longer
// verifies against the locked identity) entries are reinstalled at the pinned
// reference unless opts.Check is set, in which case nothing is written — all
// three states are still reported (Missing/Drifted), never as failures.
func (s *service) syncLockedEntry(
	ctx context.Context,
	opts plugins.SyncOptions,
	entry lockfile.Entry,
	pl plugins.InstalledPlugin,
	dbOK bool,
	targetClients []string,
	result *plugins.SyncResult,
) {
	sigOK := true
	if dbOK {
		if sigErr := s.verifyStoredSignature(entry, pl); sigErr != nil {
			// A failed offline re-verification is treated as drift: check
			// mode reports it, apply mode reinstalls from the pinned
			// reference — where install-time verification enforces the
			// locked identity and, on success, heals the stored bundle.
			sigOK = false
			slog.Warn("stored signature failed offline re-verification",
				"plugin", entry.Name, "error", sigErr)
		}
	}
	if dbOK && sigOK && pl.Managed && s.entryMatchesInstalled(ctx, entry, pl, targetClients) {
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
	if isLocalStorePin(entry) {
		return s.reinstallLocalStorePin(ctx, opts, entry, targetClients)
	}
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

// reinstallLocalStorePin restores a plain-Source lock pin whose digest lives
// only in the local OCI store. It loads that exact digest, validates the
// artifact's canonical plugin name, and installs the layer bytes directly so
// Source is never reinterpreted as Docker Hub. A missing digest is reported
// as digest-missing without mutating DB or lock state.
func (s *service) reinstallLocalStorePin(
	ctx context.Context, opts plugins.SyncOptions, entry lockfile.Entry, targetClients []string,
) error {
	if s.ociStore == nil {
		return httperr.WithCode(
			fmt.Errorf("pinned digest %q for plugin %q not found in local store: OCI store is not configured",
				entry.Digest, entry.Name),
			http.StatusNotFound,
		)
	}
	d, err := digest.Parse(entry.Digest)
	if err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid pinned digest %q for plugin %q: %w", entry.Digest, entry.Name, err),
			http.StatusUnprocessableEntity,
		)
	}

	layerData, pluginConfig, err := s.extractPluginOCIContent(ctx, d)
	if err != nil {
		// Missing digests (fresh machine) and unreadable local blobs both
		// surface as digest-missing so sync does not mutate DB/lock. Preserve
		// explicit validation rejections from extractPluginOCIContent.
		if httperr.Code(err) == http.StatusUnprocessableEntity {
			return err
		}
		return httperr.WithCode(
			fmt.Errorf("pinned digest %q for plugin %q not found in local store: %w",
				entry.Digest, entry.Name, err),
			http.StatusNotFound,
		)
	}
	if pluginConfig == nil {
		return httperr.WithCode(
			fmt.Errorf("pinned digest %q for plugin %q has no plugin config", entry.Digest, entry.Name),
			http.StatusUnprocessableEntity,
		)
	}
	if err := plugins.ValidatePluginName(pluginConfig.Name); err != nil {
		return httperr.WithCode(
			fmt.Errorf("local artifact contains invalid plugin name: %w", err),
			http.StatusUnprocessableEntity,
		)
	}
	if pluginConfig.Name != entry.Name {
		return httperr.WithCode(
			fmt.Errorf("plugin name %q in local artifact does not match lock entry name %q",
				pluginConfig.Name, entry.Name),
			http.StatusUnprocessableEntity,
		)
	}

	installOpts := plugins.InstallOptions{
		Name:                  entry.Name,
		Scope:                 plugins.ScopeProject,
		ProjectRoot:           opts.ProjectRoot,
		Clients:               targetClients,
		Force:                 true,
		LockSource:            entry.Source,
		LockResolvedReference: "", // local-store pins stay empty so sync restores by digest
		SyncRestore:           true,
		ExpectedCanonicalName: entry.Name,
	}
	hydrateOptsFromLocalBuild(&installOpts, layerData, d, pluginConfig, entry.Source)
	_, err = s.installAlreadyLocked(ctx, installOpts)
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
			if err := s.adoptLocked(ctx, opts, pl); err != nil {
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

// verifyStoredSignature re-verifies the Sigstore bundle stored with an
// installed plugin against the identity its lock entry records — entirely
// offline, via the embedded trust root, so sync never contacts a registry to
// decide whether an entry is current. Entries recorded unsigned or with no
// provenance have nothing to verify. A recorded identity with no stored
// bundle fails closed for OCI installs (the bundle should exist); git
// installs never store a bundle — their signature lives on the commit and is
// re-verified when content is re-resolved.
func (s *service) verifyStoredSignature(entry lockfile.Entry, pl plugins.InstalledPlugin) error {
	if entry.Unsigned || entry.Provenance == nil {
		return nil
	}
	if len(pl.SigstoreBundle) == 0 {
		if !strings.Contains(entry.Digest, ":") {
			return nil // git install: no stored bundle by design
		}
		return fmt.Errorf("%w: lock entry records signer %q but no bundle is stored",
			verifier.ErrSignatureInvalid, entry.Provenance.SignerIdentity)
	}
	return s.artifactVerifier().VerifyBundleOffline(pl.SigstoreBundle, entry.Digest, entry.Provenance)
}

// adoptLocked writes a lock entry for an existing, unmanaged project-scope
// install, pinning its current on-disk state and assuming the per-plugin lock
// is already held. The install's own Reference is used as Source: an adopted
// install predates (or never went through) lock tracking, so the original
// user-typed request is not recoverable — the concrete resolved reference is
// the closest available fact to pin against. Adoption is rejected when that
// reference is not a restorable git:// or OCI pin (a bare local-store tag
// cannot be re-fetched later).
//
// Trust state is back-filled from the stored Sigstore bundle when one exists;
// otherwise adoption is the same trust decision as an unsigned install and
// requires the explicit AllowUnsigned exception.
func (s *service) adoptLocked(ctx context.Context, opts plugins.SyncOptions, pl plugins.InstalledPlugin) error {
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

	provenance, unsigned, err := s.adoptionTrust(opts, pl)
	if err != nil {
		return err
	}

	if err := recordLockEntry(pl.ProjectRoot, lockEntryInput{
		Name:              pl.Metadata.Name,
		Version:           pl.Metadata.Version,
		Source:            source,
		ResolvedReference: resolved,
		Digest:            pl.Digest,
		ContentDigest:     contentDigest,
		Provenance:        provenance,
		Unsigned:          unsigned,
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

// adoptionTrust decides what trust state an adopted install records. A stored
// Sigstore bundle is the only evidence adoption has to work from: when one
// exists its identity is back-filled into the lock entry, and when it does not
// adopting is the same trust decision as an unsigned install — it records an
// explicit unsigned exception, which the caller must have opted into.
func (s *service) adoptionTrust(
	opts plugins.SyncOptions, pl plugins.InstalledPlugin,
) (*lockfile.Provenance, bool, error) {
	if len(pl.SigstoreBundle) > 0 {
		result, err := s.artifactVerifier().ResultFromBundle(pl.SigstoreBundle, pl.Digest)
		if err != nil {
			return nil, false, fmt.Errorf("verifying stored bundle for adoption: %w", err)
		}
		if provenance := result.ToLockProvenance(); provenance != nil {
			return provenance, false, nil
		}
	}
	if !opts.AllowUnsigned {
		return nil, false, httperr.WithCode(
			fmt.Errorf("%w: adopting %q records it as unsigned; pass --allow-unsigned to accept that",
				verifier.ErrUnsigned, pl.Metadata.Name),
			http.StatusForbidden,
		)
	}
	return nil, true, nil
}

// restoreAdoptedLockEntry undoes adoptLocked's lock write: reinstates the
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
	if reason := classifySignatureError(err); reason != "" {
		return reason
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
