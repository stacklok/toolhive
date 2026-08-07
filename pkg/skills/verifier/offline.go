// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/signer"
)

// VerifyBundleOffline re-verifies a stored bundle against the artifact
// digest without network access. A non-nil expected identity is enforced
// inside the Sigstore policy; a mismatch is reported as ErrSignerMismatch,
// any other verification failure as ErrSignatureInvalid.
func (*Default) VerifyBundleOffline(bundleBytes []byte, digest string, expected *lockfile.Provenance) error {
	if len(bundleBytes) == 0 {
		// Classified as an invalid signature because a recorded identity
		// with nothing backing it cannot be verified; the message leads
		// with the actionable fact — the stored bundle is missing, so
		// reinstalling (or re-adopting) is the fix.
		return fmt.Errorf("%w: no stored bundle to verify — reinstall to restore it", ErrSignatureInvalid)
	}
	_, err := coreverifier.VerifyBundleOffline(bundleBytes, digest, expectedIdentity(expected))
	if err == nil {
		return nil
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
			return signerMismatchError(vr, expected)
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
	payload, err := signer.SimpleSigningPayload(imageRef, digest)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSignatureInvalid, err.Error())
	}
	payloadDigest := sha256.Sum256(payload)
	digestArg := coreverifier.DigestAlgorithmSHA256 + ":" + hex.EncodeToString(payloadDigest[:])
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
	identity, err := coreverifier.IdentityFromResult(vr)
	if err != nil {
		return nil, wrapInvalid(err)
	}
	return resultFromCore(identity, bundleBytes), nil
}
