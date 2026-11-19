// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package idjag

import "github.com/stacklok/toolhive/pkg/oauthproto"

// IsIDJAG reports whether a JOSE "typ" header value names the ID-JAG type. It
// is true for every RFC 7515 Section 4.1.9 spelling: the comparison ignores
// case and the "application/" prefix is optional.
//
// This is the deny-side predicate. Every access-token validator that shares a
// signing key with an ID-JAG issuer must refuse a token for which it is true.
func IsIDJAG(typ string) bool {
	return oauthproto.IsJOSEType(typ, oauthproto.JWTTypeIDJAG)
}
