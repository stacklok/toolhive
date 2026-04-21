// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	"github.com/stacklok/toolhive/pkg/skills"
)

// Validate checks whether a skill definition is valid.
func (*service) Validate(_ context.Context, path string) (*skills.ValidationResult, error) {
	if err := validateLocalPath(path); err != nil {
		return nil, err
	}
	return skills.ValidateSkillDir(path)
}

// Build packages a skill directory into a local OCI artifact.
func (s *service) Build(ctx context.Context, opts skills.BuildOptions) (*skills.BuildResult, error) {
	if s.packager == nil || s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI packaging is not configured"),
			http.StatusInternalServerError,
		)
	}
	if err := validateLocalPath(opts.Path); err != nil {
		return nil, err
	}
	result, err := s.packager.Package(ctx, opts.Path, ociskills.DefaultPackageOptions())
	if err != nil {
		return nil, fmt.Errorf("packaging skill: %w", err)
	}

	// Tag resolution precedence:
	// 1. Explicit tag from BuildOptions.Tag
	// 2. Skill name from the parsed config (SKILL.md frontmatter)
	// 3. No tag — use raw digest as the reference
	tag := func() string {
		if opts.Tag != "" {
			return opts.Tag
		}
		if result.Config != nil && result.Config.Name != "" {
			return result.Config.Name
		}
		return ""
	}()

	if tag != "" {
		if err := validateOCITagOrReference(tag); err != nil {
			return nil, err
		}
	}

	if tag != "" {
		if tagErr := s.ociStore.Tag(ctx, result.IndexDigest, tag); tagErr != nil {
			return nil, fmt.Errorf("tagging artifact: %w", tagErr)
		}
		// Record build provenance so ListBuilds surfaces this tag. Tags
		// created by OCI pulls (install, content preview) deliberately skip
		// this step and stay hidden from ListBuilds while their blobs remain
		// as a cache.
		if s.provenance != nil {
			if recErr := s.provenance.Record(tag, result.IndexDigest.String()); recErr != nil {
				return nil, fmt.Errorf("recording build provenance: %w", recErr)
			}
		}
	}

	ref := func() string {
		if tag != "" {
			return tag
		}
		return result.IndexDigest.String()
	}()

	return &skills.BuildResult{Reference: ref}, nil
}

// Push pushes a locally built skill artifact to a remote OCI registry.
func (s *service) Push(ctx context.Context, opts skills.PushOptions) error {
	if s.registry == nil || s.ociStore == nil {
		return httperr.WithCode(
			errors.New("OCI registry is not configured"),
			http.StatusInternalServerError,
		)
	}
	if opts.Reference == "" {
		return httperr.WithCode(
			errors.New("reference is required"),
			http.StatusBadRequest,
		)
	}

	d, err := s.ociStore.Resolve(ctx, opts.Reference)
	if err != nil {
		slog.Debug("failed to resolve OCI reference", "reference", opts.Reference, "error", err)
		return httperr.WithCode(
			fmt.Errorf("reference %q not found in local store", opts.Reference),
			http.StatusNotFound,
		)
	}

	if err := s.registry.Push(ctx, s.ociStore, d, opts.Reference); err != nil {
		return fmt.Errorf("pushing to registry: %w", err)
	}

	return nil
}

// ListBuilds returns all locally-built OCI skill artifacts in the local store.
// It iterates the build provenance index (populated by Build) rather than the
// full tag list, so artifacts pulled into the store by install or the content
// API for caching do not appear here.
func (s *service) ListBuilds(ctx context.Context) ([]skills.LocalBuild, error) {
	if s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI packaging is not configured"),
			http.StatusInternalServerError,
		)
	}
	if s.provenance == nil {
		// ociStore is set but provenance failed to initialize; treat as
		// empty rather than surfacing every pulled tag.
		return []skills.LocalBuild{}, nil
	}

	entries, err := s.provenance.List()
	if err != nil {
		return nil, fmt.Errorf("listing build provenance: %w", err)
	}

	builds := make([]skills.LocalBuild, 0, len(entries))
	for _, entry := range entries {
		d, resolveErr := s.ociStore.Resolve(ctx, entry.Tag)
		if resolveErr != nil {
			// Tag no longer resolves (e.g. deleted outside of DeleteBuild).
			// Prune the stale provenance entry and skip.
			slog.Debug("pruning stale provenance entry", "tag", entry.Tag, "error", resolveErr)
			if forgetErr := s.provenance.Forget(entry.Tag); forgetErr != nil {
				slog.Debug("failed to prune stale provenance entry", "tag", entry.Tag, "error", forgetErr)
			}
			continue
		}

		isSkill, typeErr := s.isSkillArtifact(ctx, d)
		if typeErr != nil {
			slog.Debug("failed to check artifact type in local OCI store", "tag", entry.Tag, "error", typeErr)
			continue
		}
		if !isSkill {
			continue
		}

		build := skills.LocalBuild{
			Tag:    entry.Tag,
			Digest: d.String(),
		}

		// Best-effort: enrich with skill metadata from the OCI config labels.
		if _, cfg, extractErr := s.extractOCIContent(ctx, d); extractErr == nil && cfg != nil {
			build.Name = cfg.Name
			build.Description = cfg.Description
			build.Version = cfg.Version
		} else if extractErr != nil {
			slog.Debug("failed to extract skill config from local build", "tag", entry.Tag, "error", extractErr)
		}

		builds = append(builds, build)
	}

	return builds, nil
}

// DeleteBuild removes a locally-built OCI skill artifact from the local store.
// It deletes the tag and, when no other tag shares the same digest, also
// garbage-collects all associated blobs. The provenance entry is also forgotten.
func (s *service) DeleteBuild(ctx context.Context, tag string) error {
	if s.ociStore == nil {
		return httperr.WithCode(
			errors.New("OCI packaging is not configured"),
			http.StatusInternalServerError,
		)
	}
	if err := s.ociStore.DeleteBuild(ctx, tag); err != nil {
		return err
	}
	if s.provenance != nil {
		if forgetErr := s.provenance.Forget(tag); forgetErr != nil {
			// The underlying artifact is gone; failing to clean up the
			// provenance entry is non-fatal — ListBuilds will prune stale
			// entries on the next call.
			slog.Debug("failed to forget provenance entry", "tag", tag, "error", forgetErr)
		}
	}
	return nil
}

// validateLocalPath checks that a path is non-empty, absolute, and does not
// contain ".." path traversal segments. This prevents API clients from
// accessing arbitrary directories on the host filesystem via traversal.
func validateLocalPath(path string) error {
	if path == "" {
		return httperr.WithCode(errors.New("path is required"), http.StatusBadRequest)
	}
	if strings.ContainsRune(path, 0) {
		return httperr.WithCode(errors.New("path contains null bytes"), http.StatusBadRequest)
	}
	if !filepath.IsAbs(path) {
		return httperr.WithCode(
			fmt.Errorf("path must be absolute, got %q", path),
			http.StatusBadRequest,
		)
	}
	// Check the raw path for ".." segments before cleaning resolves them.
	// This catches traversal attempts like /safe/dir/../../../etc.
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".." {
			return httperr.WithCode(errors.New("path must not contain '..' traversal segments"), http.StatusBadRequest)
		}
	}
	return nil
}

// validateOCITagOrReference accepts either a bare OCI tag ("v1.0.0") or a full
// OCI reference ("ghcr.io/org/repo:v1.0.0"). The --tag flag in `thv skill build`
// supports both forms (matching `docker build -t` semantics), so we route to
// the appropriate parser based on the presence of '/', ':', or '@'.
func validateOCITagOrReference(value string) error {
	if strings.ContainsAny(value, "/:@") {
		// Looks like a full OCI reference — validate as such.
		if _, err := nameref.ParseReference(value, nameref.StrictValidation); err != nil {
			return httperr.WithCode(
				fmt.Errorf("invalid OCI reference or tag %q: %w", value, err),
				http.StatusBadRequest,
			)
		}
		return nil
	}
	// Bare tag — construct a dummy reference to validate the tag portion.
	if _, err := nameref.NewTag("dummy.invalid/repo:"+value, nameref.StrictValidation); err != nil {
		return httperr.WithCode(
			fmt.Errorf("invalid OCI reference or tag %q: %w", value, err),
			http.StatusBadRequest,
		)
	}
	return nil
}
