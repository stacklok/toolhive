// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ErrInsecureTransport is returned when an identity token would be sent over
// a transport that does not protect it.
var ErrInsecureTransport = errors.New("refusing to send an OIDC identity token over an unencrypted connection")

// CheckTransport reports whether baseURL is a safe destination for an OIDC
// identity token.
//
// The token is a short-lived bearer credential that Fulcio will exchange for
// a signing certificate attributed to its subject, so anyone who observes it
// in flight can sign artifacts as the caller (CWE-319, RFC 6750 §5.1). The
// API base URL is not necessarily local: TOOLHIVE_API_URL can point the CLI
// at an arbitrary host, and plain http:// to a remote host puts the token on
// the wire in cleartext.
//
// HTTPS is accepted, as is plaintext HTTP to a loopback host — the Unix
// socket and named pipe transports both present as "http://localhost", and
// on those the bytes never reach a network interface at all. Everything else
// is refused.
//
// Callers must only invoke this when a token is actually present: an unsigned
// (--no-sign) push over plaintext HTTP carries no credential and is not this
// function's business to block.
func CheckTransport(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid API URL %q: %w", baseURL, err)
	}
	switch u.Scheme {
	case "https", "unix", "npipe":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%w: the ToolHive API at %s is remote and not using TLS. "+
			"Use an https:// URL, a local server, or pass --no-sign to push without a credential",
			ErrInsecureTransport, baseURL)
	default:
		return fmt.Errorf("%w: unsupported API URL scheme %q in %s",
			ErrInsecureTransport, u.Scheme, baseURL)
	}
}

// isLoopbackHost reports whether host names the local machine. Literal
// "localhost" and its reserved subdomains are treated as loopback by name
// (RFC 6761 §6.3) rather than resolved: the Unix socket and named pipe
// transports synthesize "http://localhost" as their base URL, and a resolver
// lookup would make a security decision depend on ambient DNS.
func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// NoRedirectClient returns a shallow copy of base that refuses to follow
// redirects, for requests that carry an identity token in their body.
//
// CheckTransport alone is not enough: it clears the base URL, but an accepted
// HTTPS or loopback endpoint can answer with a 307 or 308, and those preserve
// the method and replay the body — Go sets GetBody automatically for the
// bytes.Reader the JSON encoder produces, so the credential would be re-sent
// to whatever Location names, including a plaintext remote host. Validating
// only the first hop would leave the guard trivially bypassable by whoever
// controls the endpoint the CLI was pointed at.
//
// Refusing outright rather than re-checking each hop: the ToolHive API does
// not redirect its own endpoints, so there is no legitimate case to preserve,
// and "no redirects" is a property that cannot be subtly wrong. The refusal
// happens before the second request is issued, so the token never leaves the
// origin CheckTransport approved.
func NoRedirectClient(base *http.Client) *http.Client {
	guarded := *base
	guarded.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return fmt.Errorf("%w: the ToolHive API redirected this request to %s; "+
			"a push carrying an identity token is not followed across redirects",
			ErrInsecureTransport, req.URL.Redacted())
	}
	return &guarded
}
