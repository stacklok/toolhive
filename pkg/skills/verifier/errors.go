// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"errors"
	"fmt"
)

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
	// ErrKeySigned indicates the artifact carries only cosign key-pair
	// signatures, which install-time verification cannot check: the keyless
	// (Fulcio) trust root has nothing to chain them to, and the signing
	// public key is recoverable neither from the artifact nor from the
	// attached bundle — cosign's manifest defines no annotation carrying it,
	// and the reconstructed bundle holds a fixed placeholder hint in its
	// place.
	//
	// Deliberately NOT wrapping ErrSignatureInvalid, unlike
	// ErrProvenanceFieldMismatch below: the signature may be perfectly
	// valid, so reporting it as a verification failure is precisely the
	// misclassification this sentinel exists to end. That narrowing cannot
	// fail open, because no caller treats ErrSignatureInvalid as permission
	// to proceed — it only selects a failure reason.
	ErrKeySigned = errors.New("artifact is signed with a cosign key pair, which cannot be verified at install time")
	// ErrKeylessSigned is the mirror of ErrKeySigned: every signature on the
	// artifact carries a Fulcio certificate, so a cosign public key is the
	// wrong trust anchor to check it with. Reported when a caller supplies a
	// key for an artifact that was signed keylessly — the likeliest way to
	// reach the key path by mistake, and one whose remedy (drop the key) is
	// invisible in a bare "signature verification failed".
	//
	// Deliberately NOT wrapping ErrSignatureInvalid, for the same reason
	// ErrKeySigned does not: the signature is intact, and there is a real
	// verification path for it.
	ErrKeylessSigned = errors.New("artifact is signed keylessly, not with a cosign key pair")
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

// errKeyPinnedEntry is returned when a lock entry pinned to a cosign public
// key is handed to a keyless verification path. It wraps ErrSignatureInvalid
// so existing callers classify it as a verification failure — which it is,
// the entry cannot be verified this way — while the message names the real
// problem instead of surfacing sigstore's empty-identity complaint.
var errKeyPinnedEntry = fmt.Errorf(
	"%w: entry is pinned to a cosign public key, which the keyless verification path cannot check",
	ErrSignatureInvalid)
