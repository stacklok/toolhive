// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/go-jose/go-jose/v4"
)

// paddedJWKMembers are the JWK members that go-jose v3 stripped trailing "="
// from before base64url-decoding: the byte-buffer members of rawJSONWebKey and
// the x5t thumbprints. go-jose v4 rejects padded values, so UnmarshalJWKS
// restores that leniency. x5c is standard base64, where padding is legitimate,
// and is deliberately absent.
var paddedJWKMembers = []string{
	"k", "x", "y", "n", "e", "d", "p", "q", "dp", "dq", "qi", "x5t", "x5t#S256",
}

// UnmarshalJWKS parses a JSON Web Key Set with go-jose v4 after stripping
// trailing "=" padding from the base64url members that go-jose v3 used to
// accept padded. Clients and operators that produced padded JWKS for a
// v3-based release keep working. Members that carry no padding, and every
// other member, are passed through unchanged.
func UnmarshalJWKS(data []byte) (*jose.JSONWebKeySet, error) {
	var raw struct {
		Keys []map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, key := range raw.Keys {
		for name, value := range key {
			if !isPaddedJWKMember(name) {
				continue
			}
			var s string
			if err := json.Unmarshal(value, &s); err != nil {
				continue // not a string; go-jose reports the type error
			}
			t := strings.TrimRight(s, "=")
			if t == s {
				// Also skips JSON null, which unmarshals to "" without error
				// and would otherwise become a non-nil empty byte buffer.
				continue
			}
			trimmed, err := json.Marshal(t)
			if err != nil {
				return nil, fmt.Errorf("re-encode jwk member %q: %w", name, err)
			}
			key[name] = trimmed
		}
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-encode jwks: %w", err)
	}
	var jwks jose.JSONWebKeySet
	if err := json.Unmarshal(normalized, &jwks); err != nil {
		return nil, err
	}
	return &jwks, nil
}

// isPaddedJWKMember matches names exactly: go-jose v4 decodes members with a
// case-sensitive JSON fork, so "N" is not the modulus.
func isPaddedJWKMember(name string) bool {
	return slices.Contains(paddedJWKMembers, name)
}

// UnmarshalJSON decodes the request, accepting trailing "=" padding in the
// base64url members of an inline "jwks" as go-jose v3 did. It is the inbound
// registration path's only use; the field type is unchanged so the outbound
// client is unaffected.
func (r *DynamicClientRegistrationRequest) UnmarshalJSON(data []byte) error {
	type plain DynamicClientRegistrationRequest
	aux := struct {
		*plain
		JWKS json.RawMessage `json:"jwks,omitempty"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.JWKS = nil
	if len(aux.JWKS) == 0 || string(aux.JWKS) == "null" {
		return nil
	}
	jwks, err := UnmarshalJWKS(aux.JWKS)
	if err != nil {
		return err
	}
	r.JWKS = jwks
	return nil
}
