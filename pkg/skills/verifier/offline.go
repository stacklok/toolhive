// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"errors"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/verify"

	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// VerifyBundleOffline re-verifies a stored bundle against the artifact
// digest without network access. A non-nil expected identity is enforced
// inside the Sigstore policy; a mismatch is reported as ErrSignerMismatch,
// any other verification failure as ErrSignatureInvalid.
func (*Default) VerifyBundleOffline(bundleBytes []byte, digest string, expected *lockfile.Provenance) error {
	if expected != nil && expected.PublicKey != "" {
		return errKeyPinnedEntry
	}
	if len(bundleBytes) == 0 {
		// Classified as an invalid signature because a recorded identity
		// with nothing backing it cannot be verified; the message leads
		// with the actionable fact — the stored bundle is missing, so
		// reinstalling (or re-adopting) is the fix.
		return fmt.Errorf("%w: no stored bundle to verify — reinstall to restore it", ErrSignatureInvalid)
	}
	vr, err := coreverifier.VerifyBundleOffline(bundleBytes, digest, expectedIdentity(NewLockExpectation(expected)))
	if err == nil {
		return checkStoredBundlePins(vr, expected)
	}
	if expected != nil && !errors.Is(err, coreverifier.ErrVerificationFailed) {
		// Malformed input never reaches verification; don't reclassify.
		return fmt.Errorf("%w: %s", ErrSignatureInvalid, err.Error())
	}
	if expected != nil {
		// The identity is bound into the policy, so a mismatch surfaces as
		// a verification failure; re-verifying without the constraint tells
		// mismatch apart from a broken signature — and yields the identity
		// that DID verify, which the error reports.
		if vr, tofuErr := coreverifier.VerifyBundleOffline(bundleBytes, digest, nil); tofuErr == nil {
			return signerMismatchError(vr, NewLockExpectation(expected))
		}
	}
	return wrapInvalid(err)
}

// VerifyBundleOfflineWithKey re-verifies a stored key-signed bundle against
// the signer's PEM public key — the offline counterpart of VerifyOCIWithKey.
//
// The artifact digest is passed straight through. A key-signed bundle's
// signature covers the cosign simple-signing payload rather than the artifact,
// but the stored bundle now carries that payload with it, so core recovers it
// and checks both that the signature covers those bytes and that those bytes
// name this artifact. Reconstructing the payload here from the reference —
// which is what this did before, via signer.PayloadDigest — is now refused as
// ErrSignatureArtifactMismatch: a digest derived from the payload proves
// nothing about which artifact it names, so a caller supplying it is exactly
// the caller who cannot detect a transplanted signature.
func (*Default) VerifyBundleOfflineWithKey(bundleBytes []byte, digest string, pubKeyPEM []byte) error {
	if len(bundleBytes) == 0 {
		return fmt.Errorf("%w: no stored bundle to verify — reinstall to restore it", ErrSignatureInvalid)
	}
	if _, err := coreverifier.VerifyBundleOfflineWithKey(bundleBytes, digest, pubKeyPEM); err != nil {
		return wrapInvalid(err)
	}
	return nil
}

// ResultFromBundle verifies a stored bundle offline (chain of trust only)
// and returns the observed identity, for back-filling provenance of
// adopted skills.
//
// A key-signed bundle is reported as ErrKeySigned rather than attempted:
// there is no identity to observe, so the back-fill this exists for has
// nothing to record, and the keyless verification below would fail on the
// missing certificate with a message about the wrong thing entirely.
func (*Default) ResultFromBundle(bundleBytes []byte, digest string) (*Result, error) {
	if len(bundleBytes) == 0 {
		return nil, fmt.Errorf("%w: no stored bundle to verify", ErrSignatureInvalid)
	}
	keySigned, err := bundleIsKeySigned(bundleBytes, digest)
	if err != nil {
		return nil, wrapInvalid(err)
	}
	if keySigned {
		return nil, ErrKeySigned
	}
	vr, err := coreverifier.VerifyBundleOffline(bundleBytes, digest, nil)
	if err != nil {
		return nil, wrapInvalid(err)
	}
	observed, err := observedFromResult(vr)
	if err != nil {
		return nil, wrapInvalid(err)
	}
	return resultFromCore(observed, bundleBytes), nil
}

// checkStoredBundlePins enforces the pinned ref and runner class against a
// stored bundle that just passed offline verification. The lock file's
// provenance and the bundle backing it are stored separately (lock file
// versus install record), so a bundle whose certificate no longer carries
// the recorded ref is exactly the substitution this re-verification exists
// to catch.
func checkStoredBundlePins(vr *verify.VerificationResult, expected *lockfile.Provenance) error {
	if expected == nil || (expected.RepositoryRef == "" && expected.RunnerEnvironment == "") {
		return nil
	}
	observed, err := observedFromResult(vr)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSignatureInvalid, err.Error())
	}
	return checkPinnedCertificateFields(observed, expected)
}

// bundleIsKeySigned reports whether a stored bundle came from the cosign
// key-pair flow, which is visible in its shape: a keyless bundle carries the
// Fulcio certificate its identity is read from, and a key-signed one carries
// none, since the trust anchor lives outside the artifact entirely.
//
// Decoded through core rather than straight into a Sigstore bundle: the
// durable form is not always bare bundle JSON — a cosign signature is stored
// wrapped with the payload it covers — and unmarshalling the envelope as a
// bundle fails on the wrapper's own fields. A shape question must not report
// a well-formed bundle as unparsable.
//
// The digest is only needed to construct the Bundle and is not checked here;
// callers verify separately.
func bundleIsKeySigned(bundleBytes []byte, digest string) (bool, error) {
	b, err := coreverifier.DecodeStoredBundle(bundleBytes, digest)
	if err != nil {
		return false, err
	}
	return !b.HasCertificate(), nil
}
