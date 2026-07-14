// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
)

// Install installs a plugin. When the Name field contains a git reference
// (git://...), the repo is cloned and the plugin tree is built in memory. When
// it contains an OCI reference, the artifact is pulled and extracted. A plain
// name is resolved against the local OCI store, then the registry lookup.
// Mirror of skillsvc.Install, substituting the plugin install backends.
func (s *service) Install(ctx context.Context, opts plugins.InstallOptions) (*plugins.InstallResult, error) {
	scope, projectRoot, err := normalizeProjectRoot(opts.Scope, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	scope = defaultScope(scope)
	opts.ProjectRoot = projectRoot

	// Git references are dispatched first; the prefix is unambiguous and
	// cannot collide with OCI references.
	if gitresolver.IsGitReference(opts.Name) {
		result, err := s.installFromGit(ctx, opts, scope)
		if err != nil {
			return nil, err
		}
		return s.installAndRegister(ctx, result, opts.Group, result.Plugin.Metadata.Name, scope, opts.ProjectRoot)
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
		result, ociErr := s.installFromOCI(ctx, opts, scope, ref)
		if ociErr == nil {
			return s.installAndRegister(ctx, result, opts.Group, result.Plugin.Metadata.Name, scope, opts.ProjectRoot)
		}
		// No registry-name fallback yet (Phase-3 later wave); surface the
		// OCI pull error directly.
		return nil, ociErr
	}

	// Plain plugin name.
	if err := plugins.ValidatePluginName(opts.Name); err != nil {
		return nil, httperr.WithCode(err, http.StatusBadRequest)
	}

	return s.installByName(ctx, opts, scope)
}

// installByName handles installation for a validated plain plugin name. It
// checks the local OCI store, then the registry lookup, before returning an
// error. Mirror of skillsvc.installByName.
func (s *service) installByName(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
) (*plugins.InstallResult, error) {
	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()

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
			// Release the lock before registry lookup — installFromOCI
			// acquires its own lock on the plugin name, which could be the
			// same key, causing deadlock since sync.Mutex is not re-entrant.
			unlock()
			locked = false
			return s.installFromRegistryLookup(ctx, opts, scope)
		}
	}

	result, err := s.installWithExtraction(ctx, opts, scope)
	if err != nil {
		return nil, err
	}
	return s.installAndRegister(ctx, result, opts.Group, opts.Name, scope, opts.ProjectRoot)
}

// installFromRegistryLookup resolves a plain plugin name via the registry
// lookup and dispatches to the appropriate installer. When no lookup is
// configured or it returns no hits, returns a 404 with an install hint.
func (s *service) installFromRegistryLookup(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
) (*plugins.InstallResult, error) {
	if s.pluginLookup != nil {
		hits, err := s.pluginLookup.SearchPlugins(ctx, opts.Name)
		if err != nil {
			return nil, fmt.Errorf("searching registry for plugin %q: %w", opts.Name, err)
		}
		if len(hits) > 0 && len(hits[0].Packages) > 0 {
			pkg := hits[0].Packages[0]
			slog.Info("resolved plugin from registry", "name", opts.Name, "reference", pkg.Reference)
			opts.Name = pkg.Reference
			ref, _, parseErr := parseOCIReference(pkg.Reference)
			if parseErr != nil {
				return nil, httperr.WithCode(
					fmt.Errorf("registry returned invalid OCI reference %q: %w", pkg.Reference, parseErr),
					http.StatusUnprocessableEntity,
				)
			}
			result, ociErr := s.installFromOCI(ctx, opts, scope, ref)
			if ociErr != nil {
				return nil, ociErr
			}
			return s.installAndRegister(ctx, result, opts.Group, result.Plugin.Metadata.Name, scope, opts.ProjectRoot)
		}
	}

	return nil, httperr.WithCode(
		fmt.Errorf("plugin %q not found in local store or registry;"+
			" install by OCI reference:\n  thv plugin install ghcr.io/<namespace>/%s:<version>",
			opts.Name, opts.Name),
		http.StatusNotFound,
	)
}

// registerPluginInGroup adds the plugin to the requested group when a group
// manager is configured. When groupName is empty it defaults to the "default"
// group, matching workload behavior.
func (s *service) registerPluginInGroup(ctx context.Context, groupName string, pluginName string) error {
	if s.groupManager == nil {
		return nil
	}
	if groupName == "" {
		groupName = groups.DefaultGroup
	}
	return groups.AddPluginToGroup(ctx, s.groupManager, groupName, pluginName)
}

// installAndRegister registers the just-installed plugin in the target group.
// If group registration fails, the DB record is rolled back so a retry starts
// fresh. Mirror of skillsvc.installAndRegister.
func (s *service) installAndRegister(
	ctx context.Context,
	result *plugins.InstallResult,
	groupName string,
	pluginName string,
	scope plugins.Scope,
	projectRoot string,
) (*plugins.InstallResult, error) {
	if err := s.registerPluginInGroup(ctx, groupName, pluginName); err != nil {
		// Best-effort rollback: remove the DB record so retries start fresh.
		// Materialized files are left in place; a fresh install will overwrite
		// them (the adapters are idempotent under the same name/scope).
		_ = s.store.Delete(ctx, pluginName, scope, projectRoot)
		return nil, fmt.Errorf("registering plugin in group: %w", err)
	}
	return result, nil
}
