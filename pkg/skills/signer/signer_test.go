// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package signer

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestKey generates an ECDSA P-256 key pair, writes the private key as
// a cosign-style PEM file, and returns its path plus the public key PEM.
func writeTestKey(t *testing.T) (keyPath string, pubPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	privPEM, err := cryptoutils.MarshalPrivateKeyToPEM(priv)
	require.NoError(t, err)
	keyPath = filepath.Join(t.TempDir(), "cosign.key")
	require.NoError(t, os.WriteFile(keyPath, privPEM, 0o600))

	pubPEM, err = cryptoutils.MarshalPublicKeyToPEM(priv.Public())
	require.NoError(t, err)
	return keyPath, pubPEM
}

// pushTestArtifact pushes a random OCI image to the in-process registry and
// returns its reference and manifest digest.
func pushTestArtifact(t *testing.T, registryHost string) (ref string, digestStr string) {
	t.Helper()
	img, err := random.Image(256, 1)
	require.NoError(t, err)
	ref = registryHost + "/test/skill:v1"
	parsed, err := name.ParseReference(ref)
	require.NoError(t, err)
	require.NoError(t, remote.Write(parsed, img))
	d, err := img.Digest()
	require.NoError(t, err)
	return ref, d.String()
}

// verifyKeyBundle verifies a serialized bundle against the public key and
// the given payload digest — the same semantics toolhive-core's
// VerifyBundleWithKey exposes (used directly here until the core release
// this stack's verifier PR consumes).
func verifyKeyBundle(t *testing.T, raw, pubPEM []byte, payloadDigest []byte) error {
	t.Helper()
	pub, err := cryptoutils.UnmarshalPEMToPublicKey(pubPEM)
	require.NoError(t, err)
	sigVerifier, err := signature.LoadVerifier(pub, crypto.SHA256)
	require.NoError(t, err)
	tm := root.NewTrustedPublicKeyMaterial(func(string) (root.TimeConstrainedVerifier, error) {
		return root.NewExpiringKey(sigVerifier, time.Time{}, time.Time{}), nil
	})
	sev, err := verify.NewVerifier(tm, verify.WithNoObserverTimestamps())
	require.NoError(t, err)

	parsed := &bundle.Bundle{}
	require.NoError(t, parsed.UnmarshalJSON(raw))
	_, err = sev.Verify(parsed, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", payloadDigest),
		verify.WithKey(),
	))
	return err
}

//nolint:paralleltest // uses t.Setenv indirectly via subtests sharing a registry
func TestSignOCIRoundTrip(t *testing.T) {
	reg := httptest.NewServer(registry.New())
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "http://")

	keyPath, pubPEM := writeTestKey(t)
	ref, digestStr := pushTestArtifact(t, host)

	raw, err := NewDefault(nil).SignOCI(t.Context(), ref, digestStr, Options{Key: keyPath})
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	// The returned bundle verifies against the signing key over the
	// simple-signing payload digest.
	payload, err := simpleSigningPayload(ref, digestStr)
	require.NoError(t, err)
	payloadDigest := sha256.Sum256(payload)
	require.NoError(t, verifyKeyBundle(t, raw, pubPEM, payloadDigest[:]),
		"the returned bundle must verify against the signing key")

	// The attached signature manifest reconstructs to the SAME signature:
	// this is the layout toolhive-core's bundle retrieval (and cosign)
	// discover — one signature, two representations.
	h, err := v1.NewHash(digestStr)
	require.NoError(t, err)
	sigRef := host + "/test/skill:" + h.Algorithm + "-" + h.Hex + ".sig"
	manifestBytes, err := crane.Manifest(sigRef)
	require.NoError(t, err, "the signature manifest must exist at the cosign .sig tag")

	var manifest struct {
		Layers []struct {
			MediaType   string            `json:"mediaType"`
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"layers"`
	}
	require.NoError(t, json.Unmarshal(manifestBytes, &manifest))
	require.Len(t, manifest.Layers, 1)
	layer := manifest.Layers[0]
	assert.Equal(t, mediaTypeCosignSimpleSigningV1JSON, layer.MediaType)

	// The layer content digest is the payload digest the bundle signed.
	wantLayerDigest := "sha256:" + hex.EncodeToString(payloadDigest[:])
	assert.Equal(t, wantLayerDigest, layer.Digest,
		"the signature manifest layer must be the exact signed payload")

	// The annotation signature matches the bundle's message signature.
	parsed := &bundle.Bundle{}
	require.NoError(t, parsed.UnmarshalJSON(raw))
	bundleSig := parsed.Bundle.GetMessageSignature().GetSignature()
	annotationSig, err := base64.StdEncoding.DecodeString(layer.Annotations[annotationCosignSignature])
	require.NoError(t, err)
	assert.Equal(t, bundleSig, annotationSig,
		"the attached annotation and the returned bundle must carry the same signature")
}

//nolint:paralleltest // shares process env semantics with the round-trip test
func TestSignOCIRejectsWrongKeyVerification(t *testing.T) {
	reg := httptest.NewServer(registry.New())
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "http://")

	keyPath, _ := writeTestKey(t)
	_, otherPub := writeTestKey(t)
	ref, digestStr := pushTestArtifact(t, host)

	raw, err := NewDefault(nil).SignOCI(t.Context(), ref, digestStr, Options{Key: keyPath})
	require.NoError(t, err)

	payload, err := simpleSigningPayload(ref, digestStr)
	require.NoError(t, err)
	payloadDigest := sha256.Sum256(payload)
	require.Error(t, verifyKeyBundle(t, raw, otherPub, payloadDigest[:]),
		"a different key must not verify the bundle")
}

func TestSignOCIRequiresKey(t *testing.T) {
	t.Parallel()
	_, err := NewDefault(nil).SignOCI(t.Context(), "ghcr.io/org/skill:v1", "sha256:abc", Options{})
	require.ErrorIs(t, err, ErrKeyRequired)
}

func TestSignOCIEncryptedKey(t *testing.T) {
	reg := httptest.NewServer(registry.New())
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "http://")

	// Generate an encrypted cosign-style key: the "ENCRYPTED SIGSTORE
	// PRIVATE KEY" PEM type wrapping password-encrypted DER, which is what
	// `cosign generate-key-pair` produces.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	encDER, err := cryptoutils.MarshalPrivateKeyToEncryptedDER(
		priv, cryptoutils.StaticPasswordFunc([]byte("test-password")))
	require.NoError(t, err)
	encPEM := pem.EncodeToMemory(&pem.Block{
		Type:  string(cryptoutils.EncryptedSigstorePrivateKeyPEMType),
		Bytes: encDER,
	})
	keyPath := filepath.Join(t.TempDir(), "cosign-enc.key")
	require.NoError(t, os.WriteFile(keyPath, encPEM, 0o600))

	ref, digestStr := pushTestArtifact(t, host)

	t.Setenv("COSIGN_PASSWORD", "test-password")
	raw, err := NewDefault(nil).SignOCI(t.Context(), ref, digestStr, Options{Key: keyPath})
	require.NoError(t, err, "an encrypted key must decrypt via COSIGN_PASSWORD")
	require.NotEmpty(t, raw)

	t.Setenv("COSIGN_PASSWORD", "wrong-password")
	_, err = NewDefault(nil).SignOCI(t.Context(), ref, digestStr, Options{Key: keyPath})
	require.Error(t, err, "a wrong password must fail key decryption")
}

func TestResolveKeyPath(t *testing.T) {
	t.Parallel()

	t.Run("regular file resolves", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "key.pem")
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		resolved, err := resolveKeyPath(p)
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(resolved))
	})

	t.Run("directory rejected", func(t *testing.T) {
		t.Parallel()
		_, err := resolveKeyPath(t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
	})

	t.Run("missing file rejected", func(t *testing.T) {
		t.Parallel()
		_, err := resolveKeyPath(filepath.Join(t.TempDir(), "nope.pem"))
		require.Error(t, err)
	})

	t.Run("empty path rejected", func(t *testing.T) {
		t.Parallel()
		_, err := resolveKeyPath("")
		require.ErrorIs(t, err, ErrKeyRequired)
	})
}
