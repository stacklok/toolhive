// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"context"
	"fmt"
	"strings"
)

// VerifyOCI discovers and verifies the Sigstore signature for an OCI artifact.
func (d *Default) VerifyOCI(ctx context.Context, imageRef, digestStr string) (*Result, error) {
	_ = ctx
	ref := imageRef
	if digestStr != "" && !strings.Contains(ref, "@") {
		ref = fmt.Sprintf("%s@%s", imageRef, digestStr)
	}

	bundles, err := fetchOCIBundles(ref, d.keychain)
	if err != nil {
		if err == ErrBundleNotFound {
			return nil, ErrUnsigned
		}
		return nil, err
	}

	digestBytes, digestAlgo, err := digestBytesFromString(digestStr)
	if err != nil && len(bundles) > 0 {
		digestBytes = bundles[0].digestBytes
		digestAlgo = bundles[0].digestAlgo
	}

	sev, err := newSigstoreVerifier()
	if err != nil {
		return nil, err
	}

	for _, b := range bundles {
		policyDigest := digestBytes
		policyAlgo := digestAlgo
		if len(policyDigest) == 0 {
			policyDigest = b.digestBytes
			policyAlgo = b.digestAlgo
		}
		vr, verifyErr := sev.Verify(b.bundle, verifyPolicy(policyAlgo, policyDigest))
		if verifyErr != nil {
			continue
		}
		result, idErr := identityFromVerificationResult(vr)
		if idErr != nil {
			continue
		}
		result.Bundle = b.raw
		return result, nil
	}
	return nil, ErrSignatureInvalid
}
