// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/storage"
)

// clientsAllSentinel is the reserved value that expands to all
// plugin-supporting clients. Mirror of skillsvc.clientsAllSentinel.
const clientsAllSentinel = "all"

// installWithExtraction handles the full plugin install flow: client resolution,
// per-client materialization, and DB record creation or update. It is the
// plugin analogue of skillsvc.installWithExtraction, substituting
// MaterializationAdapter.Materialize for skills.Installer.Extract.
func (s *service) installWithExtraction(
	ctx context.Context, opts plugins.InstallOptions, scope plugins.Scope,
) (*plugins.InstallResult, error) {
	clientTypes, err := s.resolveAndValidateClients(opts, scope)
	if err != nil {
		return nil, err
	}

	existing, storeErr := s.store.Get(ctx, opts.Name, scope, opts.ProjectRoot)
	isNotFound := errors.Is(storeErr, storage.ErrNotFound)
	if storeErr != nil && !isNotFound {
		return nil, fmt.Errorf("checking existing plugin: %w", storeErr)
	}

	contentDigest, err := lockContentDigest(opts, scope)
	if err != nil {
		return nil, err
	}

	result, err := s.dispatchExtraction(ctx, opts, scope, existing, storeErr, clientTypes)
	if err != nil {
		return nil, err
	}
	if storeErr == nil {
		// Preserve the pre-install record so a later rollback (e.g. a failed
		// lock write) can restore it rather than delete it.
		pre := existing
		result.PreExisting = &pre
	}
	result.ContentDigest = contentDigest
	return result, nil
}

// dispatchExtraction routes an extraction-based install to the no-op,
// same-digest, upgrade, or fresh path based on the pre-install store state.
func (s *service) dispatchExtraction(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	existing plugins.InstalledPlugin,
	storeErr error,
	clientTypes []string,
) (*plugins.InstallResult, error) {
	// Freshly verified trust material has to reach the stored record, so an
	// install carrying a bundle that differs from the record's can take
	// neither the no-op nor the same-digest client-only path: both return
	// `existing` verbatim without persisting anything. Project installs
	// predating lock tracking recorded no bundle (verification was gated
	// off), so a same-digest reinstall would otherwise write a lock entry
	// naming a signer that verifyStoredSignature cannot re-verify offline —
	// it fails closed on provenance-without-bundle — while leaving on-disk
	// drift unrepaired. Routing to the upgrade path rematerializes and
	// persists both.
	//
	// Compared by bytes rather than by "the record has none": a stored bundle
	// that is present but stale or corrupt is exactly as unusable offline as
	// a missing one, and keeping it would make an explicit reinstall — the
	// user's remedy for a failing `sync --check` — verify a good bundle and
	// then discard it, so the next offline sync fails identically.
	mustPersistTrust := storeErr == nil && len(opts.SigstoreBundle) > 0 &&
		!bytes.Equal(opts.SigstoreBundle, existing.SigstoreBundle)

	if !opts.SyncRestore && !mustPersistTrust && isExtractionNoOp(existing, storeErr, opts, clientTypes) {
		return &plugins.InstallResult{Plugin: existing}, nil
	}

	digestMatches := storeErr == nil && existing.Digest == opts.Digest
	if digestMatches && !opts.SyncRestore && !mustPersistTrust {
		return s.installExtractionSameDigestNewClients(ctx, opts, scope, existing, clientTypes)
	}
	if storeErr == nil {
		return s.installExtractionUpgradeDigest(ctx, opts, scope, existing, clientTypes)
	}
	return s.installExtractionFresh(ctx, opts, scope, clientTypes)
}

// isExtractionNoOp reports whether the install can be short-circuited because
// the same digest and all requested clients are already present. Mirror of
// skillsvc.isExtractionNoOp. Callers must also check SyncRestore and whether
// newly verified trust material still needs persisting (see
// dispatchExtraction): a lock-driven reinstall repairs on-disk drift at the
// same digest, and a record whose stored bundle differs from the freshly
// verified one needs it written, so the no-op path must not apply to either.
func isExtractionNoOp(existing plugins.InstalledPlugin, storeErr error, opts plugins.InstallOptions, clientTypes []string) bool {
	if storeErr != nil || existing.Digest != opts.Digest {
		return false
	}
	if clientsContainAll(existing.Clients, clientTypes) {
		return true
	}
	return len(existing.Clients) == 0 && len(clientTypes) <= 1 && len(opts.Clients) == 0
}

// installExtractionSameDigestNewClients materializes the plugin for clients
// not already present at the same digest, then updates the DB record.
// When a ClientManager is available, pre-existing unmanaged trees on those
// clients are snapshotted so rollback can restore them; without one,
// compensation falls back to dematerialize-only (embedded/test services).
func (s *service) installExtractionSameDigestNewClients(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	existing plugins.InstalledPlugin,
	clientTypes []string,
) (*plugins.InstallResult, error) {
	toWrite := missingClients(existing.Clients, clientTypes)
	if len(toWrite) == 0 {
		return &plugins.InstallResult{Plugin: existing}, nil
	}
	return s.materializeAndPersist(ctx, opts, scope, toWrite, clientTypes, existing.Clients, existing.Managed, false)
}

// installExtractionUpgradeDigest re-materializes the plugin for the union of
// requested and existing clients (upgrades write to every client), then updates
// the DB record. Snapshot/restore compensation applies when a ClientManager is
// configured; without one (embedded/test services, WithClientManager is
// optional) compensation degrades to dematerialize-only, matching the fresh
// and same-digest paths.
//
// SyncRestore is different: sync must materialize exactly the requested target
// clients (the sync client set), not re-merge historical Clients from the DB —
// otherwise an old client that is no longer detected/requested would stay in
// the persisted list forever.
func (s *service) installExtractionUpgradeDigest(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	existing plugins.InstalledPlugin,
	clientTypes []string,
) (*plugins.InstallResult, error) {
	allClients := clientTypes
	if !opts.SyncRestore {
		allClients = mergeClientLists(existing.Clients, clientTypes)
	}
	return s.materializeAndPersist(ctx, opts, scope, allClients, allClients, nil, existing.Managed, false)
}

// installExtractionFresh materializes the plugin for all requested clients,
// then creates the DB record. When a ClientManager is available, pre-existing
// unmanaged trees are snapshotted so Force overwrite can be rolled back.
func (s *service) installExtractionFresh(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	clientTypes []string,
) (*plugins.InstallResult, error) {
	return s.materializeAndPersist(ctx, opts, scope, clientTypes, clientTypes, nil, false, true)
}

// materializeAndPersist materializes targetClients and creates or updates the
// DB row. When a ClientManager is configured it snapshots those targets first
// and uses restoreClientTrees for compensation; otherwise it dematerializes
// only what this call wrote.
func (s *service) materializeAndPersist(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	targetClients []string,
	resultClients []string,
	existingClients []string,
	managed bool,
	create bool,
) (*plugins.InstallResult, error) {
	useSnapshot := s.clientManager != nil
	var backups map[string]clientTreeBackup
	if useSnapshot {
		var snapErr error
		backups, snapErr = s.snapshotClientTrees(ctx, opts.Name, scope, opts.ProjectRoot, targetClients)
		if snapErr != nil {
			return nil, fmt.Errorf("snapshotting plugin trees before install: %w", snapErr)
		}
	}

	materialized, err := s.materializeForClients(ctx, opts, scope, targetClients, !useSnapshot)
	if err != nil {
		if useSnapshot {
			if restoreErr := s.restoreClientTrees(ctx, opts.Name, scope, opts.ProjectRoot, backups, targetClients); restoreErr != nil {
				return nil, errors.Join(err, restoreErr)
			}
		}
		return nil, err
	}

	pl := buildInstalledPlugin(opts, scope, resultClients, existingClients)
	pl.Managed = managed
	if create {
		err = s.store.Create(ctx, pl)
	} else {
		err = s.store.Update(ctx, pl)
	}
	if err != nil {
		if useSnapshot {
			if restoreErr := s.restoreClientTrees(ctx, opts.Name, scope, opts.ProjectRoot, backups, targetClients); restoreErr != nil {
				return nil, errors.Join(err, restoreErr)
			}
		} else if dmErr := s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot); dmErr != nil {
			return nil, errors.Join(err, dmErr)
		}
		return nil, err
	}

	restore := func(ctx context.Context) error {
		if useSnapshot {
			return s.restoreClientTrees(ctx, opts.Name, scope, opts.ProjectRoot, backups, targetClients)
		}
		return s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot)
	}
	return &plugins.InstallResult{Plugin: pl, RestoreFiles: restore}, nil
}

// materializeForClients calls Materialize for each requested client type.
// When compensate is true, a failure dematerializes every client that was
// written (including the failing client, whose Materialize can extract before
// marketplace/settings registration fails). When compensate is false, the
// caller is responsible for restoreClientTrees / dematerializeAll.
// Returns the list of client types that were successfully materialized.
func (s *service) materializeForClients(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	clientTypes []string,
	compensate bool,
) ([]string, error) {
	var materialized []string
	for _, ct := range clientTypes {
		adapter, ok := s.materializers[ct]
		if !ok {
			err := httperr.WithCode(
				fmt.Errorf("no materializer configured for client %q", ct),
				http.StatusInternalServerError,
			)
			if compensate {
				return nil, errors.Join(err, s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot))
			}
			return nil, err
		}
		if _, err := adapter.Materialize(ctx, plugins.MaterializeRequest{
			Name:        opts.Name,
			LayerData:   opts.LayerData,
			Scope:       scope,
			ProjectRoot: opts.ProjectRoot,
			Components:  opts.Components,
		}); err != nil {
			wrapped := fmt.Errorf("materializing plugin for client %q: %w", ct, err)
			if !compensate {
				return nil, wrapped
			}
			// Materialize can extract the tree and then fail during
			// marketplace/settings registration. Compensate the failing
			// client too, not only the ones already appended.
			failed := append(append([]string{}, materialized...), ct)
			return nil, errors.Join(wrapped, s.dematerializeAll(ctx, failed, opts.Name, scope, opts.ProjectRoot))
		}
		materialized = append(materialized, ct)
	}
	return materialized, nil
}

// fileSnapshot is one regular file captured by snapshotDir: contents plus a
// sanitized permission mode so executable hooks stay executable on restore.
type fileSnapshot struct {
	data []byte
	mode fs.FileMode
}

// clientTreeBackup is one client's pre-mutation state: the plugin tree files
// plus whether the adapter reported the plugin as registered at snapshot
// time. Restore must reproduce that exact state — re-registering a tree that
// was never registered would make a failed install enable a previously
// undiscoverable unmanaged plugin.
type clientTreeBackup struct {
	tree       treeSnapshot
	registered bool
}

// treeSnapshot is one client tree captured by snapshotDir: its regular files
// plus every directory, so empty directories survive a restore. Symlinks and
// other non-regular entries are intentionally not captured — ExtractPlugin
// rejects symlinks at install time, so a ToolHive-managed tree never
// contains any.
type treeSnapshot struct {
	files map[string]fileSnapshot
	// dirs holds relative directory paths in walk order (parents before
	// children), so restore can recreate empty directories.
	dirs []string
}

// snapshotFileModeMask strips setuid/setgid/sticky and caps at 0755, matching
// skills.PluginFilePermissionMask so restored hooks keep +x without restoring
// unsafe bits.
const snapshotFileModeMask fs.FileMode = 0o755

func sanitizeFileMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm() & snapshotFileModeMask
}

// snapshotClientTrees copies each client's installed plugin tree into memory
// so a later rollback can restore the previous materialization without
// leaking temp directories, recording alongside each tree whether the
// adapter considered the plugin registered at snapshot time. Missing
// directories are omitted (the client was not yet installed). A walk/read
// error on an existing tree is returned so the caller can abort before
// mutating. Path-resolution failures (including a missing ClientManager)
// abort rather than being treated as "not installed."
func (s *service) snapshotClientTrees(
	ctx context.Context, name string, scope plugins.Scope, projectRoot string, clientTypes []string,
) (map[string]clientTreeBackup, error) {
	backups := make(map[string]clientTreeBackup, len(clientTypes))
	var errs []error
	for _, ct := range clientTypes {
		dir, err := s.pluginInstallPath(ct, name, scope, projectRoot)
		if err != nil {
			return nil, fmt.Errorf("resolving %s install path of %q: %w", ct, name, err)
		}
		tree, err := snapshotDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, fmt.Errorf("snapshotting %s copy of %q: %w", ct, name, err))
			continue
		}
		registered := false
		if adapter, ok := s.materializers[ct]; ok {
			registered = adapter.Health(ctx, plugins.DematerializeRequest{
				Name:        name,
				Scope:       scope,
				ProjectRoot: projectRoot,
			}) == nil
		}
		backups[ct] = clientTreeBackup{tree: tree, registered: registered}
	}
	if len(errs) > 0 {
		return backups, errors.Join(errs...)
	}
	if len(backups) == 0 {
		return nil, nil
	}
	return backups, nil
}

// restoreClientTrees restores each backed-up client to its exact snapshot
// state: any registration the failed operation added is cleared via
// Dematerialize, the snapshotted tree is rewritten, and marketplace/settings
// entries are re-registered only when the snapshot found them registered.
// Clients without a backup are dematerialized (they were newly added by the
// failed install).
func (s *service) restoreClientTrees(
	ctx context.Context,
	name string,
	scope plugins.Scope,
	projectRoot string,
	backups map[string]clientTreeBackup,
	allClients []string,
) error {
	var errs []error
	restored := make(map[string]struct{}, len(backups))
	for ct, backup := range backups {
		restored[ct] = struct{}{}
		dir, err := s.pluginInstallPath(ct, name, scope, projectRoot)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolving %s install path: %w", ct, err))
			continue
		}
		adapter, hasAdapter := s.materializers[ct]
		// Clear whatever files/registration the failed operation left behind
		// before rewriting the snapshot, so a tree that was unregistered at
		// snapshot time does not stay registered after rollback.
		if hasAdapter {
			if err := adapter.Dematerialize(ctx, plugins.DematerializeRequest{
				Name:        name,
				Scope:       scope,
				ProjectRoot: projectRoot,
			}); err != nil {
				errs = append(errs, fmt.Errorf("clearing %s state before restore: %w", ct, err))
			}
		}
		restoreErr := restoreDir(dir, backup.tree)
		if restoreErr != nil {
			errs = append(errs, fmt.Errorf("restoring %s plugin tree: %w", ct, restoreErr))
		}
		// Never re-register a tree whose restore failed: restoreDir removes
		// the live tree before rewriting it, so a partial restore followed by
		// EnsureRegistered would make the client load an incomplete plugin.
		if hasAdapter && backup.registered && restoreErr == nil {
			if err := adapter.EnsureRegistered(ctx, plugins.DematerializeRequest{
				Name:        name,
				Scope:       scope,
				ProjectRoot: projectRoot,
			}); err != nil {
				errs = append(errs, fmt.Errorf("restoring %s registration: %w", ct, err))
			}
		}
	}
	var extra []string
	for _, ct := range allClients {
		if _, ok := restored[ct]; !ok {
			extra = append(extra, ct)
		}
	}
	if err := s.dematerializeAll(ctx, extra, name, scope, projectRoot); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// snapshotDir reads every regular file under dir into a relative-path map,
// preserving sanitized permission bits. Returns os.ErrNotExist when dir does
// not exist.
func snapshotDir(dir string) (treeSnapshot, error) {
	if _, err := os.Stat(dir); err != nil {
		return treeSnapshot{}, err
	}
	snap := treeSnapshot{files: make(map[string]fileSnapshot)}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if rel != "." {
				snap.dirs = append(snap.dirs, rel)
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // path is under a GetPluginPath-validated directory
		if readErr != nil {
			return readErr
		}
		snap.files[rel] = fileSnapshot{data: data, mode: sanitizeFileMode(info.Mode())}
		return nil
	})
	if err != nil {
		return treeSnapshot{}, err
	}
	return snap, nil
}

// restoreDir replaces dir with the tree captured by snapshotDir: every
// directory is recreated (including empty ones) and every file is written
// through the contained atomic write path with its sanitized mode.
func restoreDir(dir string, tree treeSnapshot) error {
	dir = filepath.Clean(dir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	var errs []error
	for _, rel := range tree.dirs {
		if !filepath.IsLocal(rel) {
			errs = append(errs, fmt.Errorf("refusing to restore non-local directory path %q", rel))
			continue
		}
		if err := os.MkdirAll(filepath.Join(dir, rel), 0o750); err != nil {
			errs = append(errs, err)
		}
	}
	for rel, snap := range tree.files {
		if err := fileutils.WriteContainedFile(dir, rel, snap.data, 0o750, snap.mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// pluginInstallPath resolves the on-disk plugin directory for a client.
func (s *service) pluginInstallPath(clientType, name string, scope plugins.Scope, projectRoot string) (string, error) {
	if s.clientManager == nil {
		return "", errors.New("client manager is not configured")
	}
	return s.clientManager.GetPluginPath(client.ClientApp(clientType), name, scope, projectRoot)
}

// dematerializeAll reverts materializations performed in this call.
// Errors are joined so a partial rollback still surfaces.
func (s *service) dematerializeAll(
	ctx context.Context,
	clientTypes []string,
	name string,
	scope plugins.Scope,
	projectRoot string,
) error {
	var errs []error
	for _, ct := range clientTypes {
		if adapter, ok := s.materializers[ct]; ok {
			if err := adapter.Dematerialize(ctx, plugins.DematerializeRequest{
				Name:        name,
				Scope:       scope,
				ProjectRoot: projectRoot,
			}); err != nil {
				errs = append(errs, fmt.Errorf("dematerializing plugin for client %q: %w", ct, err))
			}
		}
	}
	return errors.Join(errs...)
}

// resolveAndValidateClients returns the deduplicated client list to target for
// this install. Empty opts.Clients (or the sentinel value "all") expands to
// every client from availableMaterializerClients (materializer present, and
// when a client manager is set: SupportsPlugins + IsClientInstalled). Explicit
// client names are validated to be present in s.materializers.
//
// Unlike skillsvc.resolveAndValidateClients, this does NOT resolve filesystem
// paths — the MaterializationAdapter owns path resolution, so the caller
// receives only the client-type list, not a client→dir map.
func (s *service) resolveAndValidateClients(
	opts plugins.InstallOptions,
	_ plugins.Scope,
) ([]string, error) {
	var requested []string
	switch {
	case len(opts.Clients) == 0 || (len(opts.Clients) == 1 && strings.EqualFold(opts.Clients[0], clientsAllSentinel)):
		available := s.availableMaterializerClients()
		if len(available) == 0 {
			return nil, httperr.WithCode(
				errors.New("no supported clients detected on this system; "+
					"use --clients to target a specific client explicitly"),
				http.StatusBadRequest,
			)
		}
		requested = available
	default:
		for _, c := range opts.Clients {
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
		requested = dedupeStringsPreserveOrder(opts.Clients)
	}

	// Validate each requested client has a configured materializer. When a
	// client manager is available, also reject clients it does not consider
	// plugin-supporting (defense in depth — the materializers map is the
	// source of truth, but cm catches misconfiguration).
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

// availableMaterializerClients returns the sorted list of client types that
// have a configured materializer. When a client manager is set, only clients
// that both support plugins and appear installed on the system are included.
// When the client manager is nil, every materializer key is returned (tests
// and embedded services without detection).
func (s *service) availableMaterializerClients() []string {
	var out []string
	for ct := range s.materializers {
		if s.clientManager != nil {
			app := client.ClientApp(ct)
			if !s.clientManager.SupportsPlugins(app) || !s.clientManager.IsClientInstalled(app) {
				continue
			}
		}
		out = append(out, ct)
	}
	slices.Sort(out)
	return out
}

// buildInstalledPlugin constructs an InstalledPlugin from install options.
// requestedClientTypes is merged with existingClients for the persisted Clients
// field. Mirror of skillsvc.buildInstalledSkill, substituting plugin types and
// carrying through Components/Dependencies/Tag/Signature.
func buildInstalledPlugin(
	opts plugins.InstallOptions,
	scope plugins.Scope,
	requestedClientTypes []string,
	existingClients []string,
) plugins.InstalledPlugin {
	clients := mergeClientLists(existingClients, requestedClientTypes)
	return plugins.InstalledPlugin{
		Metadata: plugins.PluginMetadata{
			Name:        opts.Name,
			Version:     opts.Version,
			Description: opts.Description,
		},
		Scope:          scope,
		ProjectRoot:    opts.ProjectRoot,
		Reference:      opts.Reference,
		Tag:            opts.Tag,
		Digest:         opts.Digest,
		Status:         plugins.InstallStatusInstalled,
		InstalledAt:    time.Now().UTC(),
		Clients:        clients,
		Components:     opts.Components,
		Dependencies:   opts.Dependencies,
		SigstoreBundle: opts.SigstoreBundle,
	}
}

// dedupeStringsPreserveOrder returns the input slice with duplicates removed,
// preserving first-seen order. Mirror of skillsvc.dedupeStringsPreserveOrder.
func dedupeStringsPreserveOrder(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// clientsContainAll reports whether every value in requested appears in existing.
func clientsContainAll(existing, requested []string) bool {
	for _, r := range requested {
		if !slices.Contains(existing, r) {
			return false
		}
	}
	return true
}

// mergeClientLists returns existing followed by any requested entries not already present.
func mergeClientLists(existing, requested []string) []string {
	out := make([]string, len(existing))
	copy(out, existing)
	seen := make(map[string]struct{}, len(existing)+len(requested))
	for _, c := range existing {
		seen[c] = struct{}{}
	}
	for _, c := range requested {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func missingClients(existing, requested []string) []string {
	var out []string
	for _, ct := range requested {
		if !slices.Contains(existing, ct) {
			out = append(out, ct)
		}
	}
	return out
}

// lockContentDigest computes the canonical-tree dirhash for a project-scope
// install. Empty when the install is not lock-scoped, so user-scope installs
// skip the extra extract.
func lockContentDigest(opts plugins.InstallOptions, scope plugins.Scope) (string, error) {
	if scope != plugins.ScopeProject {
		return "", nil
	}
	digest, err := computeContentDigest(opts.LayerData)
	if err != nil {
		return "", httperr.WithCode(
			fmt.Errorf("computing content digest: %w", err),
			http.StatusInternalServerError,
		)
	}
	return digest, nil
}
