// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	cms "github.com/github/smimesign/ietf-cms"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// VerifyGit cryptographically verifies a gitsign commit signature: the CMS
// signature is checked over the commit payload and the signing certificate
// chain is verified against the Fulcio roots in toolhive-core's embedded
// trusted material — no network. A non-nil expected identity must match the
// certificate identity (post-hoc comparison: git signatures have no
// Sigstore bundle to bind a policy into); nil expected is trust on first
// use.
//
// The embedded Rekor transparency-log proof is NOT yet validated — signing
// time is checked against the certificate's own validity window, matching
// gitsign's certificate verifier. Rekor proof validation is a tracked
// follow-up (it requires reconstructing the proof from CMS unsigned
// attributes, which gitsign only exposes internally).
func (*Default) VerifyGit(
	ctx context.Context,
	payload, signature []byte,
	expected *lockfile.Provenance,
) (*Result, error) {
	if len(signature) == 0 {
		return nil, fmt.Errorf("%w: commit is not signed", ErrUnsigned)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: no commit payload to verify", ErrSignatureInvalid)
	}

	roots, intermediates, err := fulcioPools()
	if err != nil {
		return nil, err
	}
	cert, err := verifyGitSignature(ctx, payload, signature, roots, intermediates)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSignatureInvalid, err)
	}

	identity, err := gitIdentityFromCertificate(cert)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSignatureInvalid, err)
	}
	if expected != nil && !gitIdentityMatches(identity, expected) {
		return nil, fmt.Errorf("%w: commit is signed by a different identity than %q",
			ErrSignerMismatch, expected.SignerIdentity)
	}
	result := resultFromCore(identity, nil)
	// No transparency-log proof is validated yet (see the doc comment), so
	// git verification carries a documented assurance gap — marked so the
	// lock file shows it rather than implying full verification.
	result.Provisional = true
	return result, nil
}

// verifyGitSignature verifies the CMS (PKCS#7) signature gitsign attaches
// to commits: the armored signature is parsed, the message digest and
// signature are checked over the commit payload, and the signing
// certificate chain is verified against the given pools. It returns the
// certificate the signature was authenticated against.
//
// This mirrors gitsign's own CertVerifier (which wraps a fork of the same
// ietf-cms package) without importing the gitsign module, whose package
// graph compiles cosign and the cloud KMS SDKs into the binary. The
// verification time is anchored inside the signing certificate's own
// validity window — Fulcio certificates live for minutes, so "valid at
// time.Now" would reject every real signature; proving the actual signing
// time is the transparency log's job (tracked follow-up).
func verifyGitSignature(
	_ context.Context,
	payload, signature []byte,
	roots, intermediates *x509.CertPool,
) (*x509.Certificate, error) {
	// ietf-cms v0.2.0 mutates package-level state during BER decoding
	// (protocol.encodeIndent), so concurrent ParseSignedData calls race.
	// Verification is not hot-path — serialize instead of forking the
	// library like gitsign did.
	cmsMu.Lock()
	defer cmsMu.Unlock()

	der := signature
	if blk, _ := pem.Decode(signature); blk != nil {
		der = blk.Bytes
	}
	sd, err := cms.ParseSignedData(der)
	if err != nil {
		return nil, fmt.Errorf("parsing CMS signature: %w", err)
	}
	certs, err := sd.GetCertificates()
	if err != nil {
		return nil, fmt.Errorf("reading signature certificates: %w", err)
	}
	leaf := leafCertificate(certs)
	if leaf == nil {
		return nil, errors.New("no signing certificate found in signature")
	}

	chains, err := sd.VerifyDetached(payload, x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		CurrentTime:   leaf.NotBefore.Add(time.Minute),
	})
	if err != nil {
		return nil, fmt.Errorf("verifying signature: %w", err)
	}
	// Return the leaf of the first verified chain — the certificate the
	// CMS verifier actually authenticated the signature against.
	if len(chains) == 0 || len(chains[0]) == 0 || len(chains[0][0]) == 0 {
		return nil, errors.New("no verified certificate chains returned")
	}
	return chains[0][0][0], nil
}

// cmsMu serializes ietf-cms parsing/verification; see verifyGitSignature.
var cmsMu sync.Mutex

// leafCertificate picks the end-entity certificate from a CMS certificate
// bag (the one that is not a CA), falling back to the first entry.
func leafCertificate(certs []*x509.Certificate) *x509.Certificate {
	for _, c := range certs {
		if !c.IsCA {
			return c
		}
	}
	if len(certs) > 0 {
		return certs[0]
	}
	return nil
}

// fulcioPools builds root and intermediate certificate pools from the
// Fulcio certificate authorities in the embedded trusted material.
func fulcioPools() (roots, intermediates *x509.CertPool, err error) {
	tm, err := coreverifier.OfflineTrustedMaterial()
	if err != nil {
		return nil, nil, fmt.Errorf("loading trusted material: %w", err)
	}
	roots = x509.NewCertPool()
	intermediates = x509.NewCertPool()
	for _, ca := range tm.FulcioCertificateAuthorities() {
		fulcioCA, ok := ca.(*root.FulcioCertificateAuthority)
		if !ok || fulcioCA.Root == nil {
			continue
		}
		roots.AddCert(fulcioCA.Root)
		for _, intermediate := range fulcioCA.Intermediates {
			intermediates.AddCert(intermediate)
		}
	}
	return roots, intermediates, nil
}

// gitIdentityFromCertificate extracts the signer identity from a gitsign
// certificate, reusing core's identity normalization (GitHub Actions
// workflow paths, issuer extraction) by summarizing the certificate the
// same way sigstore-go does for bundle verification results.
func gitIdentityFromCertificate(cert *x509.Certificate) (coreverifier.Identity, error) {
	summary, err := certificate.SummarizeCertificate(cert)
	if err != nil {
		return coreverifier.Identity{}, fmt.Errorf("summarizing certificate: %w", err)
	}
	return coreverifier.IdentityFromResult(&verify.VerificationResult{
		Signature: &verify.SignatureVerificationResult{Certificate: &summary},
	})
}

// gitIdentityMatches compares the observed certificate identity against the
// lock file's expected provenance. Unlike the OCI path there is no Sigstore
// policy to bind the identity into, so the comparison is explicit; empty
// expected fields are not wildcards — SignerIdentity and CertIssuer are
// always recorded together at trust-on-first-use.
func gitIdentityMatches(observed coreverifier.Identity, expected *lockfile.Provenance) bool {
	if observed.SignerIdentity != expected.SignerIdentity {
		return false
	}
	if observed.CertIssuer != expected.CertIssuer {
		return false
	}
	if expected.RepositoryURI != "" && observed.SourceRepositoryURI != expected.RepositoryURI {
		return false
	}
	return true
}
