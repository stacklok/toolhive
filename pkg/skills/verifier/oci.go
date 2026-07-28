// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// VerifyOCI discovers and verifies the Sigstore signature for an OCI
// artifact via the keyless (Fulcio) flow. See the interface documentation
// for the expected/TOFU semantics.
func (d *Default) VerifyOCI(
	ctx context.Context,
	imageRef, digest string,
	expected *lockfile.Provenance,
) (*Result, error) {
	bundles, err := d.retrieveBundles(ctx, imageRef, digest)
	if err != nil {
		return nil, err
	}

	tm, err := coreverifier.OfflineTrustedMaterial()
	if err != nil {
		return nil, fmt.Errorf("loading trusted material: %w", err)
	}
	opts, err := coreverifier.DefaultVerifierOptions()
	if err != nil {
		return nil, fmt.Errorf("loading verifier options: %w", err)
	}

	var lastErr error
	for _, b := range bundles {
		vr, verifyErr := coreverifier.VerifyBundle(b, tm, expectedIdentity(expected), opts...)
		if verifyErr != nil {
			lastErr = verifyErr
			continue
		}
		identity, idErr := coreverifier.IdentityFromResult(vr)
		if idErr != nil {
			lastErr = idErr
			continue
		}
		return resultFromCore(identity, b.Raw), nil
	}
	return nil, d.classifyVerifyFailure(bundles, tm, opts, expected, lastErr)
}

// VerifyOCIWithKey discovers and verifies the Sigstore signature for an OCI
// artifact against a PEM public key (the cosign key-pair flow).
func (d *Default) VerifyOCIWithKey(
	ctx context.Context,
	imageRef, digest string,
	pubKeyPEM []byte,
) (*Result, error) {
	bundles, err := d.retrieveBundles(ctx, imageRef, digest)
	if err != nil {
		return nil, err
	}

	for _, b := range bundles {
		if _, verifyErr := coreverifier.VerifyBundleWithKey(b, pubKeyPEM); verifyErr != nil {
			continue
		}
		return &Result{
			Signed:      true,
			SigstoreURL: sigstorePublicGoodRekorURL,
			Bundle:      b.Raw,
		}, nil
	}
	return nil, ErrSignatureInvalid
}

// retrieveBundles fetches the signature bundles for the artifact pinned to
// the digest, mapping core's unsigned signal to ErrUnsigned.
func (d *Default) retrieveBundles(ctx context.Context, imageRef, digest string) ([]coreverifier.Bundle, error) {
	ref := imageRef
	if digest != "" && !strings.Contains(ref, "@") {
		ref = imageRef + "@" + digest
	}
	bundles, err := coreverifier.RetrieveBundles(ctx, ref, d.keychain)
	if errors.Is(err, coreverifier.ErrNoBundles) {
		return nil, fmt.Errorf("%w: %w", ErrUnsigned, err)
	}
	if err != nil {
		return nil, err
	}
	return bundles, nil
}

// classifyVerifyFailure distinguishes a signer mismatch from an invalid
// signature. The expected identity is enforced inside the Sigstore policy,
// so a mismatch surfaces as a verification failure; re-verifying without
// the identity constraint tells the two apart: if the chain of trust holds
// without the constraint, the failure was the identity.
func (*Default) classifyVerifyFailure(
	bundles []coreverifier.Bundle,
	tm root.TrustedMaterial,
	opts []verify.VerifierOption,
	expected *lockfile.Provenance,
	lastErr error,
) error {
	if expected != nil {
		for _, b := range bundles {
			if _, err := coreverifier.VerifyBundle(b, tm, nil, opts...); err == nil {
				return fmt.Errorf("%w: artifact is signed by a different identity than %q",
					ErrSignerMismatch, expected.SignerIdentity)
			}
		}
	}
	if lastErr != nil {
		return fmt.Errorf("%w: %w", ErrSignatureInvalid, lastErr)
	}
	return ErrSignatureInvalid
}
