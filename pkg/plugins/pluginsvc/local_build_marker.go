// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"fmt"

	"github.com/opencontainers/go-digest"

	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
)

// LocalBuildAnnotation marks a tag in the local OCI store as produced by
// `thv plugin build` rather than an OCI pull (install, content preview).
// The value is always "true" when set; absence means "not a local build".
//
// This is the SAME annotation key as skillsvc.LocalBuildAnnotation. It is
// generic by design (reverse-DNS namespaced) so it composes with other
// locally-built artifact types. ListBuilds filters by isPluginArtifact before
// trusting it, so a skill local-build tag cannot masquerade as a plugin build
// and vice versa.
//
// The annotation is stamped at the descriptor level inside the store's root
// index.json, not on the manifest content. Two properties follow from that:
//
//  1. Push resolves the artifact by digest, which returns a plain descriptor
//     (oras-go's oci.Store strips annotations when the reference is a digest),
//     so the marker never crosses the wire.
//  2. Pull calls Store.Tag with the pulled digest, which also resolves by
//     digest before tagging, so pulled tags inherit no annotations and stay
//     invisible to ListBuilds.
const LocalBuildAnnotation = "dev.stacklok.toolhive.local-build"

// tagAsLocalBuild tags digest d with the given tag and stamps the local-build
// marker on the root-index descriptor entry. Equivalent to ociStore.Tag plus
// a descriptor annotation; callers must only invoke it from code paths that
// genuinely produced the artifact locally (currently only Build). Mirror
// skillsvc.tagAsLocalBuild, parameterized on *ociplugins.Store.
func tagAsLocalBuild(ctx context.Context, store *ociplugins.Store, d digest.Digest, tag string) error {
	target := store.Target()
	desc, err := target.Resolve(ctx, d.String())
	if err != nil {
		return fmt.Errorf("resolving digest for tag: %w", err)
	}
	// Resolve-by-digest returns a plain descriptor in oras-go, so overwriting
	// Annotations can't clobber anything meaningful on the descriptor itself.
	// (Content-level annotations live on the manifest/index blob and are
	// unaffected.)
	desc.Annotations = map[string]string{LocalBuildAnnotation: "true"}
	if err := target.Tag(ctx, desc, tag); err != nil {
		return fmt.Errorf("tagging artifact as local build: %w", err)
	}
	return nil
}

// isLocalBuild reports whether the given tag in the local OCI store carries
// the local-build marker. Tags created by OCI pulls do not carry it, so this
// is the filter used by ListBuilds to hide cached remote artifacts. Mirror
// skillsvc.isLocalBuild.
func isLocalBuild(ctx context.Context, store *ociplugins.Store, tag string) (bool, error) {
	desc, err := store.Target().Resolve(ctx, tag)
	if err != nil {
		return false, err
	}
	return desc.Annotations[LocalBuildAnnotation] == "true", nil
}
