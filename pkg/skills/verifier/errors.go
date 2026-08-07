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
)
