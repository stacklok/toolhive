// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	containerdigest "github.com/opencontainers/go-digest"
	"github.com/sigstore/sigstore-go/pkg/bundle"
)

// bundleFromAttestation retrieves the attestation bundles from the image reference. Note that the attestation
// bundles are stored as OCI image references. The function uses the referrers API to get the attestation. GitHub supports
// discovering the attestations via their API, but this is not supported here for now.
func bundleFromAttestation(imageRef string, keychain authn.Keychain) ([]sigstoreBundle, error) {
	var bundles []sigstoreBundle

	// Get the auth options
	opts := []remote.Option{remote.WithAuthFromKeychain(keychain)}

	// Get the image reference
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("error parsing image reference: %w", err)
	}

	// Get the image descriptor
	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("error getting image descriptor: %w", err)
	}

	// Get the digest
	digest := ref.Context().Digest(desc.Digest.String())

	// Get the digest in bytes
	digestByte, err := hex.DecodeString(desc.Digest.Hex)
	if err != nil {
		return nil, err
	}

	// Use the referrers API to get the attestation reference
	referrers, err := remote.Referrers(digest, opts...)
	if err != nil {
		return nil, fmt.Errorf("error getting referrers: %w, %s", ErrProvenanceNotFoundOrIncomplete, err.Error())
	}

	refManifest, err := referrers.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("error getting referrers manifest: %w, %s", ErrProvenanceNotFoundOrIncomplete, err.Error())
	}

	// Loop through all available attestations and extract the bundle
	for _, refDesc := range refManifest.Manifests {
		if !strings.HasPrefix(refDesc.ArtifactType, "application/vnd.dev.sigstore.bundle") {
			continue
		}
		refImg, err := remote.Image(ref.Context().Digest(refDesc.Digest.String()), opts...)
		if err != nil {
			slog.Debug("error getting referrer image", "error", err)
			continue
		}
		layers, err := refImg.Layers()
		if err != nil {
			slog.Debug("error getting referrer layers", "error", err)
			continue
		}
		layer0, err := layers[0].Uncompressed()
		if err != nil {
			slog.Debug("error uncompressing referrer layer", "error", err)
			continue
		}
		bundleBytes, err := io.ReadAll(layer0)
		if err != nil {
			slog.Debug("error reading referrer layer", "error", err)
			continue
		}
		b := &bundle.Bundle{}
		err = b.UnmarshalJSON(bundleBytes)
		if err != nil {
			slog.Debug("error unmarshalling bundle", "error", err)
			continue
		}

		bundles = append(bundles, sigstoreBundle{
			bundle:      b,
			digestBytes: digestByte,
			digestAlgo:  containerdigest.Canonical.String(),
		})
	}
	if len(bundles) == 0 {
		return nil, ErrProvenanceNotFoundOrIncomplete
	}
	return bundles, nil
}
