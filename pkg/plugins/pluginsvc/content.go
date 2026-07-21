// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/stacklok/toolhive-core/httperr"
	ociartifact "github.com/stacklok/toolhive-core/oci/artifact"
	"github.com/stacklok/toolhive/pkg/plugins"
)

// ociPullTimeout is the maximum time allowed for pulling an OCI plugin artifact
// during content preview. Mirrors skillsvc.ociPullTimeout.
const ociPullTimeout = 5 * time.Minute

// maxCompressedLayerSize is the maximum compressed layer size loaded into
// memory during content extraction. Mirrors skillsvc.maxCompressedLayerSize.
const maxCompressedLayerSize int64 = 50 * 1024 * 1024 // 50 MB

// GetContent retrieves the .claude-plugin/plugin.json body and file listing
// from a plugin artifact without installing it. The reference may be:
//   - A local build tag (e.g. "my-plugin")
//   - A fully-qualified OCI reference (e.g. "ghcr.io/org/plugin:v1")
//
// Phase 2 implements OCI resolution only (local store, then remote pull). Git
// and registry-name fallback paths are Phase 3 (#5527) and are intentionally
// not stubbed: a non-OCI, non-local reference returns 400 rather than silently
// succeeding via a resolver that isn't configured.
func (s *service) GetContent(ctx context.Context, opts plugins.ContentOptions) (*plugins.PluginContent, error) {
	ref := opts.Reference
	if ref == "" {
		return nil, httperr.WithCode(
			errors.New("reference is required"),
			http.StatusBadRequest,
		)
	}

	return s.getContentFromOCI(ctx, ref)
}

// getContentFromOCI resolves a reference from the local OCI store or pulls it
// from a remote registry, then extracts the plugin.json content. Mirrors
// skillsvc.getContentFromOCI but WITHOUT git/registry fallback paths. The
// decompression reuses oci/artifact.DecompressTar and oci/artifact.FileEntry —
// these are generic tar.gz utilities (the toolhive-core oci/plugins package
// does not re-export a Decompress helper).
func (s *service) getContentFromOCI(ctx context.Context, ref string) (*plugins.PluginContent, error) {
	if s.ociStore == nil {
		return nil, httperr.WithCode(
			errors.New("OCI store is not configured"),
			http.StatusInternalServerError,
		)
	}

	// Try the local store first (covers local builds by tag name and
	// previously pulled remote refs tagged by Pull).
	d, resolveErr := s.ociStore.Resolve(ctx, ref)
	if resolveErr != nil {
		if s.registry == nil {
			return nil, httperr.WithCode(
				fmt.Errorf("reference %q not found in local store and OCI registry is not configured", ref),
				http.StatusBadRequest,
			)
		}

		ociRef, isOCI, parseErr := parseOCIReference(ref)
		if parseErr != nil {
			return nil, httperr.WithCode(
				fmt.Errorf("invalid reference %q: %w", ref, parseErr),
				http.StatusBadRequest,
			)
		}
		if !isOCI {
			return nil, httperr.WithCode(
				fmt.Errorf("reference %q not found in local store and is not a valid OCI reference", ref),
				http.StatusBadRequest,
			)
		}

		qualifiedRef := qualifiedOCIRef(ociRef)
		pullCtx, cancel := context.WithTimeout(ctx, ociPullTimeout)
		defer cancel()

		// Content-preview pulls intentionally do NOT carry the local-build
		// marker: Registry.Pull tags by digest, which returns a plain
		// descriptor from the OCI store, so no annotations land on the
		// root-index entry. The pulled blobs stay in the OCI store as a cache,
		// but the tag is invisible to ListBuilds so remote plugins browsed via
		// the content API don't pollute the local builds listing.
		var pullErr error
		d, pullErr = s.registry.Pull(pullCtx, s.ociStore, qualifiedRef)
		if pullErr != nil {
			return nil, httperr.WithCode(
				fmt.Errorf("pulling OCI artifact %q: %w", qualifiedRef, pullErr),
				classifyPullError(pullErr),
			)
		}
	}

	layerData, pluginConfig, err := s.extractPluginOCIContent(ctx, d)
	if err != nil {
		return nil, err
	}

	entries, err := ociartifact.DecompressTar(layerData)
	if err != nil {
		return nil, fmt.Errorf("decompressing plugin layer: %w", err)
	}

	content := &plugins.PluginContent{
		Name:        pluginConfig.Name,
		Description: pluginConfig.Description,
		Version:     pluginConfig.Version,
		License:     pluginConfig.License,
		Files:       make([]plugins.PluginFileEntry, 0, len(entries)),
	}

	for _, entry := range entries {
		content.Files = append(content.Files, plugins.PluginFileEntry{
			Path: entry.Path,
			Size: len(entry.Content),
		})
		// The plugin manifest lives at .claude-plugin/plugin.json
		// (plugins.ManifestPath == ociplugins.ManifestFileName). Match
		// case-insensitively on the path for robustness against path-separator
		// differences in the tar archive.
		if strings.EqualFold(filepath.ToSlash(entry.Path), plugins.ManifestPath) {
			content.Manifest = string(entry.Content)
		}
	}

	return content, nil
}
