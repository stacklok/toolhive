// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package signer

import (
	"context"
	"crypto"
	"fmt"
	"os"

	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	"github.com/sigstore/sigstore-go/pkg/sign"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
)

type fileKeypair struct {
	priv crypto.PrivateKey
}

func loadKeypair(path string) (sign.Keypair, error) {
	pemBytes, err := os.ReadFile(path) //nolint:gosec // path is an explicit --key flag from the user
	if err != nil {
		return nil, fmt.Errorf("reading signing key: %w", err)
	}
	priv, err := cryptoutils.UnmarshalPEMToPrivateKey(pemBytes, cosignPassFunc())
	if err != nil {
		return nil, fmt.Errorf("decoding signing key: %w", err)
	}
	return &fileKeypair{priv: priv}, nil
}

func cosignPassFunc() cryptoutils.PassFunc {
	if pw := os.Getenv("COSIGN_PASSWORD"); pw != "" {
		return cryptoutils.StaticPasswordFunc([]byte(pw))
	}
	return func(_ bool) ([]byte, error) { return nil, nil }
}

func (*fileKeypair) GetHashAlgorithm() protocommon.HashAlgorithm {
	return protocommon.HashAlgorithm_SHA2_256
}

func (*fileKeypair) GetSigningAlgorithm() protocommon.PublicKeyDetails {
	return protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256
}

func (k *fileKeypair) GetHint() []byte {
	pubKeyBytes, err := cryptoutils.MarshalPublicKeyToPEM(k.priv.(crypto.Signer).Public())
	if err != nil {
		return nil
	}
	return pubKeyBytes
}

func (*fileKeypair) GetKeyAlgorithm() string {
	return "ecdsa"
}

func (k *fileKeypair) GetPublicKey() crypto.PublicKey {
	return k.priv.(crypto.Signer).Public()
}

func (k *fileKeypair) GetPublicKeyPem() (string, error) {
	pemBytes, err := cryptoutils.MarshalPublicKeyToPEM(k.GetPublicKey())
	if err != nil {
		return "", err
	}
	return string(pemBytes), nil
}

func (k *fileKeypair) SignData(ctx context.Context, data []byte) ([]byte, []byte, error) {
	_ = ctx
	signer, ok := k.priv.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("private key does not implement crypto.Signer")
	}
	return signDigest(signer, data)
}

func signDigest(signer crypto.Signer, data []byte) ([]byte, []byte, error) {
	hash := crypto.SHA256.New()
	if _, err := hash.Write(data); err != nil {
		return nil, nil, err
	}
	digest := hash.Sum(nil)
	sig, err := signer.Sign(nil, digest, crypto.SHA256)
	if err != nil {
		return nil, nil, err
	}
	return digest, sig, nil
}
