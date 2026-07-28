// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"errors"
	"fmt"

	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// VerifyBundleOffline re-verifies a stored bundle against the artifact
// digest without network access. A non-nil expected identity is enforced
// inside the Sigstore policy; a mismatch is reported as ErrSignerMismatch,
// any other verification failure as ErrSignatureInvalid.
func (*Default) VerifyBundleOffline(bundleBytes []byte, digest string, expected *lockfile.Provenance) error {
	if len(bundleBytes) == 0 {
		return fmt.Errorf("%w: no stored bundle", ErrSignatureInvalid)
	}
	_, err := coreverifier.VerifyBundleOffline(bundleBytes, digest, expectedIdentity(expected))
	if err == nil {
		return nil
	}
	if expected != nil && !errors.Is(err, coreverifier.ErrVerificationFailed) {
		// Malformed input never reaches verification; don't reclassify.
		return fmt.Errorf("%w: %w", ErrSignatureInvalid, err)
	}
	if expected != nil {
		// The identity is bound into the policy, so a mismatch surfaces as
		// a verification failure; re-verifying without the constraint tells
		// mismatch apart from a broken signature.
		if _, tofuErr := coreverifier.VerifyBundleOffline(bundleBytes, digest, nil); tofuErr == nil {
			return fmt.Errorf("%w: bundle is signed by a different identity than %q",
				ErrSignerMismatch, expected.SignerIdentity)
		}
	}
	return fmt.Errorf("%w: %w", ErrSignatureInvalid, err)
}

// ResultFromBundle verifies a stored bundle offline (chain of trust only)
// and returns the observed identity, for back-filling provenance of
// adopted skills.
func (*Default) ResultFromBundle(bundleBytes []byte, digest string) (*Result, error) {
	if len(bundleBytes) == 0 {
		return nil, fmt.Errorf("%w: no stored bundle", ErrSignatureInvalid)
	}
	vr, err := coreverifier.VerifyBundleOffline(bundleBytes, digest, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSignatureInvalid, err)
	}
	identity, err := coreverifier.IdentityFromResult(vr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSignatureInvalid, err)
	}
	return resultFromCore(identity, bundleBytes), nil
}
