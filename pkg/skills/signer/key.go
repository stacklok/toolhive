// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package signer

import (
	"context"
	"crypto"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	"github.com/sigstore/sigstore-go/pkg/sign"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
)

// resolveKeyPath canonicalizes and vets a user-supplied signing key path:
// absolute, cleaned, symlinks resolved, and a regular file. The value comes
// straight from a --key flag, so this is the recognized sanitization
// barrier before the path is opened — no string-inspection shortcuts.
func resolveKeyPath(path string) (string, error) {
	if path == "" {
		return "", ErrKeyRequired
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving signing key path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", fmt.Errorf("resolving signing key path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("reading signing key: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("signing key %q is not a regular file", resolved)
	}
	return resolved, nil
}

// loadKeypair reads a cosign PEM private key from path. Encrypted keys are
// decrypted with the COSIGN_PASSWORD environment variable, matching the
// cosign CLI's behavior.
func loadKeypair(path string) (sign.Keypair, error) {
	resolved, err := resolveKeyPath(path)
	if err != nil {
		return nil, err
	}
	pemBytes, err := os.ReadFile(resolved) //nolint:gosec // resolved by resolveKeyPath from an explicit --key flag
	if err != nil {
		return nil, fmt.Errorf("reading signing key: %w", err)
	}
	priv, err := cryptoutils.UnmarshalPEMToPrivateKey(pemBytes, cosignPassFunc())
	if err != nil {
		return nil, fmt.Errorf("decoding signing key: %w", err)
	}
	signerKey, ok := priv.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("signing key type %T cannot sign", priv)
	}
	return &fileKeypair{priv: signerKey}, nil
}

func cosignPassFunc() cryptoutils.PassFunc {
	if pw := os.Getenv("COSIGN_PASSWORD"); pw != "" {
		return cryptoutils.StaticPasswordFunc([]byte(pw))
	}
	return func(_ bool) ([]byte, error) { return nil, nil }
}

// fileKeypair adapts a file-loaded private key to sigstore-go's signing
// Keypair interface (ECDSA P-256 / SHA-256, the cosign default).
type fileKeypair struct {
	priv crypto.Signer
}

func (*fileKeypair) GetHashAlgorithm() protocommon.HashAlgorithm {
	return protocommon.HashAlgorithm_SHA2_256
}

func (*fileKeypair) GetSigningAlgorithm() protocommon.PublicKeyDetails {
	return protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256
}

func (k *fileKeypair) GetHint() []byte {
	pubKeyBytes, err := cryptoutils.MarshalPublicKeyToPEM(k.priv.Public())
	if err != nil {
		return nil
	}
	return pubKeyBytes
}

func (*fileKeypair) GetKeyAlgorithm() string {
	return "ecdsa"
}

func (k *fileKeypair) GetPublicKey() crypto.PublicKey {
	return k.priv.Public()
}

func (k *fileKeypair) GetPublicKeyPem() (string, error) {
	pemBytes, err := cryptoutils.MarshalPublicKeyToPEM(k.priv.Public())
	if err != nil {
		return "", err
	}
	return string(pemBytes), nil
}

// SignData hashes data with SHA-256 and signs the digest, returning
// (signature, digest) — the order sigstore-go's Keypair contract requires
// (see sign.EphemeralKeypair for the reference implementation).
func (k *fileKeypair) SignData(_ context.Context, data []byte) ([]byte, []byte, error) {
	hash := crypto.SHA256.New()
	if _, err := hash.Write(data); err != nil {
		return nil, nil, err
	}
	digest := hash.Sum(nil)
	sig, err := k.priv.Sign(rand.Reader, digest, crypto.SHA256)
	if err != nil {
		return nil, nil, err
	}
	return sig, digest, nil
}
