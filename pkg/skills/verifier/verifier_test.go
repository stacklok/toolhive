// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	sgbundle "github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/container/signer"
	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// startTestRegistry runs an in-process OCI registry and returns its host.
// testPublicKeyB64 is a real P-256 public key in the base64 DER SPKI
// form a key-pinned lock entry stores. It must genuinely parse: validation
// rejects a value that merely decodes as base64.
const testPublicKeyB64 = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAExlVDpbnOEv2fH3gS8n7UCHS9Gs0wKxIPR5EAcl8F1jSxlxAV/pll0NsSiuAK95Ws4Fpkn+5QkdVKNXy7LHgb2A=="

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

	res, err := signer.NewDefault(nil).SignOCI(t.Context(), ref, digest, signer.Options{Key: keyPath})
	require.NoError(t, err)
	return pubPEM, res.Bundle
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
	assert.Empty(t, result.SigstoreURL,
		"the key flow writes no transparency-log entry; recording one would fabricate provenance")
	assert.Nil(t, result.ToLockProvenance(), "key-signed results must not fabricate lock provenance")

	// The stored bundle re-verifies offline with the key, and rejects a
	// different key.
	require.NoError(t, NewDefault(nil).VerifyBundleOfflineWithKey(result.Bundle, ref, digest, pubPEM))
	otherPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	otherPub, err := cryptoutils.MarshalPublicKeyToPEM(otherPriv.Public())
	require.NoError(t, err)
	require.ErrorIs(t, NewDefault(nil).VerifyBundleOfflineWithKey(result.Bundle, ref, digest, otherPub),
		ErrSignatureInvalid)
	require.ErrorIs(t, NewDefault(nil).VerifyBundleOfflineWithKey(
		result.Bundle, ref, "sha256:"+strings.Repeat("e", 64), pubPEM), ErrSignatureInvalid,
		"a different artifact digest reconstructs a different payload and must not verify")
}

func TestVerifyOCIDigestGuards(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	ref, digest := pushTestArtifact(t, host)
	d := NewDefault(nil)

	_, err := d.VerifyOCI(t.Context(), ref, "", nil)
	require.ErrorContains(t, err, "digest is required",
		"an empty digest would leave tag resolution to fetch time")

	otherDigest := "sha256:" + strings.Repeat("f", 64)
	_, err = d.VerifyOCI(t.Context(), ref+"@"+digest, otherDigest, nil)
	require.ErrorContains(t, err, "refusing to verify ambiguous input",
		"a ref-embedded digest disagreeing with the parameter is lock corruption")

	// Agreement between the embedded digest and the parameter is fine.
	_, err = d.VerifyOCI(t.Context(), ref+"@"+digest, digest, nil)
	require.ErrorIs(t, err, ErrUnsigned, "consistent inputs proceed to retrieval")
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
	require.NotErrorIs(t, err, ErrKeylessSigned,
		"the artifact IS key-signed; the key is simply the wrong one, and telling the caller to"+
			" drop the key would send them to a path that cannot verify it either")
}

// pushTaggedTestArtifact pushes a random OCI image to the shared test
// repository under tag and returns its digest. Same repository as
// pushTestArtifact deliberately: a transplant within one repository is the
// case a repository-only binding would miss.
func pushTaggedTestArtifact(t *testing.T, host, tag string) string {
	t.Helper()
	img, err := random.Image(256, 1)
	require.NoError(t, err)
	parsed, err := name.ParseReference(host + "/test/skill:" + tag)
	require.NoError(t, err)
	require.NoError(t, remote.Write(parsed, img))
	d, err := img.Digest()
	require.NoError(t, err)
	return d.String()
}

// transplantSignature republishes the signature manifest attached to
// fromDigest under the tag the verifier derives from toDigest, forging the
// only link discovery relies on. It needs no key and no write access to the
// signature itself — just the ability to push a tag to the repository.
func transplantSignature(t *testing.T, host, fromDigest, toDigest string) {
	t.Helper()
	sigTag := func(d string) name.Reference {
		r, err := name.ParseReference(host + "/test/skill:" + strings.Replace(d, ":", "-", 1) + ".sig")
		require.NoError(t, err)
		return r
	}
	attached, err := remote.Image(sigTag(fromDigest))
	require.NoError(t, err, "the signed artifact must have a discoverable signature manifest to copy")
	require.NoError(t, remote.Write(sigTag(toDigest), attached))
}

// TestVerifyOCIWithKeyRejectsTransplantedSignature covers the replay a
// digest-unbound key check would accept: the signature is genuine, the key is
// genuinely trusted, and the only false claim is which artifact the signature
// is about.
//
// Signatures are discovered by tag, derived from the digest being verified, so
// a registry-side attacker who can push a tag can make one artifact's
// signature appear to be another's. The signature still verifies — it covers
// its own payload intact — so nothing about the cryptography is disturbed.
// What must reject it is the payload naming the artifact it actually signed.
func TestVerifyOCIWithKeyRejectsTransplantedSignature(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	signedRef, signedDigest := pushTestArtifact(t, host)
	pubPEM, _ := signArtifact(t, signedRef, signedDigest)

	// A second, unsigned artifact — the one the attacker wants accepted.
	targetDigest := pushTaggedTestArtifact(t, host, "v2")
	require.NotEqual(t, signedDigest, targetDigest)
	repoRef := host + "/test/skill"

	// Before the transplant the target is simply unsigned.
	_, err := NewDefault(nil).VerifyOCIWithKey(t.Context(), repoRef, targetDigest, pubPEM)
	require.ErrorIs(t, err, ErrUnsigned)

	transplantSignature(t, host, signedDigest, targetDigest)

	// The transplanted signature is now discoverable as the target's, and
	// verifies against the trusted key on its own terms.
	_, err = NewDefault(nil).VerifyOCIWithKey(t.Context(), repoRef, targetDigest, pubPEM)
	require.ErrorIs(t, err, ErrSignatureInvalid,
		"a signature naming a different artifact must not verify this one")
	require.ErrorContains(t, err, "none of it signs this artifact")
	require.NotErrorIs(t, err, ErrUnsigned,
		"signature material IS present; calling it unsigned would invite an --allow-unsigned override")

	// The signature still verifies for the artifact it was actually made for,
	// so the check rejects the false binding rather than the signature.
	_, err = NewDefault(nil).VerifyOCIWithKey(t.Context(), repoRef, signedDigest, pubPEM)
	require.NoError(t, err)
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

	// The keyless flow requires a certificate chain to Fulcio and a
	// key-signed bundle has none, so this must fail — but as the key-pair
	// layout it is, never as unsigned, and never as a panic.
	//
	// This assertion was ErrSignatureInvalid until #6442: that was the only
	// classification available, not a claim the signature was bad. It is a
	// real signature referrer, so retrieveBundles finds it and ErrUnsigned
	// never fires; blaming the signature then sent users hunting a
	// corruption that was not there, and --allow-unsigned could not override
	// it because that exception only applies to ErrUnsigned.
	_, err := NewDefault(nil).VerifyOCI(t.Context(), ref, digest, nil)
	require.ErrorIs(t, err, ErrKeySigned)
	require.NotErrorIs(t, err, ErrSignatureInvalid,
		"the signature is intact; the missing piece is a trust anchor to check it against")
	require.NotErrorIs(t, err, ErrUnsigned,
		"the artifact is signed, so --allow-unsigned must not be able to record an unsigned exception")
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
			name: "provisional result marks the lock provenance",
			result: &Result{
				Signed:         true,
				SignerIdentity: "dev@example.com",
				CertIssuer:     "https://accounts.example.com",
				Provisional:    true,
			},
			want: &lockfile.Provenance{
				SignerIdentity: "dev@example.com",
				CertIssuer:     "https://accounts.example.com",
				Provisional:    true,
			},
		},
		{
			name: "identity-bearing result maps all fields",
			result: &Result{
				Signed:            true,
				SignerIdentity:    "/.github/workflows/release.yml",
				CertIssuer:        "https://token.actions.githubusercontent.com",
				RepositoryURI:     "https://github.com/org/repo",
				RepositoryRef:     "refs/tags/v0.1.0",
				RunnerEnvironment: "github-hosted",
				SigstoreURL:       sigstorePublicGoodRekorURL,
			},
			want: &lockfile.Provenance{
				SignerIdentity:    "/.github/workflows/release.yml",
				CertIssuer:        "https://token.actions.githubusercontent.com",
				RepositoryURI:     "https://github.com/org/repo",
				RepositoryRef:     "refs/tags/v0.1.0",
				RunnerEnvironment: "github-hosted",
				SigstoreURL:       sigstorePublicGoodRekorURL,
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

	got := expectedIdentity(NewLockExpectation(&lockfile.Provenance{
		SignerIdentity:    "/.github/workflows/release.yml",
		CertIssuer:        "https://token.actions.githubusercontent.com",
		RepositoryURI:     "https://github.com/org/repo",
		RepositoryRef:     "refs/tags/v0.1.0",
		RunnerEnvironment: "github-hosted",
	}))
	require.NotNil(t, got)
	assert.Equal(t, "/.github/workflows/release.yml", got.SignerIdentity)
	assert.Equal(t, "https://token.actions.githubusercontent.com", got.CertIssuer)
	assert.Equal(t, "https://github.com/org/repo", got.SourceRepositoryURI)

	assert.Nil(t, expectedIdentity(NewCatalogExpectation(&regtypes.Provenance{
		SignerIdentity: "/.github/workflows/release.yml",
		CertIssuer:     "https://token.actions.githubusercontent.com",
	})), "signer and issuer without repository must not be bound into core's composed GitHub policy")

	assert.Nil(t, expectedIdentity(NewCatalogExpectation(&regtypes.Provenance{
		SignerIdentity: "/.github/workflows/release.yml",
		CertIssuer:     "https://token.actions.githubusercontent.com",
		RepositoryURI:  "https://github.com/org/repo",
	})), "even a complete catalog identity must retain independent-field semantics")
}

func TestCheckLockProvenanceExpectation(t *testing.T) {
	t.Parallel()

	observed := observedCertificate{
		Identity:          coreverifier.Identity{SignerIdentity: "/.github/workflows/release.yml"},
		RepositoryRef:     "refs/tags/v0.1.0",
		RunnerEnvironment: "github-hosted",
	}

	tests := []struct {
		name        string
		observed    observedCertificate
		expected    *lockfile.Provenance
		wantErr     bool
		wantMessage string
	}{
		{
			name:     "trust on first use constrains nothing",
			observed: observed,
		},
		{
			name:     "entry predating the fields is unconstrained",
			observed: observed,
			expected: &lockfile.Provenance{SignerIdentity: "/.github/workflows/release.yml"},
		},
		{
			name:     "both fields match",
			observed: observed,
			expected: &lockfile.Provenance{
				SignerIdentity:    "/.github/workflows/release.yml",
				RepositoryRef:     "refs/tags/v0.1.0",
				RunnerEnvironment: "github-hosted",
			},
		},
		{
			name:     "different ref rejected",
			observed: observed,
			expected: &lockfile.Provenance{
				SignerIdentity: "/.github/workflows/release.yml",
				RepositoryRef:  "refs/heads/attacker-branch",
			},
			wantErr:     true,
			wantMessage: "repository ref",
		},
		{
			name:     "different runner class rejected",
			observed: observed,
			expected: &lockfile.Provenance{
				SignerIdentity:    "/.github/workflows/release.yml",
				RunnerEnvironment: "self-hosted",
			},
			wantErr:     true,
			wantMessage: "runner environment",
		},
		{
			name: "certificate that stopped carrying the pinned ref rejected",
			observed: observedCertificate{
				Identity:          coreverifier.Identity{SignerIdentity: "/.github/workflows/release.yml"},
				RunnerEnvironment: "github-hosted",
			},
			expected: &lockfile.Provenance{
				SignerIdentity: "/.github/workflows/release.yml",
				RepositoryRef:  "refs/tags/v0.1.0",
			},
			wantErr:     true,
			wantMessage: "carries none",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkProvenanceExpectation(tc.observed, NewLockExpectation(tc.expected))
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrSignerMismatch)
			assert.Contains(t, err.Error(), tc.wantMessage,
				"the error must name which pinned field differed")
		})
	}
}

func TestCheckCatalogProvenanceExpectation(t *testing.T) {
	t.Parallel()

	observed := observedCertificate{
		Identity: coreverifier.Identity{
			SignerIdentity:      "/.github/workflows/release.yml",
			CertIssuer:          "https://token.actions.githubusercontent.com",
			SourceRepositoryURI: "https://github.com/org/repo",
		},
		RepositoryRef:     "refs/tags/v1.0.0",
		RunnerEnvironment: "github-hosted",
	}
	tests := []struct {
		name     string
		expected *regtypes.Provenance
		wantErr  bool
	}{
		{name: "empty constraints", expected: &regtypes.Provenance{}},
		{name: "signer only", expected: &regtypes.Provenance{SignerIdentity: observed.SignerIdentity}},
		{name: "issuer only", expected: &regtypes.Provenance{CertIssuer: observed.CertIssuer}},
		{name: "repository only", expected: &regtypes.Provenance{RepositoryURI: observed.SourceRepositoryURI}},
		{name: "ref only", expected: &regtypes.Provenance{RepositoryRef: observed.RepositoryRef}},
		{name: "runner only", expected: &regtypes.Provenance{RunnerEnvironment: observed.RunnerEnvironment}},
		{name: "signer mismatch", expected: &regtypes.Provenance{SignerIdentity: "attacker@example.com"}, wantErr: true},
		{name: "issuer mismatch", expected: &regtypes.Provenance{CertIssuer: "https://issuer.example.com"}, wantErr: true},
		{name: "repository mismatch", expected: &regtypes.Provenance{RepositoryURI: "https://github.com/attacker/repo"}, wantErr: true},
		{name: "ref mismatch", expected: &regtypes.Provenance{RepositoryRef: "refs/heads/attacker"}, wantErr: true},
		{name: "runner mismatch", expected: &regtypes.Provenance{RunnerEnvironment: "self-hosted"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkProvenanceExpectation(observed, NewCatalogExpectation(tc.expected))
			if tc.wantErr {
				require.ErrorIs(t, err, ErrSignerMismatch)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestClassifyVerifyFailureKeepsPinnedFieldDiagnosis guards the interaction
// between the two mismatch sources: a pinned ref/runner failure is reported
// verbatim instead of being re-derived as a signer-identity mismatch, which
// would print the expected identity back as the observed one.
func TestClassifyVerifyFailureKeepsPinnedFieldDiagnosis(t *testing.T) {
	t.Parallel()

	expected := &lockfile.Provenance{
		SignerIdentity: "/.github/workflows/release.yml",
		RepositoryRef:  "refs/tags/v0.1.0",
	}
	pinErr := pinnedFieldMismatch("repository ref", expected.RepositoryRef, "refs/heads/main")

	err := classifyVerifyFailure(nil, nil, nil, NewLockExpectation(expected), pinErr)
	require.ErrorIs(t, err, ErrSignerMismatch)
	assert.Contains(t, err.Error(), "repository ref")
	assert.NotErrorIs(t, err, ErrSignatureInvalid)
}

// TestMostUsefulVerifyErrorPrefersPinnedFieldMismatch is the regression test
// for the multi-bundle case TestClassifyVerifyFailureKeepsPinnedFieldDiagnosis
// does not reach: verifyKeylessBundles' loop calls this once per candidate
// bundle on the artifact, and a pinned-field mismatch from an EARLIER bundle
// must survive a LATER bundle's plain policy failure — not be overwritten by
// simple iteration order — or classifyVerifyFailure's ErrSignerMismatch
// short-circuit never triggers and the confusing "locked to X, verifies as
// X" message reappears exactly the way it did before that fix.
func TestMostUsefulVerifyErrorPrefersPinnedFieldMismatch(t *testing.T) {
	t.Parallel()

	pinErr := pinnedFieldMismatch("repository ref", "refs/tags/v0.1.0", "refs/heads/attacker")
	genericErr := errors.New("certificate does not chain to a trusted root")

	tests := []struct {
		name string
		errs []error
		want error
	}{
		{name: "no errors", errs: nil, want: nil},
		{name: "single generic failure", errs: []error{genericErr}, want: genericErr},
		{
			name: "pinned mismatch first, generic failure overwrites nothing",
			errs: []error{pinErr, genericErr},
			want: pinErr,
		},
		{
			name: "generic failure first, pinned mismatch still wins",
			errs: []error{genericErr, pinErr},
			want: pinErr,
		},
		{
			name: "two pinned mismatches: the first is reported",
			errs: []error{pinErr, pinnedFieldMismatch("runner environment", "github-hosted", "self-hosted")},
			want: pinErr,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mostUsefulVerifyError(tc.errs)
			if tc.want == nil {
				assert.NoError(t, got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFirstVerifiedBundleSelectsLaterCatalogMatch(t *testing.T) {
	t.Parallel()

	want := &Result{Signed: true, SignerIdentity: "matching@example.com"}
	visited := make([]string, 0, 3)
	result, errs := firstVerifiedBundle([]string{"valid-mismatch", "valid-match", "unused"},
		func(candidate string) (*Result, error) {
			visited = append(visited, candidate)
			switch candidate {
			case "valid-mismatch":
				return nil, fmt.Errorf("%w: first valid signature does not satisfy the catalog", ErrSignerMismatch)
			case "valid-match":
				return want, nil
			default:
				t.Fatalf("selection must stop after the later matching signature")
				return nil, nil
			}
		})

	assert.Same(t, want, result)
	assert.Nil(t, errs)
	assert.Equal(t, []string{"valid-mismatch", "valid-match"}, visited,
		"a mismatching valid signature must not hide a later catalog match")
}

func TestFirstVerifiedBundleReturnsEveryRejection(t *testing.T) {
	t.Parallel()

	result, errs := firstVerifiedBundle([]string{"first", "second"}, func(candidate string) (*Result, error) {
		return nil, errors.New(candidate)
	})

	assert.Nil(t, result)
	require.Len(t, errs, 2)
	assert.EqualError(t, errs[0], "first")
	assert.EqualError(t, errs[1], "second")
}

// TestVerifyOCIReportsKeySignedAgainstLockedIdentity covers a key-signed
// artifact arriving at an entry already pinned to a keyless identity. The expectation
// cannot be satisfied and cannot even be compared — there is no certificate to
// read an observed identity from — so the key-pair diagnosis must win rather
// than a signer-mismatch report naming an identity nothing produced.
func TestVerifyOCIReportsKeySignedAgainstLockedIdentity(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	ref, digest := pushTestArtifact(t, host)
	signArtifact(t, ref, digest)

	expected := NewLockExpectation(&lockfile.Provenance{
		SignerIdentity: "org/repo/.github/workflows/publish.yml",
		CertIssuer:     "https://token.actions.githubusercontent.com",
	})
	_, err := NewDefault(nil).VerifyOCI(t.Context(), ref, digest, expected)
	require.ErrorIs(t, err, ErrKeySigned)
	require.NotErrorIs(t, err, ErrSignerMismatch,
		"no certificate exists to compare, so reporting a signer mismatch would invent an observation")
}

// TestKeylessPathsRefuseKeyPinnedEntry covers the window this PR opens: the
// lock can now express a key-pinned entry, but nothing verifies one until the
// install path lands. Such an entry has empty certificate fields by
// construction, and sigstore rejects an all-empty identity ("there must be
// subject alternative name criteria") rather than treating it as
// match-anything — so the failure was already closed. What was missing was a
// message naming the actual problem instead of that one.
func TestKeylessPathsRefuseKeyPinnedEntry(t *testing.T) {
	t.Parallel()
	host := startTestRegistry(t)
	ref, digest := pushTestArtifact(t, host)
	keyed := &lockfile.Provenance{PublicKey: testPublicKeyB64}
	d := NewDefault(nil)

	_, err := d.VerifyOCI(t.Context(), ref, digest, NewLockExpectation(keyed))
	require.ErrorIs(t, err, ErrSignatureInvalid,
		"an entry that cannot be verified this way is a verification failure")
	require.ErrorContains(t, err, "pinned to a cosign public key")

	// Refused before any registry access, so an unsigned artifact does not
	// mask the misrouting as ErrUnsigned.
	require.NotErrorIs(t, err, ErrUnsigned)

	offlineErr := d.VerifyBundleOffline([]byte("ignored"), digest, keyed)
	require.ErrorIs(t, offlineErr, ErrSignatureInvalid)
	require.ErrorContains(t, offlineErr, "pinned to a cosign public key",
		"offline re-verification has the identical hazard")
}

// TestVerifyGitRefusesKeyPinnedEntry covers the third keyless path. Lock
// validation rejects a key-pinned git entry, but an expectation assembled in
// memory never passes through it — and VerifyGit would otherwise ignore
// PublicKey entirely and report an unrelated unsigned diagnosis, since a git
// commit signature is always certificate-based.
func TestVerifyGitRefusesKeyPinnedEntry(t *testing.T) {
	t.Parallel()

	expected := NewLockExpectation(&lockfile.Provenance{PublicKey: testPublicKeyB64})
	_, err := NewDefault(nil).VerifyGit(t.Context(), []byte("payload"), []byte("signature"), expected)

	require.ErrorIs(t, err, ErrSignatureInvalid)
	require.ErrorContains(t, err, "pinned to a cosign public key")
	require.NotErrorIs(t, err, ErrUnsigned,
		"refused before the signature checks, so the misrouting is not masked as unsigned")
}

// certBundle is a bundle carrying a signing certificate — the keyless layout.
// Only the verification material is populated: onlyKeylessSigned reads nothing
// else, and a fully-formed keyless bundle would need a live Fulcio.
func certBundle() coreverifier.Bundle {
	return coreverifier.Bundle{Parsed: &sgbundle.Bundle{Bundle: &protobundle.Bundle{
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_Certificate{
				Certificate: &protocommon.X509Certificate{RawBytes: []byte("der")},
			},
		},
	}}}
}

// TestOnlyKeylessSigned is the mirror of the onlyKeySigned rule: a supplied
// public key cannot verify a Fulcio-certificate signature, and saying so beats
// reporting the intact signature as broken. A mixed artifact is excluded
// because one of its bundles genuinely is key-signed, so the generic failure
// is the honest answer there.
func TestOnlyKeylessSigned(t *testing.T) {
	t.Parallel()

	keySigned := coreverifier.Bundle{}
	tests := []struct {
		name    string
		bundles []coreverifier.Bundle
		want    bool
	}{
		{name: "no bundles", bundles: nil, want: false},
		{name: "all keyless", bundles: []coreverifier.Bundle{certBundle(), certBundle()}, want: true},
		{name: "all key-signed", bundles: []coreverifier.Bundle{keySigned}, want: false},
		{name: "mixed", bundles: []coreverifier.Bundle{certBundle(), keySigned}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, onlyKeylessSigned(tc.bundles))
			// The two predicates must never both hold: they select opposite
			// verification paths, and a bundle set answering yes to both would
			// mean whichever is checked first decides the policy.
			assert.False(t, onlyKeylessSigned(tc.bundles) && onlyKeySigned(tc.bundles))
		})
	}
}
