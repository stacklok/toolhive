// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package fositejose holds the go-jose v3 workarounds that fosite v0.49.0
// needs. ToolHive uses go-jose v4, but fosite exposes go-jose v3 in its
// interfaces. The package has two parts.
//
// The key conversions between go-jose v4 and v3 copy already-parsed keys
// field by field, so key material is never re-parsed by go-jose v3.
//
// The embedded-JWK guard, IsUnsafeJWS, refuses JWS input with an embedded
// key before fosite parses it with go-jose v3. go-jose v3 is affected by
// GHSA-mgcc-cww5-fp2q and has no patched release.
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
// with go-jose v3, which validates an RSA key embedded in a JWS header without
// bounding its size (GHSA-mgcc-cww5-fp2q; no patched v3 exists). An
// unauthenticated caller can use that to burn CPU before fosite looks up any
// client. fosite never uses an embedded key, since it resolves keys from the
// registered client, so refusing them does not affect conforming clients.
//
// The check mirrors how go-jose v3's ParseSigned reads its input: whitespace
// is stripped, input starting with "{" is the JSON serialization (whose
// unprotected headers can also carry a key), and the compact form must have
// exactly three segments. Anything that cannot be decoded as a compact JWS
// header is reported unsafe, so a parsing difference fails closed. fosite
// rejects such input anyway.
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
	rawHeader, _, _ := strings.Cut(token, ".")
	headerJSON, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(rawHeader, "="))
	if err != nil {
		return true
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return true
	}
	for name := range header {
		if strings.EqualFold(name, "jwk") {
			return true
		}
	}
	return false
}
