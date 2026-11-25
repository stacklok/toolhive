// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package subject

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// ErrInvalidBindingClaim means a configured binding claim cannot identify a
// local user.
var ErrInvalidBindingClaim = errors.New("invalid binding claim")

// oktaDomainSuffixes are the host suffixes of Okta's hosted domains.
var oktaDomainSuffixes = []string{".okta.com", ".oktapreview.com", ".okta-emea.com"}

// mutableClaims are known claim names that users or their administrators can
// change. The list is not exhaustive. unique_name is where Entra v1 tokens
// carry the UPN.
var mutableClaims = []string{
	"email", "preferred_username", "upn", "unique_name",
	"name", "nickname", "given_name", "family_name", "phone_number",
	"mail", "login", "username",
}

// ValidateBindingClaim checks the claim that maps a token's subject to a local
// user, for the token issuer named by issuer. A known mutable claim name (such
// as email, preferred_username, upn, unique_name or name, in any letter case),
// an empty claim, or a claim with surrounding whitespace is refused. A claim
// that passes is not proven stable: prefer sub, oid or uid. A non-empty
// warning means the claim is sub on an Okta custom authorization server; the
// caller decides how to report it.
func ValidateBindingClaim(issuer, claim string) (warning string, err error) {
	if claim == "" {
		return "", fmt.Errorf("%w: the claim name is empty", ErrInvalidBindingClaim)
	}
	if strings.TrimSpace(claim) != claim {
		return "", fmt.Errorf("%w: %q has leading or trailing whitespace", ErrInvalidBindingClaim, claim)
	}
	if slices.ContainsFunc(mutableClaims, func(m string) bool { return strings.EqualFold(m, claim) }) {
		return "", fmt.Errorf("%w: %q is a known mutable claim name; use a stable claim such as sub, oid or uid",
			ErrInvalidBindingClaim, claim)
	}
	if claim == "sub" && isOktaCustomAuthorizationServer(issuer) {
		return "sub on an Okta custom authorization server may not match the sign-in subject; consider uid", nil
	}
	return "", nil
}

// isOktaCustomAuthorizationServer reports whether issuer has the shape of an
// Okta custom authorization server (https://<domain>/oauth2/<id>) on an Okta
// hosted domain. Okta custom domains cannot be recognised and return false.
func isOktaCustomAuthorizationServer(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	if !strings.HasPrefix(u.Path, "/oauth2/") {
		return false
	}
	return slices.ContainsFunc(oktaDomainSuffixes, func(suffix string) bool {
		return strings.HasSuffix(strings.ToLower(u.Hostname()), suffix)
	})
}
