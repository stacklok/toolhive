// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"
	"github.com/opencontainers/go-digest"

	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// installFromOCI pulls a plugin artifact from a remote registry, extracts
// metadata and layer data, then delegates to installWithExtraction. Mirror of
// skillsvc.installFromOCI, substituting the plugin supply-chain check
// (config.Name == OCI repo last segment) and hydrating Components/Dependencies
// from the plugin OCI config.
func (s *service) installFromOCI(
	ctx context.Context,
	opts plugins.InstallOptions,
	scope plugins.Scope,
	ref nameref.Reference,
) (*plugins.InstallResult, error) {
	if s.registry == nil || s.ociStore == nil {
		return nil, httperr.WithCode(
			fmt.Errorf("OCI registry is not configured"),
			http.StatusInternalServerError,
		)
	}
	if len(s.materializers) == 0 {
		return nil, httperr.WithCode(
			fmt.Errorf("no materializers configured for plugin installs"),
			http.StatusInternalServerError,
		)
	}

	ociRef := qualifiedOCIRef(ref)

	pullCtx, cancel := context.WithTimeout(ctx, ociPullTimeout)
	defer cancel()

	pulledDigest, err := s.registry.Pull(pullCtx, s.ociStore, ociRef)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("pulling OCI artifact %q: %w", ociRef, err),
			classifyPullError(err),
		)
	}

	layerData, pluginConfig, err := s.extractPluginOCIContent(ctx, pulledDigest)
	if err != nil {
		return nil, err
	}

	if err := plugins.ValidatePluginName(pluginConfig.Name); err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("plugin artifact contains invalid name: %w", err),
			http.StatusUnprocessableEntity,
		)
	}

	// Name/repo consistency check: the plugin name declared in the artifact
	// config must match the last path component of the OCI reference. This
	// prevents accidental clobbering when a ref resolves to an artifact that
	// self-declares a different plugin name. Note: this is a consistency check,
	// not publisher authenticity — pluginConfig.Name is the artifact's own
	// self-declared field, so it does not bind the artifact to a trusted
	// publisher. Signature/attestation verification is a separate concern.
	repo := ref.Context().RepositoryStr()
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		repo = repo[idx+1:]
	}
	if repo != pluginConfig.Name {
		return nil, httperr.WithCode(
			fmt.Errorf(
				"plugin name %q in artifact does not match OCI reference repository %q",
				pluginConfig.Name, repo,
			),
			http.StatusUnprocessableEntity,
		)
	}

	// Hydrate install options from the pulled artifact.
	opts.Name = pluginConfig.Name
	opts.LayerData = layerData
	opts.Reference = ociRef
	opts.Digest = pulledDigest.String()
	opts.Components = pluginConfig.Components
	opts.Description = pluginConfig.Description
	opts.Dependencies = requiresToDependencies(pluginConfig.Requires)
	if t, hasTag := tagFromRef(ref); hasTag {
		opts.Tag = t
	}
	if opts.Version == "" && pluginConfig.Version != "" {
		opts.Version = pluginConfig.Version
	}

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	return s.installWithExtraction(ctx, opts, scope)
}

// requiresToDependencies maps a plugin's declared `requires` OCI references
// (strings) into Dependency records. Name/Digest are left empty; they are
// resolved at dependency-install time (a later Phase-3 wave).
func requiresToDependencies(requires []string) []plugins.Dependency {
	if len(requires) == 0 {
		return nil
	}
	deps := make([]plugins.Dependency, 0, len(requires))
	for _, r := range requires {
		if r == "" {
			continue
		}
		deps = append(deps, plugins.Dependency{Reference: r})
	}
	return deps
}

// tagFromRef returns the OCI tag portion of ref (if any). Digest references
// and tag-less references report hasTag=false.
func tagFromRef(ref nameref.Reference) (string, bool) {
	if _, isDigest := ref.(nameref.Digest); isDigest {
		return "", false
	}
	if t, ok := ref.(nameref.Tag); ok {
		return t.TagStr(), true
	}
	return "", false
}

// resolveFromLocalStore attempts to resolve a plugin name against the local
// OCI store. It first tries a direct tag lookup; when that misses, it falls
// back to scanning local-build-marked tags for one whose declared plugin name
// matches. Returns (true, nil) when resolved (opts hydrated), (false, nil)
// when no match found. Mirror of skillsvc.resolveFromLocalStore.
func (s *service) resolveFromLocalStore(ctx context.Context, opts *plugins.InstallOptions) (bool, error) {
	resolved, err := s.tryDirectLocalTag(ctx, opts)
	if err != nil || resolved {
		return resolved, err
	}
	return s.tryLocalBuildScan(ctx, opts)
}

// tryDirectLocalTag attempts to resolve opts.Name as a literal tag in the
// local OCI store and verifies the artifact's declared plugin name matches.
func (s *service) tryDirectLocalTag(
	ctx context.Context,
	opts *plugins.InstallOptions,
) (bool, error) {
	d, err := s.ociStore.Resolve(ctx, opts.Name)
	if err != nil {
		return false, nil
	}

	layerData, pluginConfig, err := s.extractPluginOCIContent(ctx, d)
	if err != nil {
		return false, err
	}

	if err := plugins.ValidatePluginName(pluginConfig.Name); err != nil {
		return false, httperr.WithCode(
			fmt.Errorf("local artifact contains invalid plugin name: %w", err),
			http.StatusUnprocessableEntity,
		)
	}

	if pluginConfig.Name != opts.Name {
		return false, httperr.WithCode(
			fmt.Errorf(
				"plugin name %q in local artifact does not match install name %q",
				pluginConfig.Name, opts.Name,
			),
			http.StatusUnprocessableEntity,
		)
	}

	if opts.Version != "" && pluginConfig.Version != opts.Version {
		return false, nil
	}

	hydrateOptsFromLocalBuild(opts, layerData, d, pluginConfig, opts.Name)
	return true, nil
}

// tryLocalBuildScan walks local-build-marked tags for a plugin artifact whose
// declared name (and version, when specified) matches opts.
func (s *service) tryLocalBuildScan(
	ctx context.Context,
	opts *plugins.InstallOptions,
) (bool, error) {
	matches, err := s.findLocalBuildsByName(ctx, opts.Name, opts.Version)
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		slog.Debug("plugin name not found in local OCI store",
			"name", opts.Name, "version", opts.Version)
		return false, nil
	}

	chosen := pickLocalBuildMatch(matches, opts.Name)
	if chosen == nil {
		return false, ambiguousLocalBuildError(opts.Name, matches)
	}
	hydrateOptsFromLocalBuild(opts, chosen.LayerData, chosen.Digest, chosen.Config, chosen.Tag)
	return true, nil
}

// localBuildMatch is a candidate match found by scanning local-build-marked
// tags for a plugin with a given name.
type localBuildMatch struct {
	Tag       string
	Digest    digest.Digest
	LayerData []byte
	Config    *ociplugins.PluginConfig
}

// findLocalBuildsByName scans local-build-marked tags for plugin artifacts
// whose declared name matches `name`. Mirror of skillsvc.findLocalBuildsByName.
func (s *service) findLocalBuildsByName(
	ctx context.Context, name, version string,
) ([]localBuildMatch, error) {
	tags, err := s.ociStore.ListTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing local OCI tags: %w", err)
	}

	var matches []localBuildMatch
	for _, tag := range tags {
		local, markerErr := isLocalBuild(ctx, s.ociStore, tag)
		if markerErr != nil {
			slog.Debug("failed to read local-build marker", "tag", tag, "error", markerErr)
			continue
		}
		if !local {
			continue
		}

		d, resolveErr := s.ociStore.Resolve(ctx, tag)
		if resolveErr != nil {
			slog.Debug("failed to resolve local-build tag", "tag", tag, "error", resolveErr)
			continue
		}

		isPlugin, typeErr := s.isPluginArtifact(ctx, d)
		if typeErr != nil {
			slog.Debug("failed to check artifact type", "tag", tag, "error", typeErr)
			continue
		}
		if !isPlugin {
			continue
		}

		layerData, cfg, extractErr := s.extractPluginOCIContent(ctx, d)
		if extractErr != nil {
			slog.Debug("failed to extract local-build content", "tag", tag, "error", extractErr)
			continue
		}
		if cfg == nil || cfg.Name != name {
			continue
		}
		if version != "" && cfg.Version != version && tag != version {
			continue
		}

		matches = append(matches, localBuildMatch{
			Tag:       tag,
			Digest:    d,
			LayerData: layerData,
			Config:    cfg,
		})
	}
	return matches, nil
}

// pickLocalBuildMatch selects a single match among scan results. Mirror of
// skillsvc.pickLocalBuildMatch.
func pickLocalBuildMatch(matches []localBuildMatch, name string) *localBuildMatch {
	if len(matches) == 1 {
		return &matches[0]
	}
	for i := range matches {
		if matches[i].Tag == name {
			return &matches[i]
		}
	}
	return nil
}

// ambiguousLocalBuildError builds a 409 error listing each ambiguous match.
func ambiguousLocalBuildError(name string, matches []localBuildMatch) error {
	lines := make([]string, 0, len(matches))
	for _, m := range matches {
		var version string
		if m.Config != nil {
			version = m.Config.Version
		}
		lines = append(lines, fmt.Sprintf("  - tag %q, version %q", m.Tag, version))
	}
	return httperr.WithCode(
		fmt.Errorf(
			"multiple local builds match plugin %q; specify version to disambiguate. Candidates:\n%s",
			name, strings.Join(lines, "\n"),
		),
		http.StatusConflict,
	)
}

// hydrateOptsFromLocalBuild populates opts from a resolved local-build artifact.
func hydrateOptsFromLocalBuild(
	opts *plugins.InstallOptions,
	layerData []byte,
	d digest.Digest,
	cfg *ociplugins.PluginConfig,
	tag string,
) {
	opts.LayerData = layerData
	opts.Digest = d.String()
	if opts.Reference == "" {
		opts.Reference = tag
	}
	if cfg != nil {
		opts.Components = cfg.Components
		opts.Description = cfg.Description
		opts.Dependencies = requiresToDependencies(cfg.Requires)
		if opts.Version == "" && cfg.Version != "" {
			opts.Version = cfg.Version
		}
	}
}
