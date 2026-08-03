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

	result, lastErr := verifyKeylessBundles(bundles, tm, opts, expected)
	if result != nil {
		return result, nil
	}
	return nil, classifyVerifyFailure(bundles, tm, opts, expected, lastErr)
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

	var lastErr error
	for _, b := range bundles {
		if _, verifyErr := coreverifier.VerifyBundleWithKey(b, pubKeyPEM); verifyErr != nil {
			lastErr = verifyErr
			continue
		}
		return resultFromKey(b.Raw), nil
	}
	return nil, wrapInvalid(lastErr)
}

// verifyKeylessBundles verifies bundles until one passes the keyless policy,
// returning its result, or nil with the last verification error.
func verifyKeylessBundles(
	bundles []coreverifier.Bundle,
	tm root.TrustedMaterial,
	opts []verify.VerifierOption,
	expected *lockfile.Provenance,
) (*Result, error) {
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
	return nil, lastErr
}

// retrieveBundles fetches the signature bundles for the artifact pinned to
// the digest, mapping core's unsigned signal to ErrUnsigned. The digest is
// required — verification without a pinned digest would leave tag
// resolution to fetch time — and when imageRef already embeds one, the two
// must agree: verifying the ref's digest while the caller believes the
// parameter's was verified would hide lock corruption.
func (d *Default) retrieveBundles(ctx context.Context, imageRef, digest string) ([]coreverifier.Bundle, error) {
	if digest == "" {
		return nil, errors.New("artifact digest is required for verification")
	}
	ref := imageRef
	if embedded, ok := splitEmbeddedDigest(imageRef); ok {
		if embedded != digest {
			return nil, fmt.Errorf("reference %q embeds digest %s but %s was requested — refusing to verify ambiguous input",
				imageRef, embedded, digest)
		}
	} else {
		ref = imageRef + "@" + digest
	}
	bundles, err := coreverifier.RetrieveBundles(ctx, ref, d.keychain)
	if errors.Is(err, coreverifier.ErrNoBundles) {
		return nil, fmt.Errorf("%w: no signature material found for %s", ErrUnsigned, ref)
	}
	if err != nil {
		return nil, err
	}
	return bundles, nil
}

// splitEmbeddedDigest returns the digest embedded in an OCI reference
// ("repo@sha256:..."), if any.
func splitEmbeddedDigest(imageRef string) (string, bool) {
	_, embedded, ok := strings.Cut(imageRef, "@")
	return embedded, ok
}

// classifyVerifyFailure distinguishes a signer mismatch from an invalid
// signature. The expected identity is enforced inside the Sigstore policy,
// so a mismatch surfaces as a verification failure; re-verifying without
// the identity constraint tells the two apart: if the chain of trust holds
// without the constraint, the failure was the identity — and the identity
// that DID verify is reported, so an operator can tell a legitimate
// publisher rotation from an artifact substitution.
func classifyVerifyFailure(
	bundles []coreverifier.Bundle,
	tm root.TrustedMaterial,
	opts []verify.VerifierOption,
	expected *lockfile.Provenance,
	lastErr error,
) error {
	if expected != nil {
		for _, b := range bundles {
			vr, err := coreverifier.VerifyBundle(b, tm, nil, opts...)
			if err != nil {
				continue
			}
			return signerMismatchError(vr, expected)
		}
	}
	return wrapInvalid(lastErr)
}

// signerMismatchError builds the ErrSignerMismatch error, naming both the
// expected identity tuple and the identity the artifact actually verifies
// with (when extractable).
func signerMismatchError(vr *verify.VerificationResult, expected *lockfile.Provenance) error {
	observed, idErr := coreverifier.IdentityFromResult(vr)
	if idErr != nil {
		return fmt.Errorf("%w: artifact is signed by a different identity than %q (issuer %q)",
			ErrSignerMismatch, expected.SignerIdentity, expected.CertIssuer)
	}
	return fmt.Errorf("%w: locked to %q (issuer %q), but the artifact verifies as %q (issuer %q)",
		ErrSignerMismatch,
		expected.SignerIdentity, expected.CertIssuer,
		observed.SignerIdentity, observed.CertIssuer)
}

// wrapInvalid wraps a verification cause in ErrSignatureInvalid without
// stuttering: core's own verification-failed sentinel prefix is trimmed
// from the display text (the classification value it carried is replaced by
// our sentinel; this is message cosmetics, not error matching).
func wrapInvalid(cause error) error {
	if cause == nil {
		return ErrSignatureInvalid
	}
	text := strings.TrimPrefix(cause.Error(), coreverifier.ErrVerificationFailed.Error()+": ")
	return fmt.Errorf("%w: %s", ErrSignatureInvalid, text)
}
