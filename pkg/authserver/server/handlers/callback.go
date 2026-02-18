// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// CallbackHandler handles GET /oauth/callback requests.
// It exchanges the upstream authorization code and issues our own authorization code.
func (h *Handler) CallbackHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Parse query parameters
	code := req.URL.Query().Get("code")
	internalState := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")
	errorDescription := req.URL.Query().Get("error_description")

	// Handle upstream errors
	if errorParam != "" {
		h.handleUpstreamError(ctx, w, internalState, errorParam, errorDescription)
		return
	}

	// Validate required parameters - use http.Error for early errors without valid pending
	if internalState == "" {
		slog.Warn("callback missing state parameter")
		http.Error(w, "missing state parameter", http.StatusBadRequest)
		return
	}

	if code == "" {
		slog.Warn("callback missing code parameter")
		http.Error(w, "missing code parameter", http.StatusBadRequest)
		return
	}

	// Load and delete pending authorization (single-use)
	pending, err := h.storage.LoadPendingAuthorization(ctx, internalState)
	if err != nil {
		slog.Warn("pending authorization not found",
			"error", err,
		)
		http.Error(w, "authorization request not found or expired", http.StatusBadRequest)
		return
	}

	// Delete pending authorization immediately (single-use)
	if err := h.storage.DeletePendingAuthorization(ctx, internalState); err != nil {
		slog.Warn("failed to delete pending authorization",
			"error", err,
		)
	}

	// Build authorize requester for error responses now that we have pending
	ar := h.buildAuthorizeRequesterFromPending(ctx, pending)
	if ar == nil {
		// Stored redirect URI was corrupt - cannot redirect, show error page
		http.Error(w, "authorization request data corrupted", http.StatusInternalServerError)
		return
	}

	// Check if upstream provider is configured
	if h.upstream == nil {
		slog.Error("upstream provider not configured")
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("authorization server not configured"))
		return
	}

	// Exchange code and resolve identity in a single atomic operation.
	// This ensures OIDC nonce validation cannot be accidentally skipped.
	result, err := h.upstream.ExchangeCodeForIdentity(ctx, code, pending.UpstreamPKCEVerifier, pending.UpstreamNonce)
	if err != nil {
		slog.Error("failed to exchange code or resolve identity",
			"error", err,
		)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to exchange authorization code"))
		return
	}

	idpTokens := result.Tokens
	providerSubject := result.Subject

	// Get provider ID
	providerID := string(h.upstream.Type())

	// Resolve or create internal user
	user, err := h.userResolver.ResolveUser(ctx, providerID, providerSubject)
	if err != nil {
		slog.Error("failed to resolve user", "error", err)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to resolve user"))
		return
	}
	subject := user.ID

	// Update last authentication timestamp (supports OIDC max_age)
	h.userResolver.UpdateLastAuthenticated(ctx, providerID, providerSubject)

	// Generate session ID for this authorization
	sessionID := rand.Text()

	// Convert IDP tokens to storage tokens with binding fields
	storageTokens := &storage.UpstreamTokens{
		ProviderID:      providerID,
		AccessToken:     idpTokens.AccessToken,
		RefreshToken:    idpTokens.RefreshToken,
		IDToken:         idpTokens.IDToken,
		ExpiresAt:       idpTokens.ExpiresAt,
		ClientID:        pending.ClientID,
		UserID:          subject,         // Internal ToolHive user ID
		UpstreamSubject: providerSubject, // Upstream IDP's subject claim
	}

	if err := h.storage.StoreUpstreamTokens(ctx, sessionID, storageTokens); err != nil {
		slog.Error("failed to store upstream tokens",
			"error", err,
		)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to store session"))
		return
	}

	// Generate authorization code and redirect to client using fosite's RFC 6749 compliant handler
	if err := h.writeAuthorizationResponse(ctx, w, pending, sessionID, subject); err != nil {
		slog.Error("failed to create authorization response",
			"error", err,
		)
		// Clean up stored upstream tokens
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to create authorization code"))
		return
	}

	slog.Info("authorization successful, redirecting to client")
}

// writeAuthorizationResponse generates an authorization code and writes the redirect response.
// This uses fosite's WriteAuthorizeResponse for RFC 6749 compliant redirects with proper
// status codes (303 See Other) and cache headers.
func (h *Handler) writeAuthorizationResponse(
	ctx context.Context,
	w http.ResponseWriter,
	pending *storage.PendingAuthorization,
	sessionID string,
	subject string,
) error {
	// Get the client from storage
	fositeClient, err := h.storage.GetClient(ctx, pending.ClientID)
	if err != nil {
		return err
	}

	// Create the session with IDP session reference and client ID for binding
	sess := session.New(subject, sessionID, pending.ClientID)

	// Set expiration times
	now := time.Now()
	sess.SetExpiresAt(fosite.AuthorizeCode, now.Add(h.config.AuthorizeCodeLifespan))
	sess.SetExpiresAt(fosite.AccessToken, now.Add(h.config.AccessTokenLifespan))
	sess.SetExpiresAt(fosite.RefreshToken, now.Add(h.config.RefreshTokenLifespan))

	// Build the authorization request form
	form := url.Values{
		"redirect_uri":          {pending.RedirectURI},
		"code_challenge":        {pending.PKCEChallenge},
		"code_challenge_method": {pending.PKCEMethod},
	}

	// Create an authorize request using fosite
	authorizeRequest := fosite.NewAuthorizeRequest()
	authorizeRequest.Form = form
	authorizeRequest.Client = fositeClient
	authorizeRequest.Session = sess
	authorizeRequest.RequestedAt = now
	authorizeRequest.ResponseTypes = fosite.Arguments{"code"}
	authorizeRequest.State = pending.State // Set state for inclusion in redirect

	// Parse the redirect URI - this was validated by fosite during authorization,
	// so a parse error here indicates storage corruption
	redirectURI, err := url.Parse(pending.RedirectURI)
	if err != nil {
		return fmt.Errorf("stored redirect URI is invalid: %w", err)
	}
	authorizeRequest.RedirectURI = redirectURI

	// Grant only scopes that the client is registered for.
	// This prevents elevation if a tampered authorize request smuggled extra scopes
	// into the pending authorization.
	clientScopes := fositeClient.GetScopes()
	for _, scope := range pending.Scopes {
		if fosite.ExactScopeStrategy(clientScopes, scope) {
			authorizeRequest.RequestedScope = append(authorizeRequest.RequestedScope, scope)
			authorizeRequest.GrantedScope = append(authorizeRequest.GrantedScope, scope)
		} else {
			slog.Warn("filtered unregistered scope from authorization",
				"scope", scope,
				"client_id", pending.ClientID,
			)
		}
	}

	// Generate the authorization response using fosite
	response, err := h.provider.NewAuthorizeResponse(ctx, authorizeRequest, sess)
	if err != nil {
		return err
	}

	// Write the redirect response using fosite's RFC 6749 compliant handler
	// This handles status code (303), cache headers, and URL building
	h.provider.WriteAuthorizeResponse(ctx, w, authorizeRequest, response)
	return nil
}

// buildAuthorizeRequesterFromPending creates a minimal AuthorizeRequester for error responses.
// This allows using fosite's WriteAuthorizeError for consistent RFC 6749 error handling.
// Returns nil if the stored redirect URI cannot be parsed (indicates storage corruption).
func (h *Handler) buildAuthorizeRequesterFromPending(
	ctx context.Context,
	pending *storage.PendingAuthorization,
) fosite.AuthorizeRequester {
	ar := fosite.NewAuthorizeRequest()
	ar.State = pending.State

	// Parse redirect URI - this was validated during authorization,
	// so failure indicates storage corruption
	redirectURI, err := url.Parse(pending.RedirectURI)
	if err != nil {
		slog.Error("stored redirect URI is invalid",
			"redirect_uri", pending.RedirectURI,
			"error", err,
		)
		return nil
	}
	ar.RedirectURI = redirectURI

	if client, err := h.storage.GetClient(ctx, pending.ClientID); err == nil {
		ar.Client = client
	}
	return ar
}

// handleUpstreamError handles error responses from the upstream IDP.
// It attempts to redirect the error to the client if possible, otherwise shows an error page.
func (h *Handler) handleUpstreamError(
	ctx context.Context,
	w http.ResponseWriter,
	internalState string,
	errorParam string,
	errorDescription string,
) {
	slog.Warn("upstream IDP returned error", //nolint:gosec // G706: error params from upstream IDP response
		"error", errorParam,
		"error_description", errorDescription,
	)

	// Try to load pending authorization to redirect error to client
	if internalState != "" {
		pending, err := h.storage.LoadPendingAuthorization(ctx, internalState)
		if err == nil {
			_ = h.storage.DeletePendingAuthorization(ctx, internalState)
			ar := h.buildAuthorizeRequesterFromPending(ctx, pending)
			if ar != nil {
				// Use generic error hint to avoid exposing upstream IDP details to clients.
				// Detailed error information is logged above for server-side diagnostics.
				h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrAccessDenied.WithHint("upstream authentication failed"))
				return
			}
			// ar is nil means stored redirect URI was corrupt - fall through to error page
		}
	}

	// Cannot redirect to client, show generic error page.
	// Detailed error information is logged above for server-side diagnostics.
	http.Error(w, "upstream authentication failed", http.StatusBadGateway)
}
