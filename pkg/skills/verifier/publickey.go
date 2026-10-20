// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// publicKeyPEMType is the PEM label a cosign public key carries. Cosign's
// private keys use their own labels, which is what makes checking this a
// cheap guard against encoding one by mistake.
const publicKeyPEMType = "PUBLIC KEY"

// EncodePublicKey converts a cosign public key file's PEM contents into the
// single-line base64 DER SPKI form the API and the lock file carry. The lock
// file's provenance values must be graphic, whitespace-free strings, so PEM's
// armor and line breaks cannot be stored verbatim.
//
// This runs on the client side of the API deliberately: sending the file's
// path instead would name nothing on a server that is a different process, or
// on a different host, a different file entirely.
func EncodePublicKey(pemBytes []byte) (string, error) {
	block, rest := pem.Decode(pemBytes)
	if block == nil {
		return "", errors.New("no PEM block found; expected a cosign public key file (cosign.pub)")
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return "", errors.New("file contains more than one PEM block; expected exactly one public key")
	}
	// Checked before parsing, so a private key is refused by its own label
	// rather than by whatever a PKIX parse makes of its bytes. The encoded
	// result is sent to the server and written to the lock file, and neither
	// is somewhere private key material should be able to reach by accident.
	if block.Type != publicKeyPEMType {
		return "", fmt.Errorf("PEM block is %q, expected %q; this flag takes the cosign public key,"+
			" not the signing key", block.Type, publicKeyPEMType)
	}
	if _, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil {
		return "", fmt.Errorf("not a DER SPKI public key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(block.Bytes)
	// Rejected here rather than at the lock write, where the install has
	// already fetched and verified the artifact and the error would arrive
	// with nothing left to do about it.
	if len(encoded) > lockfile.MaxEncodedPublicKeyLength {
		return "", fmt.Errorf("encoded public key is %d characters, exceeding the %d a lock entry accepts",
			len(encoded), lockfile.MaxEncodedPublicKeyLength)
	}
	return encoded, nil
}

// DecodePublicKey converts the base64 DER SPKI form back into the PEM
// encoding the key verification APIs take. It re-validates rather than
// trusting its input: the value reaches here from an HTTP request body or a
// hand-editable lock file, and it is the only trust anchor the artifact will
// be checked against.
func DecodePublicKey(encoded string) ([]byte, error) {
	// Bounded before decoding, so the allocation is capped before it is made
	// rather than after.
	if len(encoded) > lockfile.MaxEncodedPublicKeyLength {
		return nil, fmt.Errorf("public key is %d characters, exceeding the %d maximum",
			len(encoded), lockfile.MaxEncodedPublicKeyLength)
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("public key is not valid base64: %w", err)
	}
	if _, err := x509.ParsePKIXPublicKey(der); err != nil {
		return nil, fmt.Errorf("public key is not a DER SPKI public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: publicKeyPEMType, Bytes: der}), nil
}
