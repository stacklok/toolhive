// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import "github.com/stacklok/toolhive/pkg/skills/lockfile"

// Result contains the outcome of verifying a signed artifact.
type Result struct {
	// Signed is true when a signature was found and verified.
	Signed bool
	// SignerIdentity is the Fulcio certificate identity.
	SignerIdentity string
	// CertIssuer is the Fulcio certificate issuer.
	CertIssuer string
	// RepositoryURI is the source repository URI from the certificate, if present.
	RepositoryURI string
	// SigstoreURL is the Rekor instance used for verification.
	SigstoreURL string
	// Bundle is the serialized Sigstore bundle for offline re-verification.
	Bundle []byte
}

// ToLockProvenance converts a verification result to a lock file provenance block.
func (r *Result) ToLockProvenance() *lockfile.Provenance {
	if r == nil || !r.Signed {
		return nil
	}
	return &lockfile.Provenance{
		SignerIdentity: r.SignerIdentity,
		CertIssuer:     r.CertIssuer,
		RepositoryURI:  r.RepositoryURI,
		SigstoreURL:    r.SigstoreURL,
	}
}

// MatchIdentity returns ErrSignerMismatch when expected is set and does not match the result.
func MatchIdentity(result *Result, expected *lockfile.Provenance) error {
	if expected == nil {
		return nil
	}
	if result == nil || !result.Signed {
		return ErrSignatureInvalid
	}
	if expected.SignerIdentity != "" && result.SignerIdentity != expected.SignerIdentity {
		return ErrSignerMismatch
	}
	if expected.CertIssuer != "" && result.CertIssuer != expected.CertIssuer {
		return ErrSignerMismatch
	}
	if expected.RepositoryURI != "" && result.RepositoryURI != expected.RepositoryURI {
		return ErrSignerMismatch
	}
	return nil
}
