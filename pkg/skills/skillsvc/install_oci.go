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
	"time"

	nameref "github.com/google/go-containerregistry/pkg/name"
	"github.com/opencontainers/go-digest"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/skills"
)

// ociPullTimeout is the maximum time allowed for pulling an OCI artifact.
const ociPullTimeout = 5 * time.Minute

// maxCompressedLayerSize is the maximum compressed layer size we'll load into
// memory. Skills are typically small (< 1MB compressed); this limit prevents a
// malicious artifact from causing OOM before the decompression limits kick in.
const maxCompressedLayerSize int64 = 50 * 1024 * 1024 // 50 MB

// installFromOCI pulls a skill artifact from a remote registry, extracts
// metadata and layer data, then delegates to the standard extraction flow.
func (s *service) installFromOCI(
	ctx context.Context,
	opts skills.InstallOptions,
	scope skills.Scope,
	ref nameref.Reference,
) (*skills.InstallResult, error) {
	if s.registry == nil || s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI registry is not configured"),
			http.StatusInternalServerError,
		)
	}
	if s.pathResolver == nil {
		return nil, httperr.WithCode(
			errors.New("path resolver is required for OCI installs"),
			http.StatusInternalServerError,
		)
	}

	ociRef := qualifiedOCIRef(ref)

	pullCtx, cancel := context.WithTimeout(ctx, ociPullTimeout)
	defer cancel()

	// Install pulls intentionally do NOT carry the local-build marker:
	// Registry.Pull tags by digest, which returns a plain descriptor from
	// the OCI store, so no annotations land on the root-index entry. The
	// pulled blobs stay in the OCI store as a cache, but the tag is invisible
	// to ListBuilds so installed remote skills don't appear as local builds.
	pulledDigest, err := s.registry.Pull(pullCtx, s.ociStore, ociRef)
	if err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("pulling OCI artifact %q: %w", ociRef, err),
			classifyPullError(err),
		)
	}

	layerData, skillConfig, err := s.extractOCIContent(ctx, pulledDigest)
	if err != nil {
		return nil, err
	}

	if err := skills.ValidateSkillName(skillConfig.Name); err != nil {
		return nil, httperr.WithCode(
			fmt.Errorf("skill artifact contains invalid name: %w", err),
			http.StatusUnprocessableEntity,
		)
	}

	// Supply chain defense: the declared skill name must match the last path
	// component of the OCI reference. The Agent Skills spec requires that the
	// name field matches the parent directory name; by extension, it should
	// match the repository name in the OCI reference. A mismatch could
	// indicate a supply chain attack (e.g., a trusted reference pointing to
	// an artifact that overwrites a different skill).
	repo := ref.Context().RepositoryStr()
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		repo = repo[idx+1:]
	}
	if repo != skillConfig.Name {
		return nil, httperr.WithCode(
			fmt.Errorf(
				"skill name %q in artifact does not match OCI reference repository %q",
				skillConfig.Name, repo,
			),
			http.StatusUnprocessableEntity,
		)
	}

	// Hydrate install options from the pulled artifact.
	opts.Name = skillConfig.Name
	opts.LayerData = layerData
	opts.Reference = ociRef
	opts.Digest = pulledDigest.String()
	if opts.Version == "" && skillConfig.Version != "" {
		opts.Version = skillConfig.Version
	}
	// Note: version is optional; if both are empty, install without a version.

	unlock := s.locks.lock(opts.Name, scope, opts.ProjectRoot)
	defer unlock()

	return s.installWithExtraction(ctx, opts, scope)
}

// resolveFromLocalStore attempts to resolve a skill name against the local
// OCI store. It first tries a direct tag lookup (the common "build then
// install by skill name" case). When that misses, or when the direct match's
// declared version does not satisfy opts.Version, it falls back to scanning
// local-build-marked tags for one whose declared skill name matches opts.Name
// (so users can install a build that was tagged with something other than
// the skill name, e.g. `--tag v0.0.1`). Returns (true, nil) when resolved
// (opts is hydrated with layer data, digest, reference, and version),
// (false, nil) when no match is found, or (false, err) on validation,
// extraction, or ambiguity failures.
func (s *service) resolveFromLocalStore(ctx context.Context, opts *skills.InstallOptions) (bool, error) {
	resolved, err := s.tryDirectLocalTag(ctx, opts)
	if err != nil || resolved {
		return resolved, err
	}
	return s.tryLocalBuildScan(ctx, opts)
}

// tryDirectLocalTag attempts to resolve opts.Name as a literal tag in the
// local OCI store and verifies the artifact's declared skill name matches.
// Returns (true, nil) when the tag resolves and both name and (if specified)
// version match. Returns (false, err) on supply-chain mismatch or extraction
// failure. Returns (false, nil) when no such tag exists OR when name matches
// but a caller-supplied version does not — letting the caller scan for a
// better match instead of erroring.
func (s *service) tryDirectLocalTag(
	ctx context.Context,
	opts *skills.InstallOptions,
) (bool, error) {
	d, err := s.ociStore.Resolve(ctx, opts.Name)
	if err != nil {
		// Tag not found in the local store — not an error, just unresolved.
		return false, nil
	}

	layerData, skillConfig, err := s.extractOCIContent(ctx, d)
	if err != nil {
		return false, err
	}

	if err := skills.ValidateSkillName(skillConfig.Name); err != nil {
		return false, httperr.WithCode(
			fmt.Errorf("local artifact contains invalid skill name: %w", err),
			http.StatusUnprocessableEntity,
		)
	}

	// Supply-chain defense: the skill name declared inside the artifact must
	// match the tag used to install it. A mismatch could indicate a
	// tampered or mis-tagged artifact.
	if skillConfig.Name != opts.Name {
		return false, httperr.WithCode(
			fmt.Errorf(
				"skill name %q in local artifact does not match install name %q",
				skillConfig.Name, opts.Name,
			),
			http.StatusUnprocessableEntity,
		)
	}

	// Version constraint: when the caller specifies a version, the direct
	// match must agree. If not, fall through to the scan so a sibling build
	// with the requested version can win.
	if opts.Version != "" && skillConfig.Version != opts.Version {
		return false, nil
	}

	hydrateOptsFromLocalBuild(opts, layerData, d, skillConfig, opts.Name)
	return true, nil
}

// tryLocalBuildScan walks local-build-marked tags for a skill artifact whose
// declared name (and version, when specified) matches opts. With one match
// it hydrates opts; with multiple, it applies the tag-equals-name tie-breaker
// before surfacing a 409 ambiguity error.
func (s *service) tryLocalBuildScan(
	ctx context.Context,
	opts *skills.InstallOptions,
) (bool, error) {
	matches, err := s.findLocalBuildsByName(ctx, opts.Name, opts.Version)
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		slog.Debug("skill name not found in local OCI store",
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
// tags for a skill with a given name (and optional version).
type localBuildMatch struct {
	Tag       string
	Digest    digest.Digest
	LayerData []byte
	Config    *ociskills.SkillConfig
}

// findLocalBuildsByName scans local-build-marked tags for skill artifacts
// whose declared name matches `name`. When version is non-empty, it also
// filters by the artifact's declared version. Tags without the local-build
// marker (e.g. install/content caches) and non-skill artifacts are skipped,
// mirroring ListBuilds. Per-tag inspection errors are logged and the tag is
// skipped, so a single corrupt entry does not poison the whole lookup.
func (s *service) findLocalBuildsByName(
	ctx context.Context,
	name, version string,
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

		isSkill, typeErr := s.isSkillArtifact(ctx, d)
		if typeErr != nil {
			slog.Debug("failed to check artifact type", "tag", tag, "error", typeErr)
			continue
		}
		if !isSkill {
			continue
		}

		layerData, cfg, extractErr := s.extractOCIContent(ctx, d)
		if extractErr != nil {
			slog.Debug("failed to extract local-build content", "tag", tag, "error", extractErr)
			continue
		}
		if cfg == nil || cfg.Name != name {
			continue
		}
		// Match `version` against either the artifact's declared version
		// (cfg.Version, populated from SKILL.md frontmatter) or the OCI tag
		// in the local store. SKILL.md frontmatter often omits the version
		// while the build tag carries it (e.g. `thv skill build --tag v0.0.1`),
		// and callers seeing `{tag: "v0.0.1"}` in GET /skills/builds naturally
		// pass that string as `version`. This also matches the OCI-name path,
		// which already treats `version` as the tag (see the splice in
		// install.go).
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

// pickLocalBuildMatch selects a single match among scan results. With one
// match it returns it directly; with multiple, it prefers the match whose
// tag exactly equals name (the "tagged with skill name" case). When more
// than one match remains and none has Tag == name, returns nil to signal
// the caller to surface an ambiguity error.
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

// ambiguousLocalBuildError builds a 409 error listing each ambiguous match's
// tag and version so the caller knows which builds collide. Returned only
// when the tag-equals-name tie-breaker fails to resolve a unique winner.
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
			"multiple local builds match skill %q; specify version to disambiguate. Candidates:\n%s",
			name, strings.Join(lines, "\n"),
		),
		http.StatusConflict,
	)
}

// hydrateOptsFromLocalBuild populates opts.LayerData, Digest, Reference, and
// Version from a resolved local-build artifact. Reference defaults to the tag
// in the local store so subsequent operations (e.g. push) can re-resolve it.
// A caller-supplied Version takes precedence over the artifact's declared
// version; a caller-supplied Reference is preserved.
func hydrateOptsFromLocalBuild(
	opts *skills.InstallOptions,
	layerData []byte,
	d digest.Digest,
	cfg *ociskills.SkillConfig,
	tag string,
) {
	opts.LayerData = layerData
	opts.Digest = d.String()
	if opts.Reference == "" {
		opts.Reference = tag
	}
	if opts.Version == "" && cfg != nil && cfg.Version != "" {
		opts.Version = cfg.Version
	}
}
