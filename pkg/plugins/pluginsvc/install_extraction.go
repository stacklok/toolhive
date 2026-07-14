// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
// skillsvc.isExtractionNoOp.
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
	if err := s.store.Update(ctx, pl); err != nil {
		s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot)
		return nil, err
	}
	return &plugins.InstallResult{Plugin: pl}, nil
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
	materialized, err := s.materializeForClients(ctx, opts, scope, allClients)
	if err != nil {
		return nil, err
	}
	pl := buildInstalledPlugin(opts, scope, allClients, nil)
	if err := s.store.Update(ctx, pl); err != nil {
		s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot)
		return nil, err
	}
	return &plugins.InstallResult{Plugin: pl}, nil
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
		s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot)
		return nil, err
	}
	return &plugins.InstallResult{Plugin: pl}, nil
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
			s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot)
			return nil, httperr.WithCode(
				fmt.Errorf("no materializer configured for client %q", ct),
				http.StatusInternalServerError,
			)
		}
		if _, err := adapter.Materialize(ctx, plugins.MaterializeRequest{
			Name:        opts.Name,
			LayerData:   opts.LayerData,
			Scope:       scope,
			ProjectRoot: opts.ProjectRoot,
			Components:  opts.Components,
		}); err != nil {
			s.dematerializeAll(ctx, materialized, opts.Name, scope, opts.ProjectRoot)
			return nil, fmt.Errorf("materializing plugin for client %q: %w", ct, err)
		}
		materialized = append(materialized, ct)
	}
	return materialized, nil
}

// dematerializeAll best-effort reverts materializations performed in this call.
// Errors are joined so a partial rollback still surfaces; the original install
// error is returned to the caller separately.
func (s *service) dematerializeAll(
	ctx context.Context,
	clientTypes []string,
	name string,
	scope plugins.Scope,
	projectRoot string,
) {
	for _, ct := range clientTypes {
		if adapter, ok := s.materializers[ct]; ok {
			_ = adapter.Dematerialize(ctx, plugins.DematerializeRequest{
				Name:        name,
				Scope:       scope,
				ProjectRoot: projectRoot,
			})
		}
	}
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
