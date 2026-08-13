// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package signer

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/opencontainers/go-digest"
	"github.com/sigstore/sigstore/pkg/signature"
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
	pub crypto.PublicKey,
) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}
	d, err := parseManifestDigest(digestStr)
	if err != nil {
		return err
	}

	h, err := v1.NewHash(d.String())
	if err != nil {
		return fmt.Errorf("parsing digest hash: %w", err)
	}
	sigTag := ref.Context().Tag(fmt.Sprint(h.Algorithm, "-", h.Hex, ".sig"))
	remoteOpts := []remote.Option{remote.WithAuthFromKeychain(keychain), remote.WithContext(ctx)}

	// An artifact can carry signatures from several signers, so the new
	// layer is appended to whatever is already at the .sig tag rather than
	// replacing it. Building from empty.Image unconditionally would delete
	// every existing signature — including other people's trust material —
	// on the next push. This mirrors cosign's own append behaviour.
	base, err := existingSignatureImage(sigTag, remoteOpts)
	if err != nil {
		return err
	}

	already, err := signedByKey(base, payload, pub)
	if err != nil {
		return err
	}
	if already {
		// Re-signing with the same key is a no-op; pushing repeatedly must
		// not grow the manifest without bound. Comparing signature bytes
		// would not work — ECDSA is randomised, so the same key produces a
		// different signature every time — so this asks the question that
		// actually matters: is one of the existing signatures already ours?
		return nil
	}
	encodedSig := base64.StdEncoding.EncodeToString(signatureBytes)

	layer := static.NewLayer(payload, mediaTypeCosignSimpleSigningV1JSON)
	img, err := mutate.Append(base, mutate.Addendum{
		Layer: layer,
		Annotations: map[string]string{
			annotationCosignSignature: encodedSig,
		},
		MediaType: mediaTypeCosignSimpleSigningV1JSON,
	})
	if err != nil {
		return fmt.Errorf("building signature manifest: %w", err)
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)

	if err := remote.Write(sigTag, img, remoteOpts...); err != nil {
		return fmt.Errorf("pushing signature manifest: %w", err)
	}
	return nil
}

// existingSignatureImage fetches the signature manifest already at tag, or
// an empty image when none exists yet. Only a genuine "absent" answer from
// the registry is treated as empty — any other failure is returned, because
// silently starting from empty would discard existing signatures.
func existingSignatureImage(tag name.Tag, remoteOpts []remote.Option) (v1.Image, error) {
	img, err := remote.Image(tag, remoteOpts...)
	if err == nil {
		return img, nil
	}
	if isAbsentFromRegistry(err) {
		return empty.Image, nil
	}
	return nil, fmt.Errorf("reading existing signature manifest: %w", err)
}

// isAbsentFromRegistry reports whether err means "this tag does not exist"
// as opposed to a transport, auth, or server failure.
func isAbsentFromRegistry(err error) bool {
	var terr *transport.Error
	if !errors.As(err, &terr) {
		return false
	}
	if terr.StatusCode == http.StatusNotFound {
		return true
	}
	for _, diag := range terr.Errors {
		if diag.Code == transport.ManifestUnknownErrorCode || diag.Code == transport.NameUnknownErrorCode {
			return true
		}
	}
	return false
}

// signedByKey reports whether img already carries a signature over payload
// that verifies with pub — i.e. whether this key has already signed this
// artifact. This is the same question cosign's dupe detector asks, and the
// only reliable one: ECDSA signatures are randomised, so two signatures
// from one key never match byte-for-byte.
func signedByKey(img v1.Image, payload []byte, pub crypto.PublicKey) (bool, error) {
	manifest, err := img.Manifest()
	if err != nil {
		return false, fmt.Errorf("reading signature manifest layers: %w", err)
	}
	if len(manifest.Layers) == 0 {
		return false, nil
	}
	sigVerifier, err := signature.LoadVerifier(pub, crypto.SHA256)
	if err != nil {
		return false, fmt.Errorf("loading verifier for duplicate detection: %w", err)
	}
	for _, l := range manifest.Layers {
		encoded := l.Annotations[annotationCosignSignature]
		if encoded == "" {
			continue
		}
		raw, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			// A layer we cannot decode is not one of ours; leave it alone
			// rather than failing the whole push over someone else's
			// malformed annotation.
			continue
		}
		if sigVerifier.VerifySignature(bytes.NewReader(raw), bytes.NewReader(payload)) == nil {
			return true, nil
		}
	}
	return false, nil
}
