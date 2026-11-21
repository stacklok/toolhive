// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package fositejose holds the go-jose v3 workarounds that fosite v0.49.0
// needs. ToolHive uses go-jose v4, but fosite exposes go-jose v3 in its
// interfaces. The package has two parts.
//
// The key conversions between go-jose v4 and v3 copy already-parsed keys
// field by field, so key material is never re-parsed by go-jose v3.
//
// The JWS guard, IsUnsafeJWS, refuses JWS input before fosite parses it with
// go-jose v3, which has no patched release for two advisories:
// GHSA-mgcc-cww5-fp2q (an embedded key in the header) and GHSA-vvm8-gf78-28g4
// (unbounded JSON nesting in the header or payload).
//
// Remove this package once fosite moves to go-jose v4.
package fositejose

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v4"
)

// ToFositeKey copies key into the go-jose v3 type fosite expects.
func ToFositeKey(key *jose.JSONWebKey) *josev3.JSONWebKey {
	if key == nil {
		return nil
	}
	return &josev3.JSONWebKey{
		Key:                         key.Key,
		KeyID:                       key.KeyID,
		Algorithm:                   key.Algorithm,
		Use:                         key.Use,
		Certificates:                key.Certificates,
		CertificatesURL:             key.CertificatesURL,
		CertificateThumbprintSHA1:   key.CertificateThumbprintSHA1,
		CertificateThumbprintSHA256: key.CertificateThumbprintSHA256,
	}
}

// ToFositeKeySet copies jwks into the go-jose v3 type fosite expects.
func ToFositeKeySet(jwks *jose.JSONWebKeySet) *josev3.JSONWebKeySet {
	if jwks == nil {
		return nil
	}
	keys := make([]josev3.JSONWebKey, 0, len(jwks.Keys))
	for i := range jwks.Keys {
		keys = append(keys, *ToFositeKey(&jwks.Keys[i]))
	}
	return &josev3.JSONWebKeySet{Keys: keys}
}

// FromFositeKeySet copies a key set read from a fosite client into the
// go-jose v4 type.
func FromFositeKeySet(jwks *josev3.JSONWebKeySet) *jose.JSONWebKeySet {
	if jwks == nil {
		return nil
	}
	keys := make([]jose.JSONWebKey, 0, len(jwks.Keys))
	for _, k := range jwks.Keys {
		keys = append(keys, jose.JSONWebKey{
			Key:                         k.Key,
			KeyID:                       k.KeyID,
			Algorithm:                   k.Algorithm,
			Use:                         k.Use,
			Certificates:                k.Certificates,
			CertificatesURL:             k.CertificatesURL,
			CertificateThumbprintSHA1:   k.CertificateThumbprintSHA1,
			CertificateThumbprintSHA256: k.CertificateThumbprintSHA256,
		})
	}
	return &jose.JSONWebKeySet{Keys: keys}
}

// IsUnsafeJWS reports whether token must be refused before fosite
// parses it.
//
// fosite v0.49.0 parses client assertions and OpenID Connect request objects
// with go-jose v3 before it verifies their signatures, so an unauthenticated
// caller reaches two go-jose v3 defects that have no patched v3 release:
//
//   - GHSA-mgcc-cww5-fp2q: go-jose v3 validates an RSA key embedded in a JWS
//     header without bounding its size, which burns CPU before fosite looks up
//     any client. A header with a "jwk" member is unsafe. fosite never uses an
//     embedded key, since it resolves keys from the registered client, so
//     refusing them does not affect conforming clients.
//   - GHSA-vvm8-gf78-28g4: go-jose v3 decodes JSON with its own decoder, which
//     recurses once per nesting level with no limit. A deeply nested header or
//     payload exhausts the goroutine stack, which is a fatal error rather than
//     a panic, so it stops the process. The header and payload must both be
//     JSON that encoding/json accepts, and encoding/json refuses nesting
//     deeper than 10000 levels, far below the depth that overflows go-jose v3.
//     A JWS header and JWT claims are JSON objects, so conforming tokens pass.
//
// The check mirrors how go-jose v3's ParseSigned reads its input: whitespace
// is stripped, input starting with "{" is the JSON serialization (whose
// unprotected headers can also carry a key), the compact form must have
// exactly three segments, and each segment is base64url with optional
// padding. Anything that cannot be decoded is reported unsafe, so a parsing
// difference fails closed. fosite rejects such input anyway.
//
// Remove this once fosite moves to go-jose v4.1.6 or later.
func IsUnsafeJWS(token string) bool {
	token = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, token)
	if strings.HasPrefix(token, "{") || strings.Count(token, ".") != 2 {
		return true
	}
	rawHeader, rest, _ := strings.Cut(token, ".")
	rawPayload, _, _ := strings.Cut(rest, ".")
	headerJSON, err := decodeSegment(rawHeader)
	if err != nil {
		return true
	}
	// Unmarshal also enforces encoding/json's nesting limit on the header.
	var header map[string]json.RawMessage
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return true
	}
	for name := range header {
		if strings.EqualFold(name, "jwk") {
			return true
		}
	}
	payloadJSON, err := decodeSegment(rawPayload)
	if err != nil {
		return true
	}
	return !json.Valid(payloadJSON)
}

// decodeSegment decodes a compact JWS segment the way go-jose v3 does:
// base64url with any trailing padding removed.
func decodeSegment(segment string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(segment, "="))
}
