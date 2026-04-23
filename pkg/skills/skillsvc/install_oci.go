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

	"github.com/stacklok/toolhive-core/httperr"
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

// resolveFromLocalStore attempts to resolve a skill name as a tag in the local
// OCI store. On success it hydrates opts with layer data, digest, and version
// from the artifact. Returns (true, nil) when resolved, (false, nil) when the
// tag is not found, or (false, err) on validation/extraction failure.
func (s *service) resolveFromLocalStore(ctx context.Context, opts *skills.InstallOptions) (bool, error) {
	d, err := s.ociStore.Resolve(ctx, opts.Name)
	if err != nil {
		// Tag not found in the local store — not an error, just unresolved.
		slog.Debug("skill name not found in local OCI store", "name", opts.Name, "error", err)
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

	opts.LayerData = layerData
	opts.Digest = d.String()
	if opts.Reference == "" {
		opts.Reference = opts.Name
	}
	if opts.Version == "" && skillConfig.Version != "" {
		opts.Version = skillConfig.Version
	}

	return true, nil
}
