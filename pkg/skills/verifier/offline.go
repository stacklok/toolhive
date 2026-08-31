// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"errors"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/stacklok/toolhive-core/container/signer"
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
// the signer's PEM public key — the offline counterpart of
// VerifyOCIWithKey. Key-signed bundles sign the cosign simple-signing
// payload (which embeds the artifact digest), so the payload is
// reconstructed from imageRef and digest and the signature checked over it
// — that reconstruction IS the digest-binding check.
func (*Default) VerifyBundleOfflineWithKey(bundleBytes []byte, imageRef, digest string, pubKeyPEM []byte) error {
	if len(bundleBytes) == 0 {
		return fmt.Errorf("%w: no stored bundle to verify — reinstall to restore it", ErrSignatureInvalid)
	}
	digestArg, err := signer.PayloadDigest(imageRef, digest)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSignatureInvalid, err.Error())
	}
	if _, err := coreverifier.VerifyBundleOfflineWithKey(bundleBytes, digestArg, pubKeyPEM); err != nil {
		return wrapInvalid(err)
	}
	return nil
}

// ResultFromBundle verifies a stored bundle offline (chain of trust only)
// and returns the observed identity, for back-filling provenance of
// adopted skills.
func (*Default) ResultFromBundle(bundleBytes []byte, digest string) (*Result, error) {
	if len(bundleBytes) == 0 {
		return nil, fmt.Errorf("%w: no stored bundle to verify", ErrSignatureInvalid)
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
