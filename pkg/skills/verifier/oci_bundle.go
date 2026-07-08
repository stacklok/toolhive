// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
)

const (
	mediaTypeCosignSimpleSigningV1JSON = "application/vnd.dev.cosign.simplesigning.v1+json"
	sigstoreBundleMediaType01          = "application/vnd.dev.sigstore.bundle+json;version=0.1"
	maxAttestationsBytesLimit          = 10 * 1024 * 1024
)

type sigstoreBundle struct {
	bundle      *bundle.Bundle
	digestBytes []byte
	digestAlgo  string
	raw         []byte
}

func fetchOCIBundles(imageRef string, keychain authn.Keychain) ([]sigstoreBundle, error) {
	bundles, err := bundleFromSigstoreSignedImage(imageRef, keychain)
	if err != nil {
		return nil, err
	}
	if len(bundles) == 0 {
		return nil, ErrBundleNotFound
	}
	return bundles, nil
}

func bundleFromSigstoreSignedImage(imageRef string, keychain authn.Keychain) ([]sigstoreBundle, error) {
	signatureRef, err := getSignatureReferenceFromOCIImage(imageRef, keychain)
	if err != nil {
		return nil, fmt.Errorf("getting signature reference: %w", err)
	}

	layers, err := getSimpleSigningLayersFromSignatureManifest(signatureRef, keychain)
	if err != nil {
		return nil, err
	}

	var bundles []sigstoreBundle
	for _, layer := range layers {
		sb, buildErr := buildBundleFromLayer(layer)
		if buildErr != nil {
			slog.Debug("skipping invalid signing layer", "error", buildErr)
			continue
		}
		bundles = append(bundles, sb)
	}
	if len(bundles) == 0 {
		return nil, ErrBundleNotFound
	}
	return bundles, nil
}

func buildBundleFromLayer(layer v1.Descriptor) (sigstoreBundle, error) {
	verificationMaterial, err := getBundleVerificationMaterial(layer)
	if err != nil {
		return sigstoreBundle{}, err
	}
	msgSignature, err := getBundleMsgSignature(layer)
	if err != nil {
		return sigstoreBundle{}, err
	}
	pbb := protobundle.Bundle{
		MediaType:            sigstoreBundleMediaType01,
		VerificationMaterial: verificationMaterial,
		Content:              msgSignature,
	}
	bun, err := bundle.NewBundle(&pbb)
	if err != nil {
		return sigstoreBundle{}, err
	}
	raw, err := bun.MarshalJSON()
	if err != nil {
		return sigstoreBundle{}, err
	}
	digestBytes, err := hex.DecodeString(layer.Digest.Hex)
	if err != nil {
		return sigstoreBundle{}, err
	}
	return sigstoreBundle{
		bundle:      bun,
		digestBytes: digestBytes,
		digestAlgo:  layer.Digest.Algorithm,
		raw:         raw,
	}, nil
}

func getSignatureReferenceFromOCIImage(imageRef string, keychain authn.Keychain) (string, error) {
	opts := []remote.Option{remote.WithAuthFromKeychain(keychain)}
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", fmt.Errorf("parsing image reference: %w", err)
	}
	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return "", fmt.Errorf("getting image descriptor: %w", err)
	}
	digest := ref.Context().Digest(desc.Digest.String())
	h, err := v1.NewHash(digest.Identifier())
	if err != nil {
		return "", fmt.Errorf("building hash: %w", err)
	}
	sigTag := digest.Context().Tag(fmt.Sprint(h.Algorithm, "-", h.Hex, ".sig"))
	return sigTag.Name(), nil
}

func getSimpleSigningLayersFromSignatureManifest(manifestRef string, keychain authn.Keychain) ([]v1.Descriptor, error) {
	craneOpts := []crane.Option{crane.WithAuthFromKeychain(keychain)}
	mf, err := crane.Manifest(manifestRef, craneOpts...)
	if err != nil {
		return nil, fmt.Errorf("getting signature manifest: %w", err)
	}
	r := io.LimitReader(bytes.NewReader(mf), maxAttestationsBytesLimit)
	manifest, err := v1.ParseManifest(r)
	if err != nil {
		return nil, fmt.Errorf("parsing signature manifest: %w", err)
	}
	var results []v1.Descriptor
	for _, layer := range manifest.Layers {
		if layer.MediaType == mediaTypeCosignSimpleSigningV1JSON {
			results = append(results, layer)
		}
	}
	if len(results) == 0 {
		return nil, ErrBundleNotFound
	}
	return results, nil
}

func getBundleVerificationMaterial(manifestLayer v1.Descriptor) (*protobundle.VerificationMaterial, error) {
	signingCert, err := getVerificationMaterialX509CertificateChain(manifestLayer)
	if err != nil {
		return nil, err
	}
	tlogEntries, err := getVerificationMaterialTlogEntries(manifestLayer)
	if err != nil {
		return nil, err
	}
	return &protobundle.VerificationMaterial{
		Content:     signingCert,
		TlogEntries: tlogEntries,
	}, nil
}

func getVerificationMaterialX509CertificateChain(manifestLayer v1.Descriptor) (
	*protobundle.VerificationMaterial_X509CertificateChain, error) {
	pemCert := manifestLayer.Annotations["dev.sigstore.cosign/certificate"]
	block, _ := pem.Decode([]byte(pemCert))
	if block == nil {
		return nil, errors.New("failed to decode PEM certificate")
	}
	signingCert := protocommon.X509Certificate{RawBytes: block.Bytes}
	return &protobundle.VerificationMaterial_X509CertificateChain{
		X509CertificateChain: &protocommon.X509CertificateChain{
			Certificates: []*protocommon.X509Certificate{&signingCert},
		},
	}, nil
}

func getVerificationMaterialTlogEntries(manifestLayer v1.Descriptor) ([]*protorekor.TransparencyLogEntry, error) {
	bun := manifestLayer.Annotations["dev.sigstore.cosign/bundle"]
	var jsonData map[string]any
	if err := json.Unmarshal([]byte(bun), &jsonData); err != nil {
		return nil, fmt.Errorf("unmarshaling bundle annotation: %w", err)
	}
	payload, ok := jsonData["Payload"].(map[string]any)
	if !ok {
		return nil, errors.New("bundle payload missing")
	}
	logIndex, ok := payload["logIndex"].(float64)
	if !ok {
		return nil, errors.New("bundle logIndex missing")
	}
	li, ok := payload["logID"].(string)
	if !ok {
		return nil, errors.New("bundle logID missing")
	}
	logID, err := hex.DecodeString(li)
	if err != nil {
		return nil, fmt.Errorf("decoding logID: %w", err)
	}
	integratedTime, ok := payload["integratedTime"].(float64)
	if !ok {
		return nil, errors.New("bundle integratedTime missing")
	}
	set, ok := jsonData["SignedEntryTimestamp"].(string)
	if !ok {
		return nil, errors.New("bundle SignedEntryTimestamp missing")
	}
	signedEntryTimestamp, err := base64.StdEncoding.DecodeString(set)
	if err != nil {
		return nil, fmt.Errorf("decoding SignedEntryTimestamp: %w", err)
	}
	body, ok := payload["body"].(string)
	if !ok {
		return nil, errors.New("bundle body missing")
	}
	bodyBytes, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("decoding body: %w", err)
	}
	var bodyData map[string]any
	if err := json.Unmarshal(bodyBytes, &bodyData); err != nil {
		return nil, fmt.Errorf("unmarshaling body: %w", err)
	}
	apiVersion, _ := bodyData["apiVersion"].(string)
	kind, _ := bodyData["kind"].(string)
	return []*protorekor.TransparencyLogEntry{{
		LogIndex: int64(logIndex),
		LogId:    &protocommon.LogId{KeyId: logID},
		KindVersion: &protorekor.KindVersion{
			Kind: kind, Version: apiVersion,
		},
		IntegratedTime: int64(integratedTime),
		InclusionPromise: &protorekor.InclusionPromise{
			SignedEntryTimestamp: signedEntryTimestamp,
		},
		CanonicalizedBody: bodyBytes,
	}}, nil
}

func getBundleMsgSignature(simpleSigningLayer v1.Descriptor) (*protobundle.Bundle_MessageSignature, error) {
	var msgHashAlg protocommon.HashAlgorithm
	switch simpleSigningLayer.Digest.Algorithm {
	case "sha256":
		msgHashAlg = protocommon.HashAlgorithm_SHA2_256
	default:
		return nil, fmt.Errorf("unsupported digest algorithm: %s", simpleSigningLayer.Digest.Algorithm)
	}
	digest, err := hex.DecodeString(simpleSigningLayer.Digest.Hex)
	if err != nil {
		return nil, err
	}
	s := simpleSigningLayer.Annotations["dev.cosignproject.cosign/signature"]
	sig, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return &protobundle.Bundle_MessageSignature{
		MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{Algorithm: msgHashAlg, Digest: digest},
			Signature:     sig,
		},
	}, nil
}

func digestBytesFromString(digest string) ([]byte, string, error) {
	digest = strings.TrimSpace(digest)
	if !strings.Contains(digest, ":") {
		return nil, "", fmt.Errorf("invalid digest %q", digest)
	}
	parts := strings.SplitN(digest, ":", 2)
	algo, hexPart := parts[0], parts[1]
	raw, err := hex.DecodeString(hexPart)
	if err != nil {
		return nil, "", err
	}
	return raw, algo, nil
}
