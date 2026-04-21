// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

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
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
)

// qualifiedOCIRef returns the full OCI reference string including the tag or
// digest. When the user omits a tag (e.g. "ghcr.io/org/skill"),
// go-containerregistry's ParseReference defaults to "latest" internally but
// String() omits it. This function only appends the implicit tag when the
// original string does not already include one.
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

func parseOCIReference(name string) (nameref.Reference, bool, error) {
	// Structural check: skill names never contain '/', ':', or '@'.
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

// isUnambiguousOCIRef reports whether raw was clearly intended by the user as
// an OCI reference, meaning a failed pull must NOT fall back to a registry
// catalogue lookup. A ref is unambiguous if any of the following hold:
//
//   - the parsed form is a digest reference (e.g. "name@sha256:...")
//   - the raw string contains ':' (explicit tag such as "name:v1")
//   - the raw string has more than one '/' (multi-segment path such as
//     "ghcr.io/org/skill")
//
// The parsed Reference alone is insufficient for the tag case:
// nameref.ParseReference normalizes "foo/bar" to "foo/bar:latest" (a name.Tag),
// making it indistinguishable from an explicitly tagged reference. We therefore
// rely on the parsed form for the digest check and fall back to string
// inspection for the tag and segment-count checks.
func isUnambiguousOCIRef(raw string, ref nameref.Reference) bool {
	if _, isDigest := ref.(nameref.Digest); isDigest {
		return true
	}
	return strings.Contains(raw, ":") || strings.Count(raw, "/") > 1
}

// isSkillArtifact reports whether the OCI descriptor at digest d carries
// ArtifactType == ArtifactTypeSkill. It inspects the top-level index or
// manifest without descending into layers, so it is cheap to call.
func (s *service) isSkillArtifact(ctx context.Context, d digest.Digest) (bool, error) {
	isIndex, err := s.ociStore.IsIndex(ctx, d)
	if err != nil {
		return false, fmt.Errorf("checking OCI content type: %w", err)
	}

	if isIndex {
		index, indexErr := s.ociStore.GetIndex(ctx, d)
		if indexErr != nil {
			return false, fmt.Errorf("reading OCI index: %w", indexErr)
		}
		return index.ArtifactType == ociskills.ArtifactTypeSkill, nil
	}

	manifestBytes, err := s.ociStore.GetManifest(ctx, d)
	if err != nil {
		return false, fmt.Errorf("reading OCI manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return false, fmt.Errorf("parsing OCI manifest: %w", err)
	}
	return manifest.ArtifactType == ociskills.ArtifactTypeSkill, nil
}

// extractOCIContent navigates the OCI content graph from a pulled digest,
// extracting the skill config and raw layer data.
func (s *service) extractOCIContent(ctx context.Context, d digest.Digest) ([]byte, *ociskills.SkillConfig, error) {
	isIndex, err := s.ociStore.IsIndex(ctx, d)
	if err != nil {
		return nil, nil, fmt.Errorf("checking OCI content type: %w", err)
	}

	manifestDigest := d
	if isIndex {
		// Skill content is platform-agnostic — all platforms share the same
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

	// Skills use a single-layer format (one tar.gz). Validate the first
	// (and only expected) layer.
	if manifest.Layers[0].MediaType != ocispec.MediaTypeImageLayerGzip {
		return nil, nil, httperr.WithCode(
			fmt.Errorf("unexpected layer media type %q, expected %q",
				manifest.Layers[0].MediaType, ocispec.MediaTypeImageLayerGzip),
			http.StatusUnprocessableEntity,
		)
	}

	// Extract skill config from the OCI image config.
	configBytes, err := s.ociStore.GetBlob(ctx, manifest.Config.Digest)
	if err != nil {
		return nil, nil, fmt.Errorf("reading OCI config blob: %w", err)
	}

	var imgConfig ocispec.Image
	if err := json.Unmarshal(configBytes, &imgConfig); err != nil {
		return nil, nil, fmt.Errorf("parsing OCI image config: %w", err)
	}

	skillConfig, err := ociskills.SkillConfigFromImageConfig(&imgConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting skill config from OCI artifact: %w", err)
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

	return layerData, skillConfig, nil
}
