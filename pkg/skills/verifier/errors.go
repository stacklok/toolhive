// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import "errors"

var (
	// ErrUnsigned indicates the artifact has no Sigstore signature.
	ErrUnsigned = errors.New("artifact is not signed")
	// ErrSignatureInvalid indicates signature verification failed.
	ErrSignatureInvalid = errors.New("signature verification failed")
	// ErrSignerMismatch indicates the observed signer does not match the expected identity.
	ErrSignerMismatch = errors.New("signer identity mismatch")
	// ErrBundleNotFound indicates no Sigstore bundle could be discovered.
	ErrBundleNotFound = errors.New("sigstore bundle not found")
)
