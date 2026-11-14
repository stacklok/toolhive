// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package claims provides shared helpers for decoding JWT/OIDC claim values.
package claims

import (
	"slices"

	oauthclaims "github.com/stacklok/toolhive/pkg/oauthproto/claims"
)

// DecodeStringArray preserves the historical host-package API while the strict
// OAuth claim decoder is owned by pkg/oauthproto/claims.
func DecodeStringArray(raw any) ([]string, error) {
	return oauthclaims.DecodeStringArray(raw)
}

// DecodeStringArrayLenient decodes a claim value using the permissive contract
// the Cedar authorizer has always applied to group/role claims, where a claim
// that is present but partially malformed must still yield whatever usable
// names it carries rather than failing the whole authorization decision.
//
// It differs from DecodeStringArray in three ways, each of which Cedar depends
// on today:
//
//   - non-string elements of a []any are dropped instead of raising an error,
//     so an array of all non-strings decodes to a non-nil empty slice;
//   - a typed nil slice is a recognized array, not an error: []any(nil)
//     decodes to a non-nil empty slice and []string(nil) decodes to nil,
//     preserving each shape's existing nil-ness;
//   - a recognized but empty []any stays non-nil, so a caller can tell "the
//     IdP asserted this claim with no usable values" (which must suppress a
//     fallback to other claim names) from "the claim is absent" (nil).
//
// recognized reports whether raw was a supported array shape at all. A false
// value means the claim holds a scalar, object, or other unsupported type;
// the caller decides whether to log and ignore it. As with DecodeStringArray,
// the result never aliases the input.
//
// New security-relevant callers should prefer DecodeStringArray: dropping
// malformed elements silently is only appropriate where the existing
// behavior must be preserved.
func DecodeStringArrayLenient(raw any) (values []string, recognized bool) {
	switch v := raw.(type) {
	case []string:
		// slices.Clone preserves nil-ness: nil stays nil, non-nil empty stays
		// non-nil empty. Cedar distinguishes the two.
		return slices.Clone(v), true
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	default:
		return nil, false
	}
}
