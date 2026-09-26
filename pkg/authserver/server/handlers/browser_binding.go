// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// Browser binding ties an upstream callback to the user agent that started the
// authorization flow, as RFC 6749 Section 10.12 requires of an OAuth client
// (this server is the OAuth client toward the upstream IdP).
//
// /oauth/authorize mints a random secret, stores its hash on the
// PendingAuthorization, and sets the secret as a cookie on the redirect to the
// upstream. /oauth/callback loads the record by the upstream state and refuses
// to complete the leg unless the calling browser presents a cookie whose hash
// matches. Without this, an attacker who starts a flow for their own client can
// hand the resulting upstream URL to a victim and receive an authorization code
// bound to the victim's identity. Each multi-upstream chain leg mints its own
// secret, since every leg has a fresh state and pending record. The device
// flow's verification page (POST /oauth/device) shares the callback and the
// same shape, so it binds its PendingDeviceLogin the same way.
//
// Cookie shape:
//
//   - One cookie per pending record, named from the state, so concurrent flows in
//     one browser never overwrite each other. Cookies ignore ports, so several
//     local servers on different localhost ports share one jar; a fixed name
//     would collide across them.
//   - The "__Host-" prefix is used whenever the cookie is Secure. Browsers then
//     reject the cookie unless it was set from a secure origin with Path=/ and no
//     Domain, which stops a sibling subdomain or a plain-http origin on the same
//     host from planting a cookie whose value the attacker already knows. A
//     plain-http authorize URL has no browser-enforced defense of this kind, so it
//     gets the unprefixed name.
//   - Secure follows the scheme of the browser-facing authorize base URL, not the
//     inbound connection: pods serve plain HTTP behind a TLS-terminating ingress,
//     and the browser judges Secure against the connection it used.
//   - Path=/ is required by "__Host-" and also survives a prefix-stripping proxy
//     in front of a path-based issuer. It means a browser would also send the
//     cookie on /mcp, where the transparent proxy forwards Cookie to the backend
//     when strip-auth is not installed; the value is one-shot, useless without the
//     matching state, and expires with the pending record, so this is accepted.
//   - SameSite=Lax is sent on the IdP's top-level GET redirect back to
//     /oauth/callback. The callback is GET-only; if response_mode=form_post is
//     ever supported, the cookie would need SameSite=None and Secure.
//
// Only the hash of the secret is stored, so storage never holds the value.

const (
	// browserBindingCookieBase is the cookie name stem; the state-derived suffix
	// and the optional "__Host-" prefix are added by browserBindingCookieName.
	browserBindingCookieBase = "thv_authz_"

	// hostCookiePrefix is the RFC 6265bis cookie-name prefix that browsers only
	// accept for Secure, host-only, Path=/ cookies set from a secure origin.
	hostCookiePrefix = "__Host-"

	// browserBindingNameHashLen is the number of hex characters of
	// SHA-256(state) appended to the cookie name.
	browserBindingNameHashLen = 16
)

// The device flow's verification form (GET /oauth/device renders it, POST
// /oauth/device submits it) carries a double-submit anti-forgery token: the GET
// sets a random cookie (or reuses the one the browser already holds) and embeds
// the same value in a hidden field, and the POST accepts the form only when the
// two match. Without it a cross-site page
// could auto-submit an attacker's user_code from the victim's browser; that
// browser would then be issued the binding cookie above and could complete
// the login as if the victim had typed the code. The cookie has the same shape
// as the binding cookie, so a cross-site POST neither carries it (SameSite=Lax)
// nor can plant it (host-only, "__Host-" when Secure).
const (
	// deviceFormCookieBase is the anti-forgery cookie name stem for the device
	// verification form; one name per host, since the form has no state yet.
	deviceFormCookieBase = "thv_device_form"

	// deviceFormTokenField is the hidden form field that must echo the cookie.
	deviceFormTokenField = "form_token"

	// deviceFormTokenTTL bounds how long a rendered verification form may be
	// submitted; each render refreshes the cookie's Max-Age.
	deviceFormTokenTTL = 10 * time.Minute
)

var (
	errBrowserBindingCookieMissing = errors.New("browser binding cookie missing")
	errBrowserBindingUnbound       = errors.New("pending authorization has no browser binding")
	errBrowserBindingMismatch      = errors.New("browser binding cookie does not match")
	errDeviceFormTokenInvalid      = errors.New("device verification form token missing or mismatched")
)

// browserBinding is a freshly minted binding secret and its stored form.
type browserBinding struct {
	// value goes into the cookie and is never stored.
	value string
	// hash is BASE64URL(SHA256(value)) and is stored on the pending record.
	hash string
}

// newBrowserBinding mints a browser-binding secret.
func newBrowserBinding() browserBinding {
	value := rand.Text()
	return browserBinding{value: value, hash: hashBrowserBindingSecret(value)}
}

// hashBrowserBindingSecret returns the stored form of a cookie value.
func hashBrowserBindingSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// browserBindingSecure reports whether the browser-facing authorize URL is
// served over https, which decides both the Secure attribute and the "__Host-"
// prefix. Config validation guarantees the URL parses with an http or https
// scheme; an unparseable value falls back to the plain-http shape, which every
// browser accepts.
func (h *Handler) browserBindingSecure() bool {
	u, err := url.Parse(h.config.GetAuthorizationEndpointBaseURL())
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "https")
}

// browserBindingCookieName derives the cookie name for one pending record from
// its upstream state. secure selects the "__Host-" prefixed shape. Set and
// verify must agree, so both go through here.
func browserBindingCookieName(internalState string, secure bool) string {
	sum := sha256.Sum256([]byte(internalState))
	name := browserBindingCookieBase + hex.EncodeToString(sum[:])[:browserBindingNameHashLen]
	if secure {
		return hostCookiePrefix + name
	}
	return name
}

// setBrowserBindingCookie writes the binding cookie for internalState onto a
// response that is about to redirect the browser to an upstream IdP.
func (h *Handler) setBrowserBindingCookie(w http.ResponseWriter, internalState, value string) {
	// G124 wants a literal Secure: true; here Secure deliberately follows the
	// configured authorize URL scheme because plain-http loopback issuers are
	// supported and a browser drops a Secure cookie set over plain http.
	secure := h.browserBindingSecure()
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124 - Secure is derived from the authorize URL scheme
		Name:     browserBindingCookieName(internalState, secure),
		Value:    value,
		Path:     "/",
		MaxAge:   int(storage.DefaultPendingAuthorizationTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// verifyBrowserBinding checks that req carries the binding cookie for the
// pending record loaded under internalState, whose stored hash is storedHash.
// Every cookie with the expected name is tried, so a planted duplicate cannot
// shadow the legitimate one. A record with an empty hash (written before
// browser binding existed) is rejected outright rather than treated as
// unbound-and-therefore-valid. The cookie is not cleared after use: the
// pending record is deleted on first use, so the cookie is inert and the
// browser drops it when Max-Age elapses.
func (h *Handler) verifyBrowserBinding(req *http.Request, internalState, storedHash string) error {
	if storedHash == "" {
		return errBrowserBindingUnbound
	}
	name := browserBindingCookieName(internalState, h.browserBindingSecure())
	expected := []byte(storedHash)
	found := false
	for _, c := range req.Cookies() {
		if c.Name != name {
			continue
		}
		found = true
		if subtle.ConstantTimeCompare([]byte(hashBrowserBindingSecret(c.Value)), expected) == 1 {
			return nil
		}
	}
	if !found {
		return errBrowserBindingCookieMissing
	}
	return errBrowserBindingMismatch
}

// deviceFormCookieName is the anti-forgery cookie name for the device
// verification form; secure selects the "__Host-" prefixed shape.
func deviceFormCookieName(secure bool) string {
	if secure {
		return hostCookiePrefix + deviceFormCookieBase
	}
	return deviceFormCookieBase
}

// issueDeviceFormToken returns the anti-forgery token for a render of the
// device verification form and (re)sets it as a cookie. A token the browser
// already holds is reused so that two open forms, in two tabs or on two local
// servers sharing a cookie jar, do not invalidate each other; a fresh one is
// minted only when the browser has none. Reuse is safe: a cross-site page can
// neither read the cookie nor make the browser send it (SameSite=Lax), so
// knowing the token is only possible from this origin.
func (h *Handler) issueDeviceFormToken(w http.ResponseWriter, req *http.Request) string {
	secure := h.browserBindingSecure()
	name := deviceFormCookieName(secure)
	token := ""
	for _, c := range req.Cookies() {
		if c.Name == name && c.Value != "" {
			token = c.Value
			break
		}
	}
	if token == "" {
		token = rand.Text()
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124 - Secure is derived from the authorize URL scheme
		Name:     name,
		Value:    token,
		Path:     "/",
		MaxAge:   int(deviceFormTokenTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

// verifyDeviceFormToken checks that the submitted form's hidden token matches
// an anti-forgery cookie on req. The caller has already parsed the form. Every
// cookie with the expected name is tried, as in verifyBrowserBinding.
func (h *Handler) verifyDeviceFormToken(req *http.Request) error {
	token := req.PostForm.Get(deviceFormTokenField)
	if token == "" {
		return errDeviceFormTokenInvalid
	}
	name := deviceFormCookieName(h.browserBindingSecure())
	for _, c := range req.Cookies() {
		if c.Name == name && subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1 {
			return nil
		}
	}
	return errDeviceFormTokenInvalid
}
