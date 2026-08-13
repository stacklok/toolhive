// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"fmt"
	"strconv"

	"github.com/sigstore/sigstore-go/pkg/verify"

	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// sigstorePublicGoodRekorURL identifies the transparency log instance the
// embedded trust root belongs to, recorded in lock provenance for humans
// auditing the file.
const sigstorePublicGoodRekorURL = "https://rekor.sigstore.dev"

// Result contains the outcome of verifying a signed artifact.
type Result struct {
	// Signed is true when a signature was found and verified.
	Signed bool
	// SignerIdentity is the certificate's subject identity (workflow path
	// for GitHub-Actions-issued certificates, SAN otherwise). Empty for
	// key-signed artifacts, which carry no certificate.
	SignerIdentity string
	// CertIssuer is the OIDC issuer that authenticated the signer. Empty
	// for key-signed artifacts.
	CertIssuer string
	// RepositoryURI is the source repository from the certificate, if any.
	RepositoryURI string
	// RepositoryRef is the git ref the signing workflow ran on, from the
	// certificate's Fulcio extensions. Empty when the certificate carries no
	// such extension (signers outside CI, or gitsign from a personal OIDC
	// identity).
	RepositoryRef string
	// RunnerEnvironment is the runner class the signing workflow executed in
	// (e.g. "github-hosted"), from the certificate's Fulcio extensions.
	// Empty when the certificate carries no such extension.
	RunnerEnvironment string
	// SigstoreURL is the transparency log instance used for verification.
	SigstoreURL string
	// Provisional marks a verification with a documented assurance gap
	// (git signatures until Rekor proof validation lands).
	Provisional bool
	// Bundle is the serialized Sigstore bundle for offline re-verification.
	Bundle []byte
}

// ToLockProvenance converts a verification result to a lock file provenance
// block. Key-signed results have no certificate identity and yield nil —
// the lock file records provenance only for identity-bearing signatures.
func (r *Result) ToLockProvenance() *lockfile.Provenance {
	if r == nil || !r.Signed || r.SignerIdentity == "" {
		return nil
	}
	return &lockfile.Provenance{
		SignerIdentity:    r.SignerIdentity,
		CertIssuer:        r.CertIssuer,
		RepositoryURI:     r.RepositoryURI,
		RepositoryRef:     r.RepositoryRef,
		RunnerEnvironment: r.RunnerEnvironment,
		SigstoreURL:       r.SigstoreURL,
		Provisional:       r.Provisional,
	}
}

// expectedIdentity converts a lock provenance block into the core Identity
// that gets bound into the Sigstore verification policy. A nil provenance
// (trust on first use) yields nil, which core treats as chain-of-trust-only
// verification.
//
// The recorded RepositoryRef and RunnerEnvironment are deliberately absent:
// core's Identity cannot express them (and its SAN policy matches any ref),
// so they are enforced against the certificate after the policy has passed —
// see checkPinnedCertificateFields.
func expectedIdentity(p *lockfile.Provenance) *coreverifier.Identity {
	if p == nil {
		return nil
	}
	return &coreverifier.Identity{
		SignerIdentity:      p.SignerIdentity,
		CertIssuer:          p.CertIssuer,
		SourceRepositoryURI: p.RepositoryURI,
	}
}

// resultFromKey builds a Result for a key-signed artifact. It carries no
// certificate identity and no SigstoreURL — the key flow writes no
// transparency-log entry, and recording an instance the signature never
// touched would fabricate provenance.
func resultFromKey(raw []byte) *Result {
	return &Result{
		Signed: true,
		Bundle: raw,
	}
}

// resultFromCore builds a Result from an observed certificate identity.
func resultFromCore(observed observedCertificate, raw []byte) *Result {
	return &Result{
		Signed:            true,
		SignerIdentity:    observed.SignerIdentity,
		CertIssuer:        observed.CertIssuer,
		RepositoryURI:     observed.SourceRepositoryURI,
		RepositoryRef:     observed.RepositoryRef,
		RunnerEnvironment: observed.RunnerEnvironment,
		SigstoreURL:       sigstorePublicGoodRekorURL,
		Bundle:            raw,
	}
}

// observedCertificate is the signer identity a verification observed: core's
// normalized Identity plus the Fulcio extensions core's Identity does not
// carry — the git ref the signing workflow ran on and the runner class it
// executed in.
type observedCertificate struct {
	coreverifier.Identity
	RepositoryRef     string
	RunnerEnvironment string
}

// observedFromResult extracts the observed certificate identity from a
// successful verification result.
func observedFromResult(vr *verify.VerificationResult) (observedCertificate, error) {
	identity, err := coreverifier.IdentityFromResult(vr)
	if err != nil {
		return observedCertificate{}, err
	}
	// IdentityFromResult already rejected a result without a certificate
	// summary, so the Fulcio extensions are reachable here.
	cert := vr.Signature.Certificate
	return observedCertificate{
		Identity:          identity,
		RepositoryRef:     cert.SourceRepositoryRef,
		RunnerEnvironment: cert.RunnerEnvironment,
	}, nil
}

// checkPinnedCertificateFields enforces the certificate fields the lock file
// pins that no Sigstore policy can express: the git ref the signing workflow
// ran on and the runner class it executed in. Without this, provenance
// recorded for "this workflow in this repository" is satisfied by the same
// workflow run from any branch or on a self-hosted runner.
//
// An empty expected field is unconstrained, not a wildcard mismatch: every
// lock entry written before these fields were recorded has them empty, and
// certificates from signers outside GitHub Actions carry no such extensions
// at all. Both must keep verifying.
func checkPinnedCertificateFields(observed observedCertificate, expected *lockfile.Provenance) error {
	if expected == nil {
		return nil
	}
	if expected.RepositoryRef != "" && observed.RepositoryRef != expected.RepositoryRef {
		return pinnedFieldMismatch("repository ref", expected.RepositoryRef, observed.RepositoryRef)
	}
	if expected.RunnerEnvironment != "" && observed.RunnerEnvironment != expected.RunnerEnvironment {
		return pinnedFieldMismatch("runner environment", expected.RunnerEnvironment, observed.RunnerEnvironment)
	}
	return nil
}

// pinnedFieldMismatch builds the ErrSignerMismatch error for a certificate
// field that differs from the pinned one, naming the field so the operator
// can tell a ref change from a runner-class change.
func pinnedFieldMismatch(field, expected, observed string) error {
	return fmt.Errorf("%w: locked to %s %s, but the artifact's certificate carries %s",
		ErrSignerMismatch, field, strconv.Quote(expected), quotedOrNone(observed))
}

// quotedOrNone renders an observed certificate field for an error message,
// distinguishing a differing value from an absent extension.
func quotedOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return strconv.Quote(value)
}
