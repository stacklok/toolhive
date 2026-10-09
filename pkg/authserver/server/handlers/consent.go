// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ory/fosite"
	"golang.org/x/net/idna"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

type consentPageData struct {
	Handle, ActionURL, Destination, Summary, RedirectURI, ClientID, ClientName, Resource, Upstream string
	Scopes                                                                                         []string
	CanRemember                                                                                    bool
	RememberFor                                                                                    string
}

// consentStyles extends pageStyles for the consent page only; it is served as a
// second <style> element with its own CSP hash.
const consentStyles = `
details{margin-top:1rem}
summary{cursor:pointer; color:var(--muted); font-size:0.9rem; font-weight:500}
.subtitle.summary{margin-bottom:0.6rem}
.remember{margin-top:1.25rem; padding-top:0.75rem; border-top:1px solid var(--border)}
.remember label{
  display:flex; align-items:center; gap:0.6rem; min-height:2.75rem; cursor:pointer;
  font-size:0.9rem; font-weight:500;
}
.remember input{width:1.1rem; height:1.1rem; margin:0; flex:none; accent-color:var(--brand)}
.remember input:focus-visible{outline:2px solid var(--brand); outline-offset:2px}
.remember p{margin:0 0 0 1.7rem; color:var(--muted); font-size:0.8rem; line-height:1.4}
.remember + .btn-row{margin-top:1rem}
`

// formatDays renders d as whole days, falling back to hours when it is not a whole number of days.
func formatDays(d time.Duration) string {
	unit, n := "day", int(d/(24*time.Hour))
	if d%(24*time.Hour) != 0 {
		unit, n = "hour", int(d/time.Hour)
	}
	if n != 1 {
		unit += "s"
	}
	return strconv.Itoa(n) + " " + unit
}

const maxConsentClientNameRunes = 80

var consentPageTemplate = template.Must(template.New("consent").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign-in request &middot; ToolHive</title><style>` + pageStyles + `</style><style>` + consentStyles + `</style></head><body>
<div class="card"><div class="card-body">
<h1>Allow this app to sign you in?</h1>
<p class="subtitle summary">{{.Summary}}</p>
<p class="subtitle">You will sign in with {{.Upstream}} next.</p>
<dl><dt>Sends your sign-in to:</dt><dd><strong>{{.Destination}}</strong></dd>
<dt>Name given by the app (not verified):</dt><dd>{{.ClientName}}</dd></dl>
<details><summary>Technical details</summary><dl>
<dt>Full redirect URI</dt><dd>{{.RedirectURI}}</dd>
<dt>Client ID</dt><dd>{{.ClientID}}</dd>
<dt>Requested access</dt><dd>{{range .Scopes}}<span class="scope-chip">{{.}}</span>{{end}}</dd>
<dt>Resource</dt><dd>{{.Resource}}</dd><dt>Identity provider (internal name)</dt><dd>{{.Upstream}}</dd></dl></details>
<form method="POST" action="{{.ActionURL}}"><input type="hidden" name="handle" value="{{.Handle}}">
{{if .CanRemember}}<div class="remember"><label>
<input type="checkbox" name="remember" value="1" aria-describedby="remember-help">
<span>Don't ask again for this app on this browser</span></label>
<p id="remember-help">Skips this page for {{.RememberFor}} when the same app asks for the same access.</p></div>{{end}}
<div class="btn-row"><button class="btn-secondary" type="submit" name="decision" value="deny">Deny</button>
<button class="btn-primary" type="submit" name="decision" value="approve">Approve</button></div></form>
</div></div></body></html>`))

// classifyRedirect describes where the sign-in is sent without presenting a
// custom-scheme authority as a trusted destination. Anything that cannot be
// classified is shown as the full URI so a human can still inspect it.
func classifyRedirect(redirectURI string) (destination, summary string) {
	fallback := func() (string, string) {
		return redirectURI, "An app wants to receive your sign-in at the address shown below. " +
			"Only continue if you just started this from that app or site."
	}
	u, err := url.Parse(redirectURI)
	if err != nil || u.Scheme == "" {
		return fallback()
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "an app that opens " + scheme + ":// links",
			"An app on this device that opens " + scheme + ":// links wants to receive your sign-in. " +
				"Only continue if you just started that app."
	}
	host := u.Hostname()
	if host == "" {
		return fallback()
	}
	if ip, err := netip.ParseAddr(host); strings.EqualFold(host, "localhost") || (err == nil && ip.IsLoopback()) {
		return "this computer", "An app running on this computer wants to receive your sign-in. " +
			"Only continue if you just started that app."
	}
	destination = host
	// Show the punycode form too, so a lookalike IDN host cannot pass as a legitimate one.
	if ascii, err := idna.Lookup.ToASCII(host); err == nil && !strings.EqualFold(ascii, host) {
		destination = host + " (" + ascii + ")"
	}
	return destination, "An app wants to receive your sign-in at " + destination + ". " +
		"Only continue if you just started this from that app or site."
}

// displayClientName drops control and format runes (bidi overrides, zero-width
// characters, newlines) from an attacker-controlled name before it is shown.
func displayClientName(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
}

func (h *Handler) renderConsent(
	w http.ResponseWriter, handle string, pending *storage.PendingAuthorization, client fosite.Client,
) {
	destination, summary := classifyRedirect(pending.RedirectURI)
	clientName := displayClientName(registration.ClientName(client))
	if clientName == "" {
		clientName = "No name given"
	} else if r := []rune(clientName); len(r) > maxConsentClientNameRunes {
		clientName = string(r[:maxConsentClientNameRunes]) + "…"
	}
	data := consentPageData{
		Handle: handle, ActionURL: h.config.GetAuthorizationEndpointBaseURL() + "/oauth/consent",
		Destination: destination, Summary: summary, RedirectURI: pending.RedirectURI,
		ClientID: client.GetID(), ClientName: clientName, Resource: pending.Resource,
		Upstream: pending.UpstreamProviderName, Scopes: pending.Scopes, CanRemember: h.rememberedStorage != nil,
		RememberFor: formatDays(storage.ConsentSessionTTL),
	}
	pageHash, consentHash := sha256.Sum256([]byte(pageStyles)), sha256.Sum256([]byte(consentStyles))
	setHTMLSecurityHeaders(w)
	w.Header().Set("Referrer-Policy", "same-origin")
	// A form-action restriction also applies to the POST's 302/303 target. Approval
	// navigates to the configured IdP and denial to the validated client URI
	// (including dynamic loopback ports), so 'self' would block both. The only
	// form target is the escaped, configured same-origin URL; base-uri 'none',
	// no scripts, frame-ancestors 'none', and the POST's Origin/binding checks
	// protect the submission. No form-action directive is set because it
	// would also restrict the validated cross-origin redirect after the POST.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'sha256-"+
		base64.StdEncoding.EncodeToString(pageHash[:])+"' 'sha256-"+
		base64.StdEncoding.EncodeToString(consentHash[:])+"'; frame-ancestors 'none'; base-uri 'none'")
	if err := consentPageTemplate.Execute(w, data); err != nil {
		slog.Error("failed to render consent page", "error", err)
	}
}

// ConsentHandler handles the browser-bound decision before redirecting upstream.
func (h *Handler) ConsentHandler(w http.ResponseWriter, req *http.Request) {
	wantOrigin, err := expectedBrowserOrigin(h.config.GetAuthorizationEndpointBaseURL())
	origins := req.Header.Values("Origin")
	if err != nil || len(origins) != 1 || origins[0] != wantOrigin {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	values, ok := parseConsentForm(w, req, h.rememberedStorage != nil)
	if !ok {
		return
	}
	handle := values.Get("handle")
	pending, err := h.storage.LoadPendingAuthorization(req.Context(), handle)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrExpired) {
			http.Error(w, "authorization request not found or expired", http.StatusBadRequest)
		} else {
			slog.Error("failed to load pending authorization", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	if !h.validateAndConsumePending(req.Context(), w, req, handle, pending, storage.ConsentStageAwaiting) {
		return
	}
	ar := h.buildAuthorizeRequesterFromPending(req.Context(), pending)
	if ar == nil || ar.GetClient() == nil {
		http.Error(w, "authorization request data corrupted", http.StatusInternalServerError)
		return
	}
	if values.Get("decision") == "deny" {
		h.provider.WriteAuthorizeError(req.Context(), w, ar, fosite.ErrAccessDenied)
		return
	}
	approved := *pending
	approved.ConsentStage = storage.ConsentStageApproved
	approved.RememberConsent = h.rememberedStorage != nil && values.Get("remember") == "1"
	h.beginUpstream(req.Context(), w, req, ar, &approved)
}

// expectedBrowserOrigin derives the browser's serialized origin solely from the
// trusted base URL, including compressed IPv6, ASCII domain names, and numeric ports.
func expectedBrowserOrigin(baseURL string) (string, error) {
	errInvalid := errors.New("invalid origin")
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return "", errInvalid
	}
	host, err := serializeOriginHost(base.Hostname())
	if err != nil {
		return "", err
	}
	if base.Port() != "" {
		port, err := strconv.ParseUint(base.Port(), 10, 16)
		if err != nil {
			return "", errInvalid
		}
		if (base.Scheme != "http" || port != 80) && (base.Scheme != "https" || port != 443) {
			host += ":" + strconv.FormatUint(port, 10)
		}
	}
	return (&url.URL{Scheme: base.Scheme, Host: host}).String(), nil
}

// parseConsentForm reads the size-limited consent form, requiring a handle and an
// approve/deny decision, plus remember=1 only when remembered consent is enabled.
// It writes the error response itself.
func parseConsentForm(w http.ResponseWriter, req *http.Request, allowRemember bool) (url.Values, bool) {
	req.Body = http.MaxBytesReader(w, req.Body, 4096)
	if err := req.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return nil, false
	}
	values := req.PostForm
	if len(values) < 2 || len(values) > 3 || len(values["handle"]) != 1 || len(values["decision"]) != 1 ||
		(len(values) == 3 && (len(values["remember"]) != 1 || values.Get("remember") != "1" || !allowRemember)) ||
		values.Get("handle") == "" || (values.Get("decision") != "approve" && values.Get("decision") != "deny") {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return nil, false
	}
	return values, true
}

// serializeOriginHost renders a hostname the way browsers serialize it in an origin.
func serializeOriginHost(hostname string) (string, error) {
	host := strings.ToLower(hostname)
	if !strings.Contains(host, ":") {
		ascii, err := idna.Lookup.ToASCII(host)
		if err != nil || ascii == "" {
			return "", errors.New("invalid origin")
		}
		return ascii, nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" {
		return "", errors.New("invalid origin")
	}
	host = ip.String()
	if ip.Is4In6() {
		// Browsers serialize mapped IPv4 as hexadecimal IPv6, not dotted decimal.
		b := ip.As16()
		host = fmt.Sprintf("::ffff:%x:%x", uint16(b[12])<<8|uint16(b[13]), uint16(b[14])<<8|uint16(b[15]))
	}
	return "[" + host + "]", nil
}
