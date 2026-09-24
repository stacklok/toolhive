// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"html/template"
	"log/slog"
	"net/http"
	"strings"
)

// This is the first HTML surface in pkg/authserver -- every other handler
// writes JSON. html/template (not manual string concatenation) is used
// throughout for auto-escaping, since these pages echo back a user-submitted
// user_code and an upstream-supplied name/email.
//
// Styling follows ToolHive/Stacklok's brand palette (deep emerald green
// primary action, Inter-first font stack, pill buttons, light/dark via
// prefers-color-scheme) so this page doesn't look like an unstyled internal
// tool when a human lands on it mid device-flow login. No external
// stylesheets or fonts are loaded: ToolHive commonly runs in air-gapped/
// enterprise environments, and a login-adjacent page reaching out to a
// third-party CDN is both an availability risk and a needless referrer leak.

// pageStyles is the shared stylesheet for all three device-flow pages,
// tokenized on ToolHive/Stacklok's palette (see toolhive-cloud-ui's
// globals.css: --brand hsl(161 94% 21%), --btn-primary-hover #02543a,
// --nav-background #18442e). Duplicated as a single constant across pages
// rather than a separate stylesheet request -- these are simple, low-traffic
// pages and a second HTTP round trip buys nothing.
const pageStyles = `
:root{
  --bg:#f4f4f5; --card:#ffffff; --fg:#0a0a0b; --muted:#71717a;
  --border:#e4e4e7; --brand:#026b46; --brand-hover:#02543a; --brand-fg:#ffffff;
  --nav-bg:#18442e; --danger:#dc2626; --danger-bg:#fef2f2; --danger-border:#fecaca;
  --radius:0.75rem;
}
@media (prefers-color-scheme: dark){
  :root{
    --bg:#0f0f11; --card:#18181b; --fg:#fafafa; --muted:#a1a1aa;
    --border:#27272a; --brand:#1a8f61; --brand-hover:#22a873; --brand-fg:#ffffff;
    --nav-bg:#0f2e1e; --danger:#f87171; --danger-bg:#2a1414; --danger-border:#4c1d1d;
  }
}
*{box-sizing:border-box}
body{
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,Helvetica,Arial,sans-serif;
  background:var(--bg); color:var(--fg); margin:0; padding:2.5rem 1rem;
  display:flex; justify-content:center; min-height:100vh;
}
.card{
  width:100%; max-width:26rem; height:fit-content;
  background:var(--card); border:1px solid var(--border); border-radius:var(--radius);
  box-shadow:0 1px 3px rgba(0,0,0,0.08), 0 1px 2px rgba(0,0,0,0.04);
}
.card-body{padding:2rem 1.75rem}
h1{font-size:1.25rem; font-weight:600; margin:0 0 0.4rem; letter-spacing:-0.01em}
.subtitle{color:var(--muted); font-size:0.9rem; margin:0 0 1.5rem; line-height:1.4}
.error{
  background:var(--danger-bg); border:1px solid var(--danger-border); color:var(--danger);
  border-radius:0.5rem; padding:0.65rem 0.85rem; font-size:0.875rem; margin-bottom:1.25rem;
}
input[type=text]{
  font-size:1.5rem; font-weight:600; letter-spacing:0.15em; text-align:center;
  text-transform:uppercase; width:100%; padding:0.75rem 0.5rem; margin:0 0 1.5rem;
  border:1.5px solid var(--border); border-radius:0.5rem; background:var(--card); color:var(--fg);
  font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
}
input[type=text]:focus{
  outline:none; border-color:var(--brand);
  box-shadow:0 0 0 3px color-mix(in srgb, var(--brand) 20%, transparent);
}
input[type=text]::placeholder{color:var(--muted); font-weight:400; letter-spacing:0.1em}
button{
  font-size:0.9rem; font-weight:600; padding:0.7rem 1.4rem; border-radius:999px;
  border:none; cursor:pointer; width:100%; transition:background-color 0.15s ease;
  font-family:inherit;
}
button:focus-visible{outline:2px solid var(--brand); outline-offset:2px}
.btn-primary{background:var(--brand); color:var(--brand-fg)}
.btn-primary:hover{background:var(--brand-hover)}
.btn-secondary{background:transparent; color:var(--fg); border:1.5px solid var(--border)}
.btn-secondary:hover{background:color-mix(in srgb, var(--fg) 6%, transparent)}
.btn-row{display:flex; gap:0.75rem; margin-top:1.5rem}
dl{margin:0 0 0.25rem; font-size:0.9rem}
dt{color:var(--muted); font-weight:500; margin-top:0.9rem}
dt:first-child{margin-top:0}
dd{margin:0.2rem 0 0; font-weight:500}
.scope-chip{
  display:inline-block; background:color-mix(in srgb, var(--brand) 12%, transparent);
  color:var(--brand); border-radius:999px; padding:0.15rem 0.65rem; font-size:0.8rem;
  font-weight:500; margin:0.2rem 0.35rem 0.2rem 0;
}
.identity{
  display:flex; align-items:center; gap:0.75rem; padding:0.85rem 1rem; margin-bottom:1.25rem;
  background:color-mix(in srgb, var(--fg) 4%, transparent); border-radius:0.6rem;
}
.avatar{
  width:2.25rem; height:2.25rem; border-radius:999px; background:var(--brand); color:var(--brand-fg);
  display:flex; align-items:center; justify-content:center; font-weight:600; font-size:0.95rem;
  flex-shrink:0;
}
.identity-text{min-width:0}
.identity-name{font-weight:600; font-size:0.9rem; overflow:hidden; text-overflow:ellipsis; white-space:nowrap}
.identity-email{color:var(--muted); font-size:0.8rem; overflow:hidden; text-overflow:ellipsis; white-space:nowrap}
.result-icon{
  width:3rem; height:3rem; border-radius:999px; display:flex; align-items:center; justify-content:center;
  margin:0 auto 1.25rem; font-size:1.5rem;
}
.result-icon.ok{background:color-mix(in srgb, var(--brand) 15%, transparent); color:var(--brand)}
.result-icon.denied{background:color-mix(in srgb, var(--muted) 20%, transparent); color:var(--muted)}
.result-icon.err{background:var(--danger-bg); color:var(--danger)}
.result-body{text-align:center}
`

type verifyPageData struct {
	UserCode string
	Error    string
}

var verifyPageTemplate = template.Must(template.New("device-verify").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Device Login &middot; ToolHive</title>
<style>` + pageStyles + `</style>
</head>
<body>
<div class="card">
  <div class="card-body">
    <h1>Enter your device code</h1>
    <p class="subtitle">Enter the code shown on your device to continue signing in.</p>
    {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
    <form method="POST" action="/oauth/device">
      <input type="text" name="user_code" value="{{.UserCode}}" placeholder="XXXX-XXXX"
        autocomplete="off" autofocus required>
      <button type="submit" class="btn-primary">Continue</button>
    </form>
  </div>
</div>
</body>
</html>`))

type confirmPageData struct {
	Name         string
	Email        string
	ClientID     string
	Scopes       []string
	ConfirmToken string
	Initial      string
}

var confirmPageTemplate = template.Must(template.New("device-confirm").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authorize Device &middot; ToolHive</title>
<style>` + pageStyles + `</style>
</head>
<body>
<div class="card">
  <div class="card-body">
    <h1>Authorize this device?</h1>
    <p class="subtitle">A device is requesting access to your account. Review the details below before continuing.</p>
    <div class="identity">
      <div class="avatar">{{.Initial}}</div>
      <div class="identity-text">
        <div class="identity-name">{{if .Name}}{{.Name}}{{else}}{{.Email}}{{end}}</div>
        {{if and .Name .Email}}<div class="identity-email">{{.Email}}</div>{{end}}
      </div>
    </div>
    <dl>
      <dt>Application</dt><dd>{{.ClientID}}</dd>
      {{if .Scopes}}
      <dt>Requested access</dt>
      <dd>{{range .Scopes}}<span class="scope-chip">{{.}}</span>{{end}}</dd>
      {{end}}
    </dl>
    <form method="POST" action="/oauth/device/confirm">
      <input type="hidden" name="confirm_token" value="{{.ConfirmToken}}">
      <div class="btn-row">
        <button class="btn-secondary" type="submit" name="action" value="deny">Deny</button>
        <button class="btn-primary" type="submit" name="action" value="approve">Approve</button>
      </div>
    </form>
  </div>
</div>
</body>
</html>`))

type resultPageData struct {
	Title   string
	Message string
	Icon    string // "ok", "denied", or "err"
	Glyph   string // a single character/emoji rendered inside .result-icon
}

var resultPageTemplate = template.Must(template.New("device-result").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} &middot; ToolHive</title>
<style>` + pageStyles + `</style>
</head>
<body>
<div class="card">
  <div class="card-body result-body">
    <div class="result-icon {{.Icon}}">{{.Glyph}}</div>
    <h1>{{.Title}}</h1>
    <p class="subtitle">{{.Message}}</p>
  </div>
</div>
</body>
</html>`))

// setHTMLSecurityHeaders sets the headers common to every rendered device-flow
// page: HTML content type, no caching (these pages carry a confirm_token or
// resolved-identity content that must not be cached), and anti-framing
// headers. This is the first interactive human UI surface in pkg/authserver
// -- every other handler writes JSON with no framing risk -- so the
// Approve/Deny confirmation page is the first place a clickjacking defense is
// needed here.
func setHTMLSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// initials returns up to two uppercase initials for a display name/email, for
// the confirm page's avatar badge. Falls back to "?" when neither is usable.
func initials(name, email string) string {
	source := name
	if source == "" {
		source = email
	}
	fields := strings.FieldsFunc(source, func(r rune) bool {
		return r == ' ' || r == '.' || r == '@' || r == '_' || r == '-'
	})
	if len(fields) == 0 {
		return "?"
	}
	out := firstRuneUpper(fields[0])
	if len(fields) > 1 && fields[1] != "" {
		out += firstRuneUpper(fields[1])
	}
	return out
}

// firstRuneUpper returns the uppercased first Unicode rune of s. Slicing the
// first byte instead (s[:1]) would split a multi-byte rune and render
// invalid UTF-8/replacement characters for non-ASCII names.
func firstRuneUpper(s string) string {
	r := []rune(s)
	return strings.ToUpper(string(r[:1]))
}

func renderVerifyForm(w http.ResponseWriter, status int, userCode, errMsg string) {
	setHTMLSecurityHeaders(w)
	w.WriteHeader(status)
	data := verifyPageData{UserCode: userCode, Error: errMsg}
	if err := verifyPageTemplate.Execute(w, data); err != nil {
		slog.Error("device verification: failed to render verify form", "error", err)
	}
}

func renderConfirm(w http.ResponseWriter, data confirmPageData) {
	setHTMLSecurityHeaders(w)
	data.Initial = initials(data.Name, data.Email)
	if err := confirmPageTemplate.Execute(w, data); err != nil {
		slog.Error("device verification: failed to render confirm page", "error", err)
	}
}

func renderResult(w http.ResponseWriter, title, message string) {
	setHTMLSecurityHeaders(w)
	data := resultPageData{Title: title, Message: message, Icon: "ok", Glyph: "✓"}
	if err := resultPageTemplate.Execute(w, data); err != nil {
		slog.Error("device verification: failed to render result page", "error", err)
	}
}

// renderDenied renders the result page in its "denied" visual state --
// distinct from renderResult's success checkmark, since a device denial is a
// deliberate, expected outcome rather than a completed grant.
func renderDenied(w http.ResponseWriter, title, message string) {
	setHTMLSecurityHeaders(w)
	data := resultPageData{Title: title, Message: message, Icon: "denied", Glyph: "✕"}
	if err := resultPageTemplate.Execute(w, data); err != nil {
		slog.Error("device verification: failed to render result page", "error", err)
	}
}

func renderError(w http.ResponseWriter, status int, message string) {
	setHTMLSecurityHeaders(w)
	w.WriteHeader(status)
	data := resultPageData{Title: "Something went wrong", Message: message, Icon: "err", Glyph: "!"}
	if err := resultPageTemplate.Execute(w, data); err != nil {
		slog.Error("device verification: failed to render error page", "error", err)
	}
}
