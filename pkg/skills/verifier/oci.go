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

	"github.com/stacklok/toolhive-core/container/signer"
	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
)

// VerifyOCI discovers and verifies the Sigstore signature for an OCI
// artifact via the keyless (Fulcio) flow. See the interface documentation
// for the expected/TOFU semantics.
func (d *Default) VerifyOCI(
	ctx context.Context,
	imageRef, digest string,
	expected *ProvenanceExpectation,
) (*Result, error) {
	if keyPinnedExpectation(expected) {
		return nil, errKeyPinnedEntry
	}
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
	expectedPayload, err := signer.PayloadDigest(imageRef, digest)
	if err != nil {
		return nil, fmt.Errorf("reconstructing the signed payload for %s: %w", digest, err)
	}

	var lastErr error
	boundCandidates := 0
	for _, b := range bundles {
		// Discovery is by tag — the ".sig" manifest is found by naming it
		// after this artifact's digest — so being attached here is not
		// evidence of being about this artifact. Only the payload says that.
		if !bundleSignsPayload(b, expectedPayload) {
			continue
		}
		boundCandidates++
		if _, verifyErr := coreverifier.VerifyBundleWithKey(b, pubKeyPEM); verifyErr != nil {
			lastErr = verifyErr
			continue
		}
		return resultFromKey(b.Raw), nil
	}
	// Only after every bundle failed: a key-signed bundle that verifies wins
	// regardless of what else is attached to the artifact. Reported ahead of
	// the generic failure so aiming a key at a keylessly-signed artifact is
	// named as the mistake it is rather than reported as a bad signature.
	if onlyKeylessSigned(bundles) {
		return nil, ErrKeylessSigned
	}
	if boundCandidates == 0 {
		return nil, fmt.Errorf("%w: signature material is attached to this artifact but none of it"+
			" signs this artifact — the signed payload names a different repository or digest",
			ErrSignatureInvalid)
	}
	return nil, wrapInvalid(lastErr)
}

// bundleSignsPayload reports whether b's signature covers the simple-signing
// payload for the artifact under verification.
//
// core binds a candidate to the digest of the layer the candidate came from
// (RetrieveBundles records it as DigestAlgo/DigestHex), which proves the
// signature covers that blob intact but says nothing about which artifact the
// blob describes. The artifact is named only inside the payload, as a
// repository and a manifest digest, so agreeing with a payload digest
// reconstructed from the requested reference is what ties the two together.
//
// Without this, a valid signature is transplantable: copying artifact A's
// signature layer into "sha256-<B>.sig" makes it discoverable as B's, and it
// still verifies — B is then accepted under whatever key legitimately signed
// A. Comparing digests rather than parsing the payload keeps the check on the
// bytes that were actually signed; a payload edited to name B no longer
// hashes to the layer digest core verified the signature against.
//
// REMOVE THIS when the toolhive-core dependency moves past v0.0.42.
// toolhive-core#263 fixes the same gap at the source and inverts the contract
// this relies on: Bundle.DigestHex becomes the ARTIFACT digest rather than the
// payload digest, so the comparison below turns into artifact-vs-payload and
// can never hold. That fails closed, not open, and the sign-then-verify round
// trip in TestVerifyOCIWithKeyRoundTrip fails with it — so a bump surfaces as
// a red test rather than as silently disabled verification. The fix then is to
// delete this helper and let core's ErrSignatureArtifactMismatch do the work,
// NOT to loosen the comparison.
func bundleSignsPayload(b coreverifier.Bundle, expectedPayload string) bool {
	return b.DigestAlgo+":"+b.DigestHex == expectedPayload
}

// verifyKeylessBundles verifies bundles until one passes the keyless policy
// and its source-specific provenance expectation, returning its result or
// nil with the most useful verification error.
func verifyKeylessBundles(
	bundles []coreverifier.Bundle,
	tm root.TrustedMaterial,
	opts []verify.VerifierOption,
	expected *ProvenanceExpectation,
) (*Result, error) {
	result, errs := firstVerifiedBundle(bundles, func(b coreverifier.Bundle) (*Result, error) {
		return verifyOneKeylessBundle(b, tm, opts, expected)
	})
	if result != nil {
		return result, nil
	}
	return nil, mostUsefulVerifyError(errs)
}

// firstVerifiedBundle returns the first bundle accepted by verify and keeps
// every rejection when none match. Keeping selection separate from the
// cryptographic operation makes the "any satisfying valid signature wins"
// rule explicit for multi-signature artifacts.
func firstVerifiedBundle[T any](bundles []T, verifyBundle func(T) (*Result, error)) (*Result, []error) {
	errList := make([]error, 0, len(bundles))
	for _, bundle := range bundles {
		result, err := verifyBundle(bundle)
		if err != nil {
			errList = append(errList, err)
			continue
		}
		return result, nil
	}
	return nil, errList
}

// verifyOneKeylessBundle verifies a single bundle against the keyless
// policy, then its source-specific provenance expectation. A mismatch
// disqualifies the bundle exactly like a policy failure — another signature
// on the artifact may satisfy the full expectation.
func verifyOneKeylessBundle(
	b coreverifier.Bundle,
	tm root.TrustedMaterial,
	opts []verify.VerifierOption,
	expected *ProvenanceExpectation,
) (*Result, error) {
	vr, verifyErr := coreverifier.VerifyBundle(b, tm, expectedIdentity(expected), opts...)
	if verifyErr != nil {
		return nil, verifyErr
	}
	observed, idErr := observedFromResult(vr)
	if idErr != nil {
		return nil, idErr
	}
	// Catalog constraints are checked here, inside the per-bundle loop, so a
	// mismatching valid signature does not hide a later satisfying one.
	if err := checkProvenanceExpectation(observed, expected); err != nil {
		return nil, err
	}
	return resultFromCore(observed, b.Raw), nil
}

// mostUsefulVerifyError picks the most specific diagnosis out of a set of
// per-bundle verification failures. A pinned-field mismatch
// (ErrSignerMismatch from checkPinnedCertificateFields) is preferred over
// any other failure regardless of which bundle in the loop produced it: to
// reach that mismatch, the bundle's signer identity and issuer were already
// accepted by the Sigstore policy, which is a more specific and more useful
// diagnosis than a bundle that failed the policy outright. Without this, a
// later bundle's bare policy failure would overwrite the pinned-field
// diagnosis by iteration order alone, and classifyVerifyFailure's
// ErrSignerMismatch short-circuit would never trigger — silently falling
// back to the confusing "locked to X, verifies as X" message it exists to
// avoid. When no pinned-field mismatch occurred, the last error is
// returned, preserving prior behavior.
func mostUsefulVerifyError(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	for _, err := range errs {
		if errors.Is(err, ErrSignerMismatch) {
			return err
		}
	}
	return errs[len(errs)-1]
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
// signature. Lock identities are enforced inside the Sigstore policy, so a
// mismatch surfaces as a verification failure; re-verifying without the
// identity constraint tells the two apart. Catalog mismatches are already
// classified inside the per-bundle loop.
func classifyVerifyFailure(
	bundles []coreverifier.Bundle,
	tm root.TrustedMaterial,
	opts []verify.VerifierOption,
	expected *ProvenanceExpectation,
	lastErr error,
) error {
	// A pinned ref or runner mismatch is already the precise diagnosis, and
	// naming the field is the whole value of it: the Sigstore policy accepted
	// the certificate, so re-verifying without the identity constraint would
	// report the expected signer identity back as the observed one.
	if errors.Is(lastErr, ErrSignerMismatch) {
		return lastErr
	}
	if expected != nil {
		for _, b := range bundles {
			vr, err := coreverifier.VerifyBundle(b, tm, nil, opts...)
			if err != nil {
				continue
			}
			return signerMismatchError(vr, expected)
		}
	}
	// Every bundle being certificate-less is the cosign key-pair layout, not
	// a broken keyless signature — the keyless policy could never have
	// accepted it. Falling through to wrapInvalid would report
	// ErrSignatureInvalid, sending the user hunting a corrupt signature; and
	// because --allow-unsigned only overrides ErrUnsigned, it would leave
	// them a 403 with no available remedy. A mixed artifact keeps the
	// keyless diagnosis: one of its bundles genuinely failed the policy.
	if onlyKeySigned(bundles) {
		return ErrKeySigned
	}
	return wrapInvalid(lastErr)
}

// onlyKeySigned reports whether every retrieved bundle uses the cosign
// key-pair layout. An empty slice is not key-signed: retrieveBundles already
// reports having found no signature material as ErrUnsigned, so classify is
// never reached with one.
func onlyKeySigned(bundles []coreverifier.Bundle) bool {
	if len(bundles) == 0 {
		return false
	}
	for _, b := range bundles {
		if b.HasCertificate() {
			return false
		}
	}
	return true
}

// onlyKeylessSigned reports whether every retrieved bundle carries a Fulcio
// certificate — the keyless layout, which no supplied public key can verify.
// The inverse of onlyKeySigned, and an empty slice is excluded for the same
// reason.
func onlyKeylessSigned(bundles []coreverifier.Bundle) bool {
	if len(bundles) == 0 {
		return false
	}
	for _, b := range bundles {
		if !b.HasCertificate() {
			return false
		}
	}
	return true
}

// signerMismatchError builds the ErrSignerMismatch error, naming both the
// expected identity tuple and the identity the artifact actually verifies
// with (when extractable).
func signerMismatchError(vr *verify.VerificationResult, expected *ProvenanceExpectation) error {
	observed, idErr := observedFromResult(vr)
	if idErr != nil {
		return fmt.Errorf("%w: artifact is signed by a different identity", ErrSignerMismatch)
	}
	if expected.catalog != nil {
		return checkCatalogExpectation(observed, expected.catalog)
	}
	p := expected.locked
	return fmt.Errorf("%w: locked to %q (issuer %q), but the artifact verifies as %q (issuer %q)",
		ErrSignerMismatch,
		p.SignerIdentity, p.CertIssuer,
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
