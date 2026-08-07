// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package signer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/opencontainers/go-digest"
)

const (
	mediaTypeCosignSimpleSigningV1JSON = "application/vnd.dev.cosign.simplesigning.v1+json"
	annotationCosignSignature          = "dev.cosignproject.cosign/signature"
)

// cosignSimpleSigning is the payload cosign signs: it embeds the artifact's
// manifest digest, binding the signature to the exact artifact content.
type cosignSimpleSigning struct {
	Critical cosignCritical `json:"critical"`
}

type cosignCritical struct {
	Identity cosignIdentity `json:"identity"`
	Image    cosignImage    `json:"image"`
	Type     string         `json:"type"`
}

type cosignIdentity struct {
	DockerReference string `json:"docker-reference"`
}

type cosignImage struct {
	DockerManifestDigest string `json:"docker-manifest-digest"`
}

// SimpleSigningPayload builds the canonical simple-signing payload for the
// artifact at ref pinned to digestStr. This payload — not the manifest
// digest — is what gets signed, per the cosign convention: a verifier
// recovers the payload from the signature manifest's layer, checks the
// signature over it, and reads the bound manifest digest out of it.
// Exported because offline re-verification of a stored key-signed bundle
// must reconstruct exactly these bytes to check the signature's binding.
func SimpleSigningPayload(imageRef, digestStr string) ([]byte, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("parsing image reference: %w", err)
	}
	d, err := parseManifestDigest(digestStr)
	if err != nil {
		return nil, err
	}
	payload := cosignSimpleSigning{
		Critical: cosignCritical{
			Identity: cosignIdentity{DockerReference: ref.Context().Name()},
			Image:    cosignImage{DockerManifestDigest: d.String()},
			Type:     "cosign container image signature",
		},
	}
	return json.Marshal(payload)
}

// parseManifestDigest validates and normalizes an artifact manifest digest
// string, defaulting a bare hex value to sha256.
func parseManifestDigest(digestStr string) (digest.Digest, error) {
	digestStr = strings.TrimSpace(digestStr)
	if digestStr == "" {
		return "", fmt.Errorf("digest is required for signing")
	}
	if !strings.Contains(digestStr, ":") {
		digestStr = "sha256:" + digestStr
	}
	d, err := digest.Parse(digestStr)
	if err != nil {
		return "", fmt.Errorf("parsing digest: %w", err)
	}
	return d, nil
}

// attachCosignSignature writes the cosign signature manifest for the
// artifact: an OCI image at the "sha256-<hex>.sig" tag whose single layer is
// the simple-signing payload, carrying the signature in the layer's
// annotations. This is the classic cosign layout, chosen deliberately for
// interop — "cosign verify --key" and any Sigstore-aware registry tooling
// can discover and verify it.
func attachCosignSignature(
	ctx context.Context,
	keychain authn.Keychain,
	imageRef, digestStr string,
	payload, signatureBytes []byte,
) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}
	d, err := parseManifestDigest(digestStr)
	if err != nil {
		return err
	}

	layer := static.NewLayer(payload, mediaTypeCosignSimpleSigningV1JSON)
	img, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer: layer,
		Annotations: map[string]string{
			annotationCosignSignature: base64.StdEncoding.EncodeToString(signatureBytes),
		},
		MediaType: mediaTypeCosignSimpleSigningV1JSON,
	})
	if err != nil {
		return fmt.Errorf("building signature manifest: %w", err)
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)

	h, err := v1.NewHash(d.String())
	if err != nil {
		return fmt.Errorf("parsing digest hash: %w", err)
	}
	sigTag := ref.Context().Tag(fmt.Sprint(h.Algorithm, "-", h.Hex, ".sig"))
	remoteOpts := []remote.Option{remote.WithAuthFromKeychain(keychain), remote.WithContext(ctx)}
	if err := remote.Write(sigTag, img, remoteOpts...); err != nil {
		return fmt.Errorf("pushing signature manifest: %w", err)
	}
	return nil
}
