// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import "errors"

var (
	// ErrUnsigned indicates the artifact carries no Sigstore signature
	// material in any supported layout.
	ErrUnsigned = errors.New("artifact is not signed")
	// ErrSignatureInvalid indicates signature material was found but failed
	// cryptographic verification (or a stored bundle is malformed).
	ErrSignatureInvalid = errors.New("signature verification failed")
	// ErrSignerMismatch indicates the signature verifies, but against an
	// identity other than the expected one.
	ErrSignerMismatch = errors.New("signer identity mismatch")
	// ErrProvenanceFieldMismatch indicates the signature verifies against
	// the expected signer identity and issuer, but a certificate field the
	// Sigstore policy cannot itself express — the repository ref or runner
	// environment — differs from what is pinned. This is a NARROWER claim
	// than ErrSignerMismatch: the signer itself did not change, only one of
	// these additional certificate fields did. An error produced for this
	// reason satisfies errors.Is against BOTH sentinels (see
	// pinnedFieldMismatch), so existing callers checking only
	// ErrSignerMismatch keep working — the same --allow-signer-change
	// override remains the correct remediation for either cause — while a
	// caller that wants to tell them apart (e.g. to explain that a version
	// bump's ref changed, rather than its publisher) can check this one
	// specifically.
	ErrProvenanceFieldMismatch = errors.New("certificate provenance field mismatch")
)
