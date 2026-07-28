// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/signer"
)

// startTestRegistry runs an in-process OCI registry and returns its host.
func startTestRegistry(t *testing.T) string {
	t.Helper()
	reg := httptest.NewServer(registry.New())
	t.Cleanup(reg.Close)
	return strings.TrimPrefix(reg.URL, "http://")
}

// pushTestArtifact pushes a random OCI image and returns its ref and digest.
func pushTestArtifact(t *testing.T, host string) (ref string, digest string) {
	t.Helper()
	img, err := random.Image(256, 1)
	require.NoError(t, err)
	ref = host + "/test/skill:v1"
	parsed, err := name.ParseReference(ref)
	require.NoError(t, err)
	require.NoError(t, remote.Write(parsed, img))
	d, err := img.Digest()
	require.NoError(t, err)
	return ref, d.String()
}

// signArtifact signs the artifact with a fresh cosign key via the signer
// package and returns the public key PEM and the bundle it produced —
// exactly the flow `thv skill push --key` performs.
func signArtifact(t *testing.T, ref, digest string) (pubPEM, bundle []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	privPEM, err := cryptoutils.MarshalPrivateKeyToPEM(priv)
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "cosign.key")
	require.NoError(t, os.WriteFile(keyPath, privPEM, 0o600))
	pubPEM, err = cryptoutils.MarshalPublicKeyToPEM(priv.Public())
	require.NoError(t, err)

	raw, err := signer.NewDefault(nil).SignOCI(t.Context(), ref, digest, signer.Options{Key: keyPath})
	require.NoError(t, err)
	return pubPEM, raw
}

func TestVerifyOCIWithKeyRoundTrip(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	ref, digest := pushTestArtifact(t, host)
	pubPEM, _ := signArtifact(t, ref, digest)

	result, err := NewDefault(nil).VerifyOCIWithKey(t.Context(), ref, digest, pubPEM)
	require.NoError(t, err, "the artifact signed by the signer package must verify with its key")
	assert.True(t, result.Signed)
	assert.NotEmpty(t, result.Bundle, "the bundle must be returned for durable storage")
	assert.Empty(t, result.SignerIdentity, "key-signed artifacts carry no certificate identity")
	assert.Nil(t, result.ToLockProvenance(), "key-signed results must not fabricate lock provenance")
}

func TestVerifyOCIWithKeyRejectsWrongKey(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	ref, digest := pushTestArtifact(t, host)
	signArtifact(t, ref, digest)

	otherPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	otherPub, err := cryptoutils.MarshalPublicKeyToPEM(otherPriv.Public())
	require.NoError(t, err)

	_, err = NewDefault(nil).VerifyOCIWithKey(t.Context(), ref, digest, otherPub)
	require.ErrorIs(t, err, ErrSignatureInvalid)
}

func TestVerifyOCIUnsignedArtifact(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	ref, digest := pushTestArtifact(t, host)

	_, err := NewDefault(nil).VerifyOCI(t.Context(), ref, digest, nil)
	require.ErrorIs(t, err, ErrUnsigned)

	_, err = NewDefault(nil).VerifyOCIWithKey(t.Context(), ref, digest, nil)
	require.ErrorIs(t, err, ErrUnsigned)
}

func TestVerifyOCIKeylessRejectsKeySignedArtifact(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	ref, digest := pushTestArtifact(t, host)
	signArtifact(t, ref, digest)

	// The keyless flow requires a certificate chain to Fulcio; a key-signed
	// bundle has none, so this must fail as an invalid signature — not as
	// unsigned, and never as a panic.
	_, err := NewDefault(nil).VerifyOCI(t.Context(), ref, digest, nil)
	require.ErrorIs(t, err, ErrSignatureInvalid)
}

func TestVerifyBundleOffline(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	ref, digest := pushTestArtifact(t, host)
	_, bundle := signArtifact(t, ref, digest)
	d := NewDefault(nil)

	t.Run("empty bundle rejected", func(t *testing.T) {
		t.Parallel()
		err := d.VerifyBundleOffline(nil, digest, nil)
		require.ErrorIs(t, err, ErrSignatureInvalid)
	})

	t.Run("malformed bundle rejected", func(t *testing.T) {
		t.Parallel()
		err := d.VerifyBundleOffline([]byte("not a bundle"), digest, nil)
		require.ErrorIs(t, err, ErrSignatureInvalid)
	})

	t.Run("key-signed bundle fails keyless offline verification", func(t *testing.T) {
		t.Parallel()
		// The stored bundle parses and reaches verification, but carries no
		// certificate — the offline keyless path must reject it rather than
		// trusting it.
		err := d.VerifyBundleOffline(bundle, digest, nil)
		require.ErrorIs(t, err, ErrSignatureInvalid)
	})

	t.Run("expected identity against unverifiable bundle stays invalid", func(t *testing.T) {
		t.Parallel()
		// Both the pinned and the TOFU re-verify fail, so this must NOT be
		// misclassified as a signer mismatch.
		err := d.VerifyBundleOffline(bundle, digest, &lockfile.Provenance{
			SignerIdentity: "/.github/workflows/release.yml",
			CertIssuer:     "https://token.actions.githubusercontent.com",
		})
		require.ErrorIs(t, err, ErrSignatureInvalid)
		require.NotErrorIs(t, err, ErrSignerMismatch)
	})
}

func TestResultFromBundleMalformed(t *testing.T) {
	t.Parallel()
	d := NewDefault(nil)

	_, err := d.ResultFromBundle(nil, "sha256:abc")
	require.ErrorIs(t, err, ErrSignatureInvalid)

	_, err = d.ResultFromBundle([]byte("junk"), "sha256:abc")
	require.ErrorIs(t, err, ErrSignatureInvalid)
}

func TestToLockProvenance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result *Result
		want   *lockfile.Provenance
	}{
		{name: "nil result", result: nil, want: nil},
		{name: "unsigned result", result: &Result{Signed: false}, want: nil},
		{
			name:   "key-signed result has no identity to record",
			result: &Result{Signed: true, SigstoreURL: sigstorePublicGoodRekorURL},
			want:   nil,
		},
		{
			name: "identity-bearing result maps all fields",
			result: &Result{
				Signed:         true,
				SignerIdentity: "/.github/workflows/release.yml",
				CertIssuer:     "https://token.actions.githubusercontent.com",
				RepositoryURI:  "https://github.com/org/repo",
				SigstoreURL:    sigstorePublicGoodRekorURL,
			},
			want: &lockfile.Provenance{
				SignerIdentity: "/.github/workflows/release.yml",
				CertIssuer:     "https://token.actions.githubusercontent.com",
				RepositoryURI:  "https://github.com/org/repo",
				SigstoreURL:    sigstorePublicGoodRekorURL,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.result.ToLockProvenance())
		})
	}
}

func TestExpectedIdentityConversion(t *testing.T) {
	t.Parallel()

	assert.Nil(t, expectedIdentity(nil), "TOFU first use must yield a nil core identity")

	got := expectedIdentity(&lockfile.Provenance{
		SignerIdentity: "/.github/workflows/release.yml",
		CertIssuer:     "https://token.actions.githubusercontent.com",
		RepositoryURI:  "https://github.com/org/repo",
	})
	require.NotNil(t, got)
	assert.Equal(t, "/.github/workflows/release.yml", got.SignerIdentity)
	assert.Equal(t, "https://token.actions.githubusercontent.com", got.CertIssuer)
	assert.Equal(t, "https://github.com/org/repo", got.SourceRepositoryURI)
}
