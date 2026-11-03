// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package fositejose converts JSON Web Keys between go-jose v4, which ToolHive
// uses, and go-jose v3, which fosite v0.49.0 exposes in its interfaces.
//
// go-jose v3 is affected by GHSA-mgcc-cww5-fp2q and has no patched release.
// The conversions copy already-parsed keys field by field, so key material is
// never re-parsed by go-jose v3. Remove this package once fosite moves to
// go-jose v4.
package fositejose

import (
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
