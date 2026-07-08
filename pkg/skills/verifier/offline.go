// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// VerifyBundleOffline re-verifies a stored bundle without network access.
func (d *Default) VerifyBundleOffline(bundleBytes []byte, digestStr string, expected *lockfile.Provenance) error {
	result, err := d.verifyBundleBytes(bundleBytes, digestStr)
	if err != nil {
		return err
	}
	return MatchIdentity(result, expected)
}

// ResultFromBundle verifies a stored bundle and returns the observed identity.
func (d *Default) ResultFromBundle(bundleBytes []byte, digestStr string) (*Result, error) {
	return d.verifyBundleBytes(bundleBytes, digestStr)
}

func (*Default) verifyBundleBytes(bundleBytes []byte, digestStr string) (*Result, error) {
	if len(bundleBytes) == 0 {
		return nil, ErrSignatureInvalid
	}
	bun := &bundle.Bundle{}
	if err := bun.UnmarshalJSON(bundleBytes); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	}
	digestBytes, digestAlgo, err := digestBytesFromString(digestStr)
	if err != nil {
		return nil, err
	}
	sev, err := newSigstoreVerifier()
	if err != nil {
		return nil, err
	}
	vr, err := sev.Verify(bun, verifyPolicy(digestAlgo, digestBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	}
	result, err := identityFromVerificationResult(vr)
	if err != nil {
		return nil, err
	}
	result.Bundle = bundleBytes
	return result, nil
}
