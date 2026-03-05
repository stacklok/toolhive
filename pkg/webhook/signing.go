// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Header names for webhook HMAC signing.
const (
	// SignatureHeader is the HTTP header containing the HMAC signature.
	SignatureHeader = "X-ToolHive-Signature"
	// TimestampHeader is the HTTP header containing the Unix timestamp.
	TimestampHeader = "X-ToolHive-Timestamp"
)

// signaturePrefix is the prefix for the HMAC-SHA256 signature value.
const signaturePrefix = "sha256="

// SignPayload computes an HMAC-SHA256 signature over the given timestamp and
// payload. The signature is computed over the string "timestamp.payload" and
// returned in the format "sha256=<hex-encoded-signature>".
func SignPayload(secret []byte, timestamp int64, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	// Write the message: "timestamp.payload"
	msg := fmt.Sprintf("%d.", timestamp)
	mac.Write([]byte(msg))
	mac.Write(payload)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature verifies an HMAC-SHA256 signature against the given timestamp
// and payload. The signature should be in the format "sha256=<hex-encoded-signature>".
// Comparison is done in constant time to prevent timing attacks.
func VerifySignature(secret []byte, timestamp int64, payload []byte, signature string) bool {
	if !strings.HasPrefix(signature, signaturePrefix) {
		return false
	}

	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, signaturePrefix))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	msg := fmt.Sprintf("%d.", timestamp)
	mac.Write([]byte(msg))
	mac.Write(payload)

	return hmac.Equal(mac.Sum(nil), sigBytes)
}
