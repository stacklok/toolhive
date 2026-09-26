// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"errors"
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
// the form the human uses to enter/confirm a device flow user_code, issuing
// the anti-forgery token the POST requires (see browser_binding.go). See
// DeviceVerificationSubmitHandler for what happens next.
func (h *Handler) DeviceVerificationHandler(w http.ResponseWriter, req *http.Request) {
	userCode := normalizeUserCode(req.URL.Query().Get("user_code"))
	h.renderVerifyForm(w, req, http.StatusOK, userCode, "")
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
		h.renderVerifyForm(w, req, http.StatusBadRequest, "", "Could not read the submitted form.")
		return
	}

	userCode := normalizeUserCode(req.PostForm.Get("user_code"))

	// A submission must come from a form this server rendered to this browser:
	// otherwise a cross-site page could submit an attacker's user_code from the
	// victim's browser and have that browser bound to the attacker's login.
	if err := h.verifyDeviceFormToken(req); err != nil {
		slog.Warn("device verification: rejected form submission", "reason", err.Error())
		h.renderVerifyForm(w, req, http.StatusBadRequest, userCode,
			"This form has expired. Please enter the code again.")
		return
	}

	if userCode == "" {
		h.renderVerifyForm(w, req, http.StatusBadRequest, "", "Please enter the code shown on your device.")
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
		h.renderVerifyForm(w, req, http.StatusBadRequest, userCode,
			"That code is invalid or has expired. Please check your device and try again.")
		return
	}

	// Device flow has no client_id/redirect_uri of its own to route on (see
	// this function's doc comment), so it cannot pick the upstream
	// appropriate for device.ClientID the way AuthorizeHandler's per-client
	// IDP routing does. Rather than silently defaulting to h.upstreams[0] --
	// which would authenticate against whichever upstream happens to be
	// configured first in a multi-upstream deployment, exactly the kind of
	// IDP mix-up these servers otherwise defend against -- require exactly
	// one configured upstream so that limitation is a loud startup-time-visible
	// error, not a silent routing decision made per request.
	if len(h.upstreams) != 1 {
		slog.Error("device verification: device flow requires exactly one configured upstream",
			"upstream_count", len(h.upstreams))
		renderError(w, http.StatusInternalServerError, "This server is not configured for device-flow sign-in.")
		return
	}

	secrets := newUpstreamAuthSecrets()
	binding := newBrowserBinding()
	pending := &storage.PendingDeviceLogin{
		DeviceCode:           device.DeviceCode,
		UserCode:             device.UserCode,
		UpstreamPKCEVerifier: secrets.PKCEVerifier,
		UpstreamNonce:        secrets.Nonce,
		BrowserBindingHash:   binding.hash,
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

	// Bind the login to this browser exactly as AuthorizeHandler does: only the
	// browser that submitted the user_code may complete the upstream callback,
	// so a user_code holder cannot hand the upstream URL to someone else and
	// have that person's identity land on the device. See browser_binding.go.
	h.setBrowserBindingCookie(w, secrets.State, binding.value)
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
// ordinary 400. Returns deviceLoginRejected (response already written) when
// req does not carry the browser-binding cookie the verification page set
// for internalState; the login is consumed so the state cannot be retried.
func (h *Handler) tryCompleteDeviceLogin(
	ctx context.Context, w http.ResponseWriter, req *http.Request, internalState, code string,
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
	if bindErr := h.verifyBrowserBinding(req, internalState, devicePending.BrowserBindingHash); bindErr != nil {
		writeUnboundCallbackRejection(w, bindErr, "flow", "device", "upstream_provider", devicePending.UpstreamProviderName)
		return deviceLoginRejected
	}
	h.completeDeviceLogin(ctx, w, devicePending, code)
	return deviceLoginCompleted
}

// tryDenyDeviceLoginOnUpstreamError handles an upstream IDP error (the
// "error" query parameter on /oauth/callback, e.g. the human clicked "Deny"
// at the upstream login screen, or the IDP itself failed) for a device-flow
// login: handleUpstreamError's fallback when internalState does not match a
// PendingAuthorization either. Without this, an upstream error for a
// device-flow login left the DeviceRequest Pending forever (until the
// device_code itself expired) instead of surfacing the denial immediately,
// since nothing else ever calls MarkDeviceRequestDenied for this path.
//
// Returns false (no response written) when internalState does not match a
// pending device login either, so the caller falls through to its own
// generic error page. A browser that does not present the login's binding
// cookie gets a 400 and the login is consumed, but the DeviceRequest is left
// pending: that browser did not start the login and may not decide it.
func (h *Handler) tryDenyDeviceLoginOnUpstreamError(
	ctx context.Context, w http.ResponseWriter, req *http.Request, internalState string,
) bool {
	devicePending, err := h.storage.LoadPendingDeviceLogin(ctx, internalState)
	if err != nil {
		return false
	}
	if delErr := h.storage.DeletePendingDeviceLogin(ctx, internalState); delErr != nil {
		slog.Warn("failed to delete pending device login", "error", delErr)
	}
	if bindErr := h.verifyBrowserBinding(req, internalState, devicePending.BrowserBindingHash); bindErr != nil {
		writeUnboundCallbackRejection(w, bindErr, "flow", "device", "upstream_provider", devicePending.UpstreamProviderName)
		return true
	}
	if err := h.deviceStorage.MarkDeviceRequestDenied(ctx, devicePending.DeviceCode); err != nil {
		slog.Warn("device verification: failed to mark device request denied after upstream error", "error", err)
	}
	renderDenied(w, "Sign-in failed",
		"The identity provider reported an error, so this device's request was denied. You may close this window.")
	return true
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
		// UserID/SessionExpiresAt are left zero here and filled in by
		// DeviceVerificationConfirmHandler once a session id exists -- see
		// PendingDeviceConfirmation.UpstreamTokens's doc comment.
		UpstreamTokens: &storage.UpstreamTokens{
			ProviderID:      pending.UpstreamProviderName,
			AccessToken:     result.Tokens.AccessToken,
			RefreshToken:    result.Tokens.RefreshToken,
			IDToken:         result.Tokens.IDToken,
			ExpiresAt:       result.Tokens.ExpiresAt,
			UpstreamSubject: result.Subject,
			ClientID:        device.ClientID,
		},
		Synthetic: result.Synthetic,
		CreatedAt: time.Now(),
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
		renderDenied(w, "Access denied", "You have denied this device's request. You may close this window.")
		return
	}

	sessionID := rand.Text()
	if err := h.persistDeviceSessionTokens(ctx, confirmation, sessionID); err != nil {
		slog.Error("device verification: failed to store upstream tokens", "error", err)
		renderError(w, http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}
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

// persistDeviceSessionTokens stores the upstream tokens captured at login
// time (completeDeviceLogin) under the device grant's final session id.
// Device-flow sessions have no session id until this point -- unlike the
// OAuth-client authorization_code flow, where CallbackHandler stores tokens
// under a session id minted up front -- so completeDeviceLogin could only
// stash the tokens in the PendingDeviceConfirmation for this handler to
// persist once sessionID exists. Mirrors CallbackHandler's
// UserID/SessionExpiresAt population and refresh-token carry-forward.
func (h *Handler) persistDeviceSessionTokens(
	ctx context.Context, confirmation *storage.PendingDeviceConfirmation, sessionID string,
) error {
	if confirmation.UpstreamTokens == nil {
		return errors.New("pending device confirmation has no upstream tokens")
	}
	storageTokens := *confirmation.UpstreamTokens
	storageTokens.UserID = confirmation.ResolvedUserID
	storageTokens.SessionExpiresAt = time.Now().Add(h.config.RefreshTokenLifespan)

	h.maybeCarryForwardRefreshToken(
		ctx, &storageTokens, confirmation.ResolvedUserID, storageTokens.UpstreamSubject,
		storageTokens.ProviderID, confirmation.Synthetic,
	)

	return h.storage.StoreUpstreamTokens(ctx, sessionID, storageTokens.ProviderID, &storageTokens)
}
