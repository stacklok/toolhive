// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
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
	// SigstoreURL is the transparency log instance used for verification.
	SigstoreURL string
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
		SignerIdentity: r.SignerIdentity,
		CertIssuer:     r.CertIssuer,
		RepositoryURI:  r.RepositoryURI,
		SigstoreURL:    r.SigstoreURL,
	}
}

// expectedIdentity converts a lock provenance block into the core Identity
// that gets bound into the Sigstore verification policy. A nil provenance
// (trust on first use) yields nil, which core treats as chain-of-trust-only
// verification.
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

// resultFromCore builds a Result from a core verification outcome.
func resultFromCore(identity coreverifier.Identity, raw []byte) *Result {
	return &Result{
		Signed:         true,
		SignerIdentity: identity.SignerIdentity,
		CertIssuer:     identity.CertIssuer,
		RepositoryURI:  identity.SourceRepositoryURI,
		SigstoreURL:    sigstorePublicGoodRekorURL,
		Bundle:         raw,
	}
}
