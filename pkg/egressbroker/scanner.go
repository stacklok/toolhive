// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	envoycore "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

// LeakLocation identifies where the echoed credential was found.
type LeakLocation string

const (
	// LeakLocationHeader means a response header carried the injected credential.
	LeakLocationHeader LeakLocation = "header"
	// LeakLocationBody means the response body carried the injected credential.
	LeakLocationBody LeakLocation = "body"
)

// ScannerBounds are the response-scanner limits.
type ScannerBounds struct {
	// MaxBodyBytes caps the buffered body actually scanned (default 1 MiB).
	// The cap is enforced HERE, in-band: Envoy's ext_proc filter has no
	// per-filter byte cap (no max_bytes / allow_partial_message field exists),
	// so Envoy buffers the whole response body and this bound refuses to scan
	// past the cap (a cost bound) and records a skip — headers are always
	// scanned. The cap is NOT a security boundary: the allowlist + destination
	// binding are (ADR-0001 §5).
	MaxBodyBytes int64
}

// scanResult is the scanner's verdict for one response part.
type scanResult int

const (
	// scanClean: no credential material found; the response passes untouched.
	scanClean scanResult = iota
	// scanLeak: the injected credential (exact or base64-encoded) was found;
	// the response must be suppressed.
	scanLeak
	// scanOversize: the body exceeded the scan cap and was NOT scanned
	// (headers still were). Not a leak verdict.
	scanOversize
)

// buildNeedles derives the byte sequences scanned for from a recorded token
// hash. The scan compares exact + base64 encodings of the INJECTED credential
// header value; the raw value is recoverable here only in the sense that the
// broker itself injected it (the map retains hashes, so the scanner needs the
// header value back — see recordNeedles: the hash map entry carries the
// needles, not the token).
//
// IMPORTANT: needles are computed at record time (injection), when the
// plaintext header value is legitimately in scope, and retained alongside the
// hash — the scanner never re-derives them from a credential store.
func buildNeedles(headerValue string) [][]byte {
	b64 := base64.StdEncoding.EncodeToString([]byte(headerValue))
	return [][]byte{[]byte(headerValue), []byte(b64)}
}

// scanHeaders looks for any needle in any response header key or value. The
// matched needle is never reported, logged, or returned.
func scanHeaders(headers *envoycore.HeaderMap, needles [][]byte) bool {
	if headers == nil {
		return false
	}
	for _, hv := range headers.GetHeaders() {
		for _, n := range needles {
			if bytes.Contains([]byte(hv.GetKey()), n) ||
				bytes.Contains([]byte(hv.GetValue()), n) ||
				containsRawValue(hv, n) {
				return true
			}
		}
	}
	return false
}

// containsRawValue checks the envoy HeaderValue raw form (present when the
// value is not valid UTF-8).
func containsRawValue(hv *envoycore.HeaderValue, needle []byte) bool {
	raw := hv.GetRawValue()
	return len(raw) > 0 && bytes.Contains(raw, needle)
}

// scanBody looks for any needle in a buffered response body. Bodies larger
// than maxBytes are refused (scanOversize): Envoy buffers the whole body
// (ext_proc has no byte cap), so the cap is enforced here — never scan past
// it (cost bound).
func scanBody(body []byte, needles [][]byte, maxBytes int64) scanResult {
	if maxBytes <= 0 {
		return scanOversize
	}
	if int64(len(body)) > maxBytes {
		return scanOversize
	}
	for _, n := range needles {
		if bytes.Contains(body, n) {
			return scanLeak
		}
	}
	return scanClean
}

// hashOf is the SHA-256 the token map stores (kept here so the scanner and
// the injector share exactly one hash definition).
func hashOf(headerValue string) [32]byte {
	return sha256.Sum256([]byte(headerValue))
}

// validateScannerBounds fails loudly on a nonsensical scan cap (a zero/negative
// cap would silently disable body scanning entirely).
func validateScannerBounds(b ScannerBounds) error {
	if b.MaxBodyBytes <= 0 {
		return fmt.Errorf("egressbroker: scanner body cap must be positive")
	}
	return nil
}
