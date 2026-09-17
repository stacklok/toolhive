// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// normalizeUserCode uppercases and strips whitespace/dashes from a
// user-submitted code, then re-inserts the canonical "XXXX-XXXX" grouping
// (see userCodeGroupLength, device_authorization.go) when the result is the
// expected length. A malformed submission is returned as-is and simply fails
// the LoadDeviceRequestByUserCode lookup, surfacing as "invalid or expired".
func normalizeUserCode(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s)
	if len(s) == userCodeGroupLength*2 {
		return s[:userCodeGroupLength] + "-" + s[userCodeGroupLength:]
	}
	return s
}

// DeviceVerificationHandler handles GET /oauth/device requests: it renders
// the form the human uses to enter/confirm a device flow user_code. See
// DeviceVerificationSubmitHandler for what happens next.
//
// a bound method value (h.DeviceVerificationHandler) alongside every other
// route on this type, even though this particular handler needs no Handler state.
//
//nolint:revive // must stay a *Handler method: OAuthRoutes registers it as
func (h *Handler) DeviceVerificationHandler(w http.ResponseWriter, req *http.Request) {
	userCode := normalizeUserCode(req.URL.Query().Get("user_code"))
	renderVerifyForm(w, http.StatusOK, userCode, "")
}

// DeviceVerificationSubmitHandler handles POST /oauth/device: it validates
// the submitted user_code against a pending DeviceRequest and, on success,
// redirects to the first configured upstream IDP to authenticate the human --
// mirroring AuthorizeHandler's redirect-to-upstream step (authorize.go), but
// without any of the OAuth-client-specific machinery (no fosite
// AuthorizeRequester, no client redirect_uri): the device flow's
// verification page has no client_id/redirect_uri of its own.
func (h *Handler) DeviceVerificationSubmitHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	req.Body = http.MaxBytesReader(w, req.Body, MaxDCRBodySize)
	if err := req.ParseForm(); err != nil {
		renderVerifyForm(w, http.StatusBadRequest, "", "Could not read the submitted form.")
		return
	}

	userCode := normalizeUserCode(req.PostForm.Get("user_code"))
	if userCode == "" {
		renderVerifyForm(w, http.StatusBadRequest, "", "Please enter the code shown on your device.")
		return
	}

	device, err := h.deviceStorage.LoadDeviceRequestByUserCode(ctx, userCode)
	if err != nil && !isNotFoundOrExpired(err) {
		slog.Error("device verification: failed to look up user_code", "error", err)
		renderError(w, http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}
	if err != nil || device.Status != storage.DeviceRequestStatusPending {
		slog.Debug("device verification: invalid or expired user_code", "error", err)
		renderVerifyForm(w, http.StatusBadRequest, userCode,
			"That code is invalid or has expired. Please check your device and try again.")
		return
	}

	if len(h.upstreams) == 0 {
		slog.Error("device verification: no upstream configured")
		renderError(w, http.StatusInternalServerError, "This server has no identity provider configured.")
		return
	}

	secrets := newUpstreamAuthSecrets()
	pending := &storage.PendingDeviceLogin{
		DeviceCode:           device.DeviceCode,
		UserCode:             device.UserCode,
		UpstreamPKCEVerifier: secrets.PKCEVerifier,
		UpstreamNonce:        secrets.Nonce,
		UpstreamProviderName: h.upstreams[0].Name,
		CreatedAt:            time.Now(),
	}
	if err := h.storage.StorePendingDeviceLogin(ctx, secrets.State, pending); err != nil {
		slog.Error("device verification: failed to store pending login", "error", err)
		renderError(w, http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}

	var authOpts []upstream.AuthorizationOption
	if secrets.Nonce != "" {
		authOpts = append(authOpts, upstream.WithAdditionalParams(map[string]string{"nonce": secrets.Nonce}))
	}
	upstreamURL, err := h.upstreams[0].Provider.AuthorizationURL(secrets.State, secrets.PKCEChallenge, authOpts...)
	if err != nil {
		slog.Error("device verification: failed to build upstream authorization URL", "error", err)
		_ = h.storage.DeletePendingDeviceLogin(ctx, secrets.State)
		renderError(w, http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}

	http.Redirect(w, req, upstreamURL, http.StatusFound)
}

// tryCompleteDeviceLogin attempts to treat internalState as a device-flow
// login rather than an OAuth-client authorization: it is CallbackHandler's
// fallback when internalState does not match a PendingAuthorization, kept as
// a separate function so CallbackHandler's own branching stays simple. See
// completeDeviceLogin's doc comment for why /oauth/callback is shared between
// the two flows.
//
// Returns deviceLoginNotFound (no response written) when internalState
// genuinely does not match a pending device login either, so the caller
// falls through to its own not-found handling. Returns deviceLoginStorageError
// (response already written) when the lookup failed for a reason other than
// not-found/expired -- a real backend error must not be silently
// reinterpreted as "not a device login" and reported to the client as an
// ordinary 400.
func (h *Handler) tryCompleteDeviceLogin(
	ctx context.Context, w http.ResponseWriter, internalState, code string,
) deviceLoginOutcome {
	devicePending, err := h.storage.LoadPendingDeviceLogin(ctx, internalState)
	if err != nil {
		if isNotFoundOrExpired(err) {
			return deviceLoginNotFound
		}
		slog.Error("failed to load pending device login", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return deviceLoginStorageError
	}
	if delErr := h.storage.DeletePendingDeviceLogin(ctx, internalState); delErr != nil {
		slog.Warn("failed to delete pending device login", "error", delErr)
	}
	h.completeDeviceLogin(ctx, w, devicePending, code)
	return deviceLoginCompleted
}

// completeDeviceLogin finishes a device-flow verification-page login once
// the upstream IDP has redirected back to /oauth/callback (CallbackHandler
// dispatches here after determining internalState matches a
// PendingDeviceLogin rather than a PendingAuthorization -- see that
// function's doc comment for why both flows share one callback endpoint).
// It resolves the canonical identity (mirroring CallbackHandler's first-leg
// resolution) and stores it server-side behind a fresh opaque token, then
// renders the Approve/Deny confirmation page. See PendingDeviceConfirmation's
// doc comment for why the resolved identity is never sent to the browser as
// editable form data.
func (h *Handler) completeDeviceLogin(
	ctx context.Context, w http.ResponseWriter, pending *storage.PendingDeviceLogin, code string,
) {
	upstreamProvider, ok := h.upstreamByName(pending.UpstreamProviderName)
	if !ok {
		slog.Error("device verification: upstream provider not found", "provider", pending.UpstreamProviderName)
		renderError(w, http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}

	result, err := upstreamProvider.ExchangeCodeForIdentity(ctx, code, pending.UpstreamPKCEVerifier, pending.UpstreamNonce)
	if err != nil {
		slog.Error("device verification: failed to exchange code or resolve identity", "error", err)
		renderError(w, http.StatusInternalServerError, "Sign-in failed. Please try again.")
		return
	}

	// Time has passed during login: re-check the device request is still
	// pending/unexpired before proceeding.
	device, err := h.deviceStorage.LoadDeviceRequestByUserCode(ctx, pending.UserCode)
	if err != nil && !isNotFoundOrExpired(err) {
		slog.Error("device verification: failed to re-check device request", "error", err)
		renderError(w, http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}
	if err != nil || device.Status != storage.DeviceRequestStatusPending {
		slog.Debug("device verification: device request no longer pending", "error", err)
		renderError(w, http.StatusBadRequest,
			"This device request is no longer valid. Please try again from your device.")
		return
	}

	userID, userName, userEmail := result.Subject, result.Name, result.Email
	if !result.Synthetic {
		user, resolveErr := h.userResolver.ResolveUser(ctx, pending.UpstreamProviderName, result.Subject)
		if resolveErr != nil {
			slog.Error("device verification: failed to resolve user", "error", resolveErr)
			status, msg := http.StatusInternalServerError, "Something went wrong. Please try again."
			if errors.Is(resolveErr, storage.ErrUserNotProvisioned) {
				status, msg = http.StatusForbidden, "Your account is not provisioned to use this application."
			}
			renderError(w, status, msg)
			return
		}
		userID = user.ID
		h.userResolver.UpdateLastAuthenticated(ctx, pending.UpstreamProviderName, result.Subject)
	}

	token := rand.Text()
	confirmation := &storage.PendingDeviceConfirmation{
		DeviceCode:        device.DeviceCode,
		UserCode:          device.UserCode,
		ResolvedUserID:    userID,
		ResolvedUserName:  userName,
		ResolvedUserEmail: userEmail,
		CreatedAt:         time.Now(),
	}
	if err := h.storage.StorePendingDeviceConfirmation(ctx, token, confirmation); err != nil {
		slog.Error("device verification: failed to store pending confirmation", "error", err)
		renderError(w, http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}

	renderConfirm(w, confirmPageData{
		Name:         userName,
		Email:        userEmail,
		ClientID:     device.ClientID,
		Scopes:       device.Scopes,
		ConfirmToken: token,
	})
}

// DeviceVerificationConfirmHandler handles POST /oauth/device/confirm: the
// human's explicit Approve/Deny decision from the confirmation page rendered
// by DeviceVerificationCallbackHandler. It resolves confirm_token back to the
// server-side PendingDeviceConfirmation (single-use) and calls
// storage.MarkDeviceRequestAuthorized or MarkDeviceRequestDenied accordingly
// -- the same calls the device-flow integration test drives directly against
// storage to simulate this page.
func (h *Handler) DeviceVerificationConfirmHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	req.Body = http.MaxBytesReader(w, req.Body, MaxDCRBodySize)
	if err := req.ParseForm(); err != nil {
		renderError(w, http.StatusBadRequest, "Could not read the submitted form.")
		return
	}

	token := req.PostForm.Get("confirm_token")
	action := req.PostForm.Get("action")
	if token == "" || (action != "approve" && action != "deny") {
		renderError(w, http.StatusBadRequest, "Invalid confirmation request.")
		return
	}

	confirmation, err := h.storage.LoadPendingDeviceConfirmation(ctx, token)
	if err != nil {
		slog.Warn("device verification: pending confirmation not found", "error", err)
		renderError(w, http.StatusBadRequest, "This confirmation request was not found or has expired.")
		return
	}
	// Delete immediately (single-use): a confirm_token authorizes exactly one decision.
	if err := h.storage.DeletePendingDeviceConfirmation(ctx, token); err != nil {
		slog.Warn("device verification: failed to delete pending confirmation", "error", err)
	}

	if action == "deny" {
		if err := h.deviceStorage.MarkDeviceRequestDenied(ctx, confirmation.DeviceCode); err != nil {
			slog.Warn("device verification: failed to mark device request denied", "error", err)
			renderError(w, http.StatusBadRequest,
				"This device request is no longer valid. Please try again from your device.")
			return
		}
		renderResult(w, "Access denied", "You have denied this device's request. You may close this window.")
		return
	}

	sessionID := rand.Text()
	if err := h.deviceStorage.MarkDeviceRequestAuthorized(
		ctx, confirmation.DeviceCode,
		confirmation.ResolvedUserID, confirmation.ResolvedUserName, confirmation.ResolvedUserEmail, sessionID,
	); err != nil {
		slog.Error("device verification: failed to mark device request authorized", "error", err)
		renderError(w, http.StatusBadRequest,
			"This device request is no longer valid. Please try again from your device.")
		return
	}

	renderResult(w, "Device authorized", "You may now close this window and return to your device.")
}

// --- Rendering ---
//
// This is the first HTML surface in pkg/authserver -- every other handler
// writes JSON. html/template (not manual string concatenation) is used
// throughout for auto-escaping, since these pages echo back a user-submitted
// user_code and an upstream-supplied name/email.

type verifyPageData struct {
	UserCode string
	Error    string
}

var verifyPageTemplate = template.Must(template.New("device-verify").Parse(`<!DOCTYPE html>
<html>
<head><title>Device Login</title>
<style>
body{font-family:sans-serif;max-width:420px;margin:80px auto;padding:0 16px}
input{font-size:1.5rem;letter-spacing:0.1em;text-align:center;text-transform:uppercase;
width:100%;padding:0.5em;margin:1em 0;box-sizing:border-box}
button{font-size:1rem;padding:0.6em 1.2em;width:100%}
.error{color:#b00020;margin-bottom:1em}
</style>
</head>
<body>
<h1>Enter your code</h1>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
<form method="POST" action="/oauth/device">
<input type="text" name="user_code" value="{{.UserCode}}" placeholder="XXXX-XXXX" autofocus required>
<button type="submit">Continue</button>
</form>
</body>
</html>`))

type confirmPageData struct {
	Name         string
	Email        string
	ClientID     string
	Scopes       []string
	ConfirmToken string
}

var confirmPageTemplate = template.Must(template.New("device-confirm").Parse(`<!DOCTYPE html>
<html>
<head><title>Authorize Device</title>
<style>
body{font-family:sans-serif;max-width:420px;margin:80px auto;padding:0 16px}
button{font-size:1rem;padding:0.6em 1.2em;margin-right:0.5em;cursor:pointer}
.approve{background:#0a7d2c;color:#fff;border:none}
.deny{background:#eee;border:1px solid #ccc}
dl{margin:1em 0}
dt{font-weight:bold}
dd{margin:0 0 0.5em}
</style>
</head>
<body>
<h1>Authorize this device?</h1>
<p>Signed in as <strong>{{if .Name}}{{.Name}}{{else}}{{.Email}}{{end}}</strong>{{if and .Name .Email}} ({{.Email}}){{end}}.</p>
<dl>
<dt>Application</dt><dd>{{.ClientID}}</dd>
{{if .Scopes}}<dt>Requested access</dt><dd>{{range .Scopes}}{{.}} {{end}}</dd>{{end}}
</dl>
<form method="POST" action="/oauth/device/confirm">
<input type="hidden" name="confirm_token" value="{{.ConfirmToken}}">
<button class="approve" type="submit" name="action" value="approve">Approve</button>
<button class="deny" type="submit" name="action" value="deny">Deny</button>
</form>
</body>
</html>`))

type resultPageData struct {
	Title   string
	Message string
}

var resultPageTemplate = template.Must(template.New("device-result").Parse(`<!DOCTYPE html>
<html>
<head><title>{{.Title}}</title>
<style>body{font-family:sans-serif;max-width:420px;margin:80px auto;padding:0 16px;text-align:center}</style>
</head>
<body>
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
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

func renderVerifyForm(w http.ResponseWriter, status int, userCode, errMsg string) {
	setHTMLSecurityHeaders(w)
	w.WriteHeader(status)
	if err := verifyPageTemplate.Execute(w, verifyPageData{UserCode: userCode, Error: errMsg}); err != nil {
		slog.Error("device verification: failed to render verify form", "error", err)
	}
}

func renderConfirm(w http.ResponseWriter, data confirmPageData) {
	setHTMLSecurityHeaders(w)
	if err := confirmPageTemplate.Execute(w, data); err != nil {
		slog.Error("device verification: failed to render confirm page", "error", err)
	}
}

func renderResult(w http.ResponseWriter, title, message string) {
	setHTMLSecurityHeaders(w)
	if err := resultPageTemplate.Execute(w, resultPageData{Title: title, Message: message}); err != nil {
		slog.Error("device verification: failed to render result page", "error", err)
	}
}

func renderError(w http.ResponseWriter, status int, message string) {
	setHTMLSecurityHeaders(w)
	w.WriteHeader(status)
	if err := resultPageTemplate.Execute(w, resultPageData{Title: "Error", Message: message}); err != nil {
		slog.Error("device verification: failed to render error page", "error", err)
	}
}
