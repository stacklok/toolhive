// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
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

	// Look up the upstream provider that was used for this authorization leg.
	// Validating against pending.UpstreamProviderName (set during authorize) provides
	// IDP mix-up defense: we only accept callbacks for the provider we redirected to.
	upstreamProvider, ok := h.upstreamByName(pending.UpstreamProviderName)
	if !ok {
		slog.Error("upstream provider not found", "provider", pending.UpstreamProviderName)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("upstream provider not configured"))
		return
	}

	// Exchange code and resolve identity in a single atomic operation.
	// This ensures OIDC nonce validation cannot be accidentally skipped.
	result, err := upstreamProvider.ExchangeCodeForIdentity(ctx, code, pending.UpstreamPKCEVerifier, pending.UpstreamNonce)
	if err != nil {
		slog.Error("failed to exchange code or resolve identity",
			"error", err,
		)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to exchange authorization code"))
		return
	}

	idpTokens := result.Tokens
	providerSubject := result.Subject

	// Use the logical upstream name as the provider identifier for storage and identity lookups.
	// This ensures write-side (StoreUpstreamTokens) and read-side (GetUpstreamTokens) keys match.
	providerID := pending.UpstreamProviderName

	// Use the session ID from the pending authorization.
	// This was generated in authorize.go and will be reused across all legs of the chain.
	sessionID := pending.SessionID

	// Determine identity: first leg resolves from upstream, subsequent legs
	// carry from pending. Synthetic identities (see upstream.Identity.Synthetic)
	// bypass UserResolver — the synthesized subject rotates per re-auth and
	// would otherwise grow `users` monotonically. We use the synthesized value
	// directly as an ephemeral session key and skip the LastAuthenticated
	// update (no provider_identities row to bump).
	var subject, userName, userEmail string
	if pending.ResolvedUserID == "" {
		// First leg — this is the identity provider
		if result.Synthetic {
			subject = result.Subject
		} else {
			user, err := h.userResolver.ResolveUser(ctx, providerID, providerSubject)
			if err != nil {
				slog.Error("failed to resolve user", "error", err)
				h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to resolve user"))
				return
			}
			subject = user.ID
			h.userResolver.UpdateLastAuthenticated(ctx, providerID, providerSubject)
		}
		userName = result.Name
		userEmail = result.Email
	} else {
		// Subsequent leg — use identity carried from first leg
		subject = pending.ResolvedUserID
		userName = pending.ResolvedUserName
		userEmail = pending.ResolvedUserEmail
		if !result.Synthetic {
			h.userResolver.UpdateLastAuthenticated(ctx, providerID, providerSubject)
		}
	}

	// Convert IDP tokens to storage tokens with binding fields.
	// SessionExpiresAt is set unconditionally as the Fosite session bound. Storage
	// backends use it as a fallback storage lifetime when ExpiresAt is zero (a
	// non-expiring upstream token). Setting it on every write — even when ExpiresAt
	// is non-zero — protects the refresh path: if the upstream provider stops
	// asserting expires_in on a later refresh, the carried-forward SessionExpiresAt
	// still bounds the storage lifetime instead of leaving the row indefinitely.
	storageTokens := &storage.UpstreamTokens{
		ProviderID:       providerID,
		AccessToken:      idpTokens.AccessToken,
		RefreshToken:     idpTokens.RefreshToken,
		IDToken:          idpTokens.IDToken,
		ExpiresAt:        idpTokens.ExpiresAt,
		SessionExpiresAt: time.Now().Add(h.config.RefreshTokenLifespan),
		ClientID:         pending.ClientID,
		UserID:           subject,         // Internal ToolHive user ID
		UpstreamSubject:  providerSubject, // Upstream IDP's subject claim
	}

	if err := h.storage.StoreUpstreamTokens(ctx, sessionID, providerID, storageTokens); err != nil {
		slog.Error("failed to store upstream tokens",
			"error", err,
		)
		// Clean up any tokens stored by earlier legs of a multi-upstream chain.
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to store session"))
		return
	}

	h.continueChainOrComplete(ctx, w, req, ar, pending, sessionID, subject, userName, userEmail)
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
	name string,
	email string,
) error {
	// Get the client from storage
	fositeClient, err := h.storage.GetClient(ctx, pending.ClientID)
	if err != nil {
		return err
	}

	// Create the session with IDP session reference, client ID, and user profile claims
	sess := session.New(subject, sessionID, pending.ClientID, session.UserClaims{
		Name:  name,
		Email: email,
	})

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
			slog.Warn("filtered unregistered scope from authorization", //nolint:gosec // G706 - scope from server-side storage
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
		slog.Error("stored redirect URI is invalid", //nolint:gosec // G706 - redirect URI from server-side storage
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
			// Clean up any upstream tokens stored by earlier legs of a multi-upstream chain.
			if pending.SessionID != "" {
				_ = h.storage.DeleteUpstreamTokens(ctx, pending.SessionID)
			}
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

// continueChainOrComplete checks whether all upstream providers in the authorization
// chain have been satisfied. If so, it issues the authorization code and redirects
// to the client. If not, it redirects to the next upstream provider to continue
// the chain. Called after StoreUpstreamTokens succeeds for each leg.
func (h *Handler) continueChainOrComplete(
	ctx context.Context,
	w http.ResponseWriter,
	req *http.Request,
	ar fosite.AuthorizeRequester,
	pending *storage.PendingAuthorization,
	sessionID string,
	subject string,
	name string,
	email string,
) {
	nextProvider, err := h.nextMissingUpstream(ctx, sessionID)
	if err != nil {
		slog.Error("failed to determine next upstream", "error", err)
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to check authorization chain state"))
		return
	}

	if nextProvider == "" {
		// Defense-in-depth: verify identity consistency across chain legs.
		// The subject was resolved from the first leg's upstream and carried through
		// PendingAuthorization. Cross-check it against the stored upstream tokens.
		if len(h.upstreams) > 1 {
			allTokens, err := h.storage.GetAllUpstreamTokens(ctx, sessionID)
			if err != nil {
				slog.Error("failed to verify identity consistency", "error", err)
				_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
				h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to verify identity consistency"))
				return
			}
			firstProvider := h.upstreams[0].Name
			if firstTokens, ok := allTokens[firstProvider]; ok && firstTokens.UserID != subject {
				slog.Error("identity mismatch between chain state and stored tokens",
					"expected", subject,
					"got", firstTokens.UserID,
					"provider", firstProvider,
				)
				_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
				h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("identity verification failed"))
				return
			}
		}

		// All upstreams satisfied — issue authorization code
		if err := h.writeAuthorizationResponse(ctx, w, pending, sessionID, subject, name, email); err != nil {
			slog.Error("failed to create authorization response", "error", err)
			_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
			h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to create authorization code"))
		}
		return
	}

	// Chain continues — redirect to next upstream.
	// TODO: If the user abandons the flow here (closes browser), upstream tokens from
	// completed legs remain in storage until their TTL expires. Add cascading cleanup
	// when pending authorizations expire to also delete associated upstream tokens.
	secrets := newUpstreamAuthSecrets()
	nextPending := &storage.PendingAuthorization{
		// Carry client request fields
		ClientID:      pending.ClientID,
		RedirectURI:   pending.RedirectURI,
		State:         pending.State,
		PKCEChallenge: pending.PKCEChallenge,
		PKCEMethod:    pending.PKCEMethod,
		Scopes:        pending.Scopes,
		// Fresh per-leg secrets
		InternalState:        secrets.State,
		UpstreamPKCEVerifier: secrets.PKCEVerifier,
		UpstreamNonce:        secrets.Nonce,
		// Chain state
		UpstreamProviderName: nextProvider,
		SessionID:            sessionID,
		// Carry resolved identity from first leg
		ResolvedUserID:    subject,
		ResolvedUserName:  name,
		ResolvedUserEmail: email,
		CreatedAt:         time.Now(),
	}

	if err := h.storage.StorePendingAuthorization(ctx, secrets.State, nextPending); err != nil {
		slog.Error("failed to store next chain leg", "error", err)
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to continue authorization chain"))
		return
	}

	// Build authorization URL for next upstream
	var authOpts []upstream.AuthorizationOption
	if secrets.Nonce != "" {
		authOpts = append(authOpts, upstream.WithAdditionalParams(map[string]string{"nonce": secrets.Nonce}))
	}
	nextUpstream, ok := h.upstreamByName(nextProvider)
	if !ok {
		slog.Error("next upstream provider not found", "provider", nextProvider)
		_ = h.storage.DeletePendingAuthorization(ctx, secrets.State)
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("upstream provider configuration error"))
		return
	}
	nextURL, err := nextUpstream.AuthorizationURL(secrets.State, secrets.PKCEChallenge, authOpts...)
	if err != nil {
		slog.Error("failed to build next upstream authorization URL", "error", err)
		_ = h.storage.DeletePendingAuthorization(ctx, secrets.State)
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to build authorization URL"))
		return
	}

	http.Redirect(w, req, nextURL, http.StatusFound)
}
