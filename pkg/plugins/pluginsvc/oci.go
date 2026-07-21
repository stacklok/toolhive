// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
)

// maxConfigSize bounds the OCI image config blob read into memory during
// content extraction. The config is just JSON metadata; ~1MB is plenty. The
// manifest-declared Config.Size is advisory (an attacker can declare a tiny
// config and store a large one), so we check before GetBlob.
const maxConfigSize int64 = 1024 * 1024 // 1 MB

// qualifiedOCIRef returns the full OCI reference string including the tag or
// digest. When the user omits a tag (e.g. "ghcr.io/org/plugin"),
// go-containerregistry's ParseReference defaults to "latest" internally but
// String() omits it. This function only appends the implicit tag when the
// original string does not already include one.
// Mirror skillsvc.qualifiedOCIRef.
func qualifiedOCIRef(ref nameref.Reference) string {
	s := ref.String()
	if _, ok := ref.(nameref.Digest); ok {
		return s // already has @sha256:...
	}
	if strings.Contains(s, ":") {
		return s // already has an explicit tag
	}
	return s + ":" + ref.Identifier()
}

// parseOCIReference parses a string that may be a plain plugin name or an OCI
// reference. The bool return reports whether name was structurally an OCI
// reference (contains '/', ':', or '@'). Mirror skillsvc.parseOCIReference.
func parseOCIReference(name string) (nameref.Reference, bool, error) {
	// Structural check: plugin names never contain '/', ':', or '@'.
	// OCI references always require at least one of these.
	if !strings.ContainsAny(name, "/:@") {
		return nil, false, nil
	}

	ref, err := nameref.ParseReference(name)
	if err != nil {
		return nil, true, err
	}
	return ref, true, nil
}

// isPluginArtifact reports whether the OCI descriptor at digest d carries
// ArtifactType == ArtifactTypePlugin. It inspects the top-level index or
// manifest without descending into layers, so it is cheap to call.
// Mirror skillsvc.isSkillArtifact.
func (s *service) isPluginArtifact(ctx context.Context, d digest.Digest) (bool, error) {
	isIndex, err := s.ociStore.IsIndex(ctx, d)
	if err != nil {
		return false, fmt.Errorf("checking OCI content type: %w", err)
	}

	if isIndex {
		index, indexErr := s.ociStore.GetIndex(ctx, d)
		if indexErr != nil {
			return false, fmt.Errorf("reading OCI index: %w", indexErr)
		}
		return index.ArtifactType == ociplugins.ArtifactTypePlugin, nil
	}

	manifestBytes, err := s.ociStore.GetManifest(ctx, d)
	if err != nil {
		return false, fmt.Errorf("reading OCI manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return false, fmt.Errorf("parsing OCI manifest: %w", err)
	}
	return manifest.ArtifactType == ociplugins.ArtifactTypePlugin, nil
}

// extractPluginOCIContent navigates the OCI content graph from a pulled digest,
// extracting the plugin config and raw layer data. Mirrors
// skillsvc.extractOCIContent, substituting ociplugins.PluginConfigFromImageConfig.
func (s *service) extractPluginOCIContent(ctx context.Context, d digest.Digest) ([]byte, *ociplugins.PluginConfig, error) {
	isIndex, err := s.ociStore.IsIndex(ctx, d)
	if err != nil {
		return nil, nil, fmt.Errorf("checking OCI content type: %w", err)
	}

	manifestDigest := d
	if isIndex {
		// Plugin content is platform-agnostic — all platforms share the same
		// layer, so we can use the first manifest in the index.
		index, indexErr := s.ociStore.GetIndex(ctx, d)
		if indexErr != nil {
			return nil, nil, fmt.Errorf("reading OCI index: %w", indexErr)
		}
		if len(index.Manifests) == 0 {
			return nil, nil, httperr.WithCode(
				errors.New("OCI index contains no manifests"),
				http.StatusUnprocessableEntity,
			)
		}
		manifestDigest = index.Manifests[0].Digest
	}

	manifestBytes, err := s.ociStore.GetManifest(ctx, manifestDigest)
	if err != nil {
		return nil, nil, fmt.Errorf("reading OCI manifest: %w", err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parsing OCI manifest: %w", err)
	}

	if len(manifest.Layers) == 0 {
		return nil, nil, httperr.WithCode(
			errors.New("OCI manifest contains no layers"),
			http.StatusUnprocessableEntity,
		)
	}

	// Plugins use a single-layer format (one tar.gz). Validate the first
	// (and only expected) layer.
	if manifest.Layers[0].MediaType != ocispec.MediaTypeImageLayerGzip {
		return nil, nil, httperr.WithCode(
			fmt.Errorf("unexpected layer media type %q, expected %q",
				manifest.Layers[0].MediaType, ocispec.MediaTypeImageLayerGzip),
			http.StatusUnprocessableEntity,
		)
	}

	// Extract plugin config from the OCI image config. Guard against an
	// oversized config blob before loading it: manifest.Config.Size is
	// advisory, so a malicious artifact could declare a tiny config and
	// store a large one.
	if manifest.Config.Size > maxConfigSize {
		return nil, nil, httperr.WithCode(
			fmt.Errorf("config blob size %d bytes exceeds maximum %d bytes",
				manifest.Config.Size, maxConfigSize),
			http.StatusUnprocessableEntity,
		)
	}
	configBytes, err := s.ociStore.GetBlob(ctx, manifest.Config.Digest)
	if err != nil {
		return nil, nil, fmt.Errorf("reading OCI config blob: %w", err)
	}

	var imgConfig ocispec.Image
	if err := json.Unmarshal(configBytes, &imgConfig); err != nil {
		return nil, nil, fmt.Errorf("parsing OCI image config: %w", err)
	}

	pluginConfig, err := ociplugins.PluginConfigFromImageConfig(&imgConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting plugin config from OCI artifact: %w", err)
	}

	// Guard against oversized layers before loading into memory.
	if manifest.Layers[0].Size > maxCompressedLayerSize {
		return nil, nil, httperr.WithCode(
			fmt.Errorf("compressed layer size %d bytes exceeds maximum %d bytes",
				manifest.Layers[0].Size, maxCompressedLayerSize),
			http.StatusUnprocessableEntity,
		)
	}

	// Extract the raw tar.gz layer data.
	layerData, err := s.ociStore.GetBlob(ctx, manifest.Layers[0].Digest)
	if err != nil {
		return nil, nil, fmt.Errorf("reading OCI layer blob: %w", err)
	}

	return layerData, pluginConfig, nil
}
