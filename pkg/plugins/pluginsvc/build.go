// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

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
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// Validate checks whether a plugin definition is valid. It runs the
// toolhive-side pre-build checks (filesystem safety + manifest strictness +
// name + bundled-skills) WITHOUT requiring a packager or OCI store to be
// configured. The packager re-walks and re-validates at build time, so Build
// does NOT call ValidatePluginDir (that would double-walk).
func (*service) Validate(_ context.Context, path string) (*plugins.ValidationResult, error) {
	if err := validateLocalPath(path); err != nil {
		return nil, err
	}
	return plugins.ValidatePluginDir(path)
}

// Build packages a plugin directory into a local OCI artifact. Mirrors
// skillsvc.Build: validateLocalPath → packager.Package → tag resolution →
// tagAsLocalBuild.
func (s *service) Build(ctx context.Context, opts plugins.BuildOptions) (*plugins.BuildResult, error) {
	if s.packager == nil || s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI packaging is not configured"),
			http.StatusInternalServerError,
		)
	}
	if err := validateLocalPath(opts.Path); err != nil {
		return nil, err
	}
	result, err := s.packager.Package(ctx, opts.Path, ociplugins.DefaultPackageOptions())
	if err != nil {
		// User-input failures (missing manifest, bad manifest, symlinks,
		// size/count limits, unreadable directory) are surfaced as 400 with
		// the packager's message intact. Anything else is a real 500.
		switch {
		case errors.Is(err, ociplugins.ErrInvalidPluginDir),
			errors.Is(err, ociplugins.ErrPluginManifestMissing),
			errors.Is(err, ociplugins.ErrInvalidPluginManifest),
			errors.Is(err, ociplugins.ErrInvalidPluginFile),
			errors.Is(err, ociplugins.ErrTooManyFiles),
			errors.Is(err, ociplugins.ErrPluginTooLarge):
			return nil, httperr.WithCode(err, http.StatusBadRequest)
		}
		return nil, fmt.Errorf("packaging plugin: %w", err)
	}

	// Tag resolution precedence:
	// 1. Explicit tag from BuildOptions.Tag
	// 2. Plugin name from the parsed config (plugin.json)
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
		// Tag the artifact and stamp the local-build marker on the root-index
		// descriptor entry so ListBuilds can distinguish this tag from ones
		// created by OCI pulls (install, content preview). The marker lives at
		// the descriptor level in index.json, not in the manifest blob, so it
		// doesn't change the artifact digest and is not carried across push.
		if tagErr := tagAsLocalBuild(ctx, s.ociStore, result.IndexDigest, tag); tagErr != nil {
			return nil, fmt.Errorf("tagging artifact: %w", tagErr)
		}
	}

	ref := func() string {
		if tag != "" {
			return tag
		}
		return result.IndexDigest.String()
	}()

	return &plugins.BuildResult{Reference: ref}, nil
}

// Push pushes a locally built plugin artifact to a remote OCI registry.
func (s *service) Push(ctx context.Context, opts plugins.PushOptions) error {
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

// ListBuilds returns all locally-built OCI plugin artifacts in the local store.
// Tags are filtered by the local-build descriptor annotation (set by Build),
// so artifacts pulled into the store by install or the content API for caching
// do not appear here.
func (s *service) ListBuilds(ctx context.Context) ([]plugins.LocalBuild, error) {
	if s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI packaging is not configured"),
			http.StatusInternalServerError,
		)
	}

	tags, err := s.ociStore.ListTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing OCI tags: %w", err)
	}

	builds := make([]plugins.LocalBuild, 0, len(tags))
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
			slog.Debug("failed to resolve tag in local OCI store", "tag", tag, "error", resolveErr)
			continue
		}

		isPlugin, typeErr := s.isPluginArtifact(ctx, d)
		if typeErr != nil {
			slog.Debug("failed to check artifact type in local OCI store", "tag", tag, "error", typeErr)
			continue
		}
		if !isPlugin {
			continue
		}

		build := plugins.LocalBuild{
			Tag:    tag,
			Digest: d.String(),
		}

		// Best-effort: enrich with plugin metadata from the OCI config labels.
		if _, cfg, extractErr := s.extractPluginOCIContent(ctx, d); extractErr == nil && cfg != nil {
			build.Name = cfg.Name
			build.Description = cfg.Description
			build.Version = cfg.Version
		} else if extractErr != nil {
			slog.Debug("failed to extract plugin config from local build", "tag", tag, "error", extractErr)
		}

		builds = append(builds, build)
	}

	return builds, nil
}

// DeleteBuild removes a locally-built OCI plugin artifact from the local store.
// It deletes the tag and, when no other tag shares the same digest, also
// garbage-collects all associated blobs. The local-build descriptor annotation
// disappears from index.json together with the tag.
func (s *service) DeleteBuild(ctx context.Context, tag string) error {
	if s.ociStore == nil {
		return httperr.WithCode(
			errors.New("OCI packaging is not configured"),
			http.StatusInternalServerError,
		)
	}
	return s.ociStore.DeleteBuild(ctx, tag)
}

// validateLocalPath checks that a path is non-empty, absolute, and does not
// contain ".." path traversal segments. This prevents API clients from
// accessing arbitrary directories on the host filesystem via traversal.
// Mirror skillsvc.validateLocalPath.
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
// OCI reference ("ghcr.io/org/repo:v1.0.0"). The --tag flag in `thv plugin
// build` supports both forms (matching `docker build -t` semantics), so we
// route to the appropriate parser based on the presence of '/', ':', or '@'.
// Mirror skillsvc.validateOCITagOrReference.
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
