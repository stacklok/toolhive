// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package signer

import (
	"context"
	"encoding/base64"
	"encoding/hex"
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

const mediaTypeCosignSimpleSigningV1JSON = "application/vnd.dev.cosign.simplesigning.v1+json"

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

func attachCosignSignature(
	ctx context.Context,
	keychain authn.Keychain,
	imageRef, digestStr string,
	signature []byte,
) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}
	if digestStr == "" {
		return fmt.Errorf("digest is required for signing")
	}
	if !strings.Contains(digestStr, ":") {
		digestStr = "sha256:" + digestStr
	}
	d, err := digest.Parse(digestStr)
	if err != nil {
		return fmt.Errorf("parsing digest: %w", err)
	}

	payload := cosignSimpleSigning{
		Critical: cosignCritical{
			Identity: cosignIdentity{DockerReference: ref.Context().Name()},
			Image:    cosignImage{DockerManifestDigest: d.String()},
			Type:     "cosign container image signature",
		},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	layer := static.NewLayer(payloadBytes, mediaTypeCosignSimpleSigningV1JSON)
	img := empty.Image
	img, err = mutate.Append(img, mutate.Addendum{
		Layer: layer,
		Annotations: map[string]string{
			"dev.cosignproject.cosign/signature": base64.StdEncoding.EncodeToString(signature),
		},
		MediaType: mediaTypeCosignSimpleSigningV1JSON,
	})
	if err != nil {
		return err
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)

	h, err := v1.NewHash(d.String())
	if err != nil {
		return err
	}
	sigTag := ref.Context().Digest(d.String()).Context().Tag(fmt.Sprint(h.Algorithm, "-", h.Hex, ".sig"))
	remoteOpts := []remote.Option{remote.WithAuthFromKeychain(keychain), remote.WithContext(ctx)}
	return remote.Write(sigTag, img, remoteOpts...)
}

func digestBytesFromString(digestStr string) ([]byte, error) {
	digestStr = strings.TrimSpace(digestStr)
	if !strings.Contains(digestStr, ":") {
		return nil, fmt.Errorf("invalid digest %q", digestStr)
	}
	parts := strings.SplitN(digestStr, ":", 2)
	raw, err := hex.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func signDigestWithKeypair(ctx context.Context, keypair interface {
	SignData(context.Context, []byte) ([]byte, []byte, error)
}, digestBytes []byte) ([]byte, error) {
	_, sig, err := keypair.SignData(ctx, digestBytes)
	return sig, err
}
