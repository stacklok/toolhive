// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
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
	if isExtractionNoOp(existing, storeErr, opts, clientTypes) {
		return &plugins.InstallResult{Plugin: existing}, nil
	}

	digestMatches := storeErr == nil && existing.Digest == opts.Digest
	if digestMatches {
		return s.installExtractionSameDigestNewClients(ctx, opts, scope, existing, clientTypes)
	}
	if storeErr == nil {
		return s.installExtractionUpgradeDigest(ctx, opts, scope, existing, clientTypes)
	}
	return s.installExtractionFresh(ctx, opts, scope, clientTypes)
}

// isExtractionNoOp reports whether the install can be short-circuited because
// the same digest and all requested clients are already present. Mirror of
// skillsvc.isExtractionNoOp. Sync (a later PR) will need a SyncRestore bypass
// so a lock-driven reinstall can repair on-disk drift at the same digest.
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
	materialized, err := s.materializeForClients(ctx, opts, scope, toWrite)
	if err != nil {
		return nil, err
	}
	pl := buildInstalledPlugin(opts, scope, clientTypes, existing.Clients)
	pl.Managed = existing.Managed
	if err := s.store.Update(ctx, pl); err != nil {
		if dmErr := s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot); dmErr != nil {
			return nil, errors.Join(err, dmErr)
		}
		return nil, err
	}
	return &plugins.InstallResult{
		Plugin: pl,
		RestoreFiles: func(ctx context.Context) error {
			return s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot)
		},
	}, nil
}

// installExtractionUpgradeDigest re-materializes the plugin for the union of
// requested and existing clients (upgrades write to every client), then updates
// the DB record.
func (s *service) installExtractionUpgradeDigest(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	existing plugins.InstalledPlugin,
	clientTypes []string,
) (*plugins.InstallResult, error) {
	allClients := mergeClientLists(existing.Clients, clientTypes)
	backups, snapErr := s.snapshotClientTrees(opts.Name, scope, opts.ProjectRoot, allClients)
	if snapErr != nil {
		return nil, fmt.Errorf("snapshotting installed plugin trees: %w", snapErr)
	}
	if _, err := s.materializeForClients(ctx, opts, scope, allClients); err != nil {
		if restoreErr := s.restoreClientTrees(ctx, opts.Name, scope, opts.ProjectRoot, backups, allClients); restoreErr != nil {
			return nil, errors.Join(err, restoreErr)
		}
		return nil, err
	}
	pl := buildInstalledPlugin(opts, scope, allClients, nil)
	pl.Managed = existing.Managed
	if err := s.store.Update(ctx, pl); err != nil {
		if restoreErr := s.restoreClientTrees(ctx, opts.Name, scope, opts.ProjectRoot, backups, allClients); restoreErr != nil {
			return nil, errors.Join(err, restoreErr)
		}
		return nil, err
	}
	return &plugins.InstallResult{
		Plugin: pl,
		RestoreFiles: func(ctx context.Context) error {
			return s.restoreClientTrees(ctx, opts.Name, scope, opts.ProjectRoot, backups, allClients)
		},
	}, nil
}

// installExtractionFresh materializes the plugin for all requested clients,
// then creates the DB record.
func (s *service) installExtractionFresh(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	clientTypes []string,
) (*plugins.InstallResult, error) {
	materialized, err := s.materializeForClients(ctx, opts, scope, clientTypes)
	if err != nil {
		return nil, err
	}
	pl := buildInstalledPlugin(opts, scope, clientTypes, nil)
	if err := s.store.Create(ctx, pl); err != nil {
		if dmErr := s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot); dmErr != nil {
			return nil, errors.Join(err, dmErr)
		}
		return nil, err
	}
	return &plugins.InstallResult{
		Plugin: pl,
		RestoreFiles: func(ctx context.Context) error {
			return s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot)
		},
	}, nil
}

// materializeForClients calls Materialize for each requested client type,
// rolling back (Dematerialize) any already-materialized client on failure.
// Returns the list of client types that were successfully materialized.
func (s *service) materializeForClients(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	clientTypes []string,
) ([]string, error) {
	var materialized []string
	for _, ct := range clientTypes {
		adapter, ok := s.materializers[ct]
		if !ok {
			err := httperr.WithCode(
				fmt.Errorf("no materializer configured for client %q", ct),
				http.StatusInternalServerError,
			)
			return nil, errors.Join(err, s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot))
		}
		if _, err := adapter.Materialize(ctx, plugins.MaterializeRequest{
			Name:        opts.Name,
			LayerData:   opts.LayerData,
			Scope:       scope,
			ProjectRoot: opts.ProjectRoot,
			Components:  opts.Components,
		}); err != nil {
			wrapped := fmt.Errorf("materializing plugin for client %q: %w", ct, err)
			return nil, errors.Join(wrapped, s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot))
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

// snapshotFileModeMask strips setuid/setgid/sticky and caps at 0755, matching
// skills.PluginFilePermissionMask so restored hooks keep +x without restoring
// unsafe bits.
const snapshotFileModeMask fs.FileMode = 0o755

func sanitizeFileMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm() & snapshotFileModeMask
}

// snapshotClientTrees copies each client's installed plugin tree into memory
// so a later rollback can restore the previous materialization without
// leaking temp directories. Missing directories are omitted (the client was
// not yet installed). A walk/read error on an existing tree is returned so
// the caller can abort before mutating.
func (s *service) snapshotClientTrees(
	name string, scope plugins.Scope, projectRoot string, clientTypes []string,
) (map[string]map[string]fileSnapshot, error) {
	backups := make(map[string]map[string]fileSnapshot, len(clientTypes))
	var errs []error
	for _, ct := range clientTypes {
		dir, err := s.pluginInstallPath(ct, name, scope, projectRoot)
		if err != nil {
			return nil, fmt.Errorf("resolving %s install path of %q: %w", ct, name, err)
		}
		files, err := snapshotDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, fmt.Errorf("snapshotting %s copy of %q: %w", ct, name, err))
			continue
		}
		backups[ct] = files
	}
	if len(errs) > 0 {
		return backups, errors.Join(errs...)
	}
	if len(backups) == 0 {
		return nil, nil
	}
	return backups, nil
}

// restoreClientTrees writes snapshotClientTrees backups back over the live
// plugin directories and re-registers each restored client so marketplace
// and settings entries match the restored tree. Clients without a backup are
// dematerialized (they were newly added by the failed install).
func (s *service) restoreClientTrees(
	ctx context.Context,
	name string,
	scope plugins.Scope,
	projectRoot string,
	backups map[string]map[string]fileSnapshot,
	allClients []string,
) error {
	var errs []error
	restored := make(map[string]struct{}, len(backups))
	for ct, files := range backups {
		restored[ct] = struct{}{}
		dir, err := s.pluginInstallPath(ct, name, scope, projectRoot)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolving %s install path: %w", ct, err))
			continue
		}
		if err := restoreDir(dir, files); err != nil {
			errs = append(errs, fmt.Errorf("restoring %s plugin tree: %w", ct, err))
		}
		if adapter, ok := s.materializers[ct]; ok {
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
func snapshotDir(dir string) (map[string]fileSnapshot, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	files := make(map[string]fileSnapshot)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // path is under a GetPluginPath-validated directory
		if readErr != nil {
			return readErr
		}
		files[rel] = fileSnapshot{data: data, mode: sanitizeFileMode(info.Mode())}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// restoreDir replaces dir with the files captured by snapshotDir, writing
// each file with its sanitized mode.
func restoreDir(dir string, files map[string]fileSnapshot) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	var errs []error
	for rel, snap := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.WriteFile(path, snap.data, snap.mode); err != nil { //nolint:gosec // mode is masked to 0755
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
// every client present in s.materializers (additionally filtered by
// cm.SupportsPlugins when a client manager is configured). Explicit client
// names are validated to be present in s.materializers.
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
// have a configured materializer and (when a client manager is set) are
// considered plugin-supporting by it.
func (s *service) availableMaterializerClients() []string {
	var out []string
	for ct := range s.materializers {
		if s.clientManager != nil && !s.clientManager.SupportsPlugins(client.ClientApp(ct)) {
			continue
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
		Scope:        scope,
		ProjectRoot:  opts.ProjectRoot,
		Reference:    opts.Reference,
		Tag:          opts.Tag,
		Digest:       opts.Digest,
		Status:       plugins.InstallStatusInstalled,
		InstalledAt:  time.Now().UTC(),
		Clients:      clients,
		Components:   opts.Components,
		Dependencies: opts.Dependencies,
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
// install when the lock file feature is enabled. Empty when the install is
// not lock-scoped, so user-scope and ungated installs skip the extra extract.
func lockContentDigest(opts plugins.InstallOptions, scope plugins.Scope) (string, error) {
	if scope != plugins.ScopeProject || !plugins.LockFileFeatureEnabled() {
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
