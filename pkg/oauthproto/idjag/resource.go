// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package idjag

// ResourceClaim reads the "resource" claim of an ID-JAG.
//
// values holds the claim's strings: a string gives a one-element slice, an
// array gives its string elements (other elements are skipped), and any other
// value gives nil.
//
// ok is true only when the claim is well formed under draft-ietf-oauth-
// identity-assertion-authz-grant-06 section 3: a non-empty string, or a
// non-empty array whose elements are all non-empty strings with no duplicates.
//
// ResourceClaim cannot tell an absent claim from a malformed one. A caller
// that needs that distinction checks the claims map for the key itself (the
// comma-ok form). A JSON null counts as present but malformed.
func ResourceClaim(claim any) (values []string, ok bool) {
	switch v := claim.(type) {
	case string:
		return []string{v}, v != ""
	case []any:
		ok = len(v) > 0
		seen := make(map[string]struct{}, len(v))
		for _, e := range v {
			s, isString := e.(string)
			if !isString {
				ok = false
				continue
			}
			values = append(values, s)
			if _, dup := seen[s]; dup || s == "" {
				ok = false
			}
			seen[s] = struct{}{}
		}
		return values, ok
	}
	return nil, false
}
