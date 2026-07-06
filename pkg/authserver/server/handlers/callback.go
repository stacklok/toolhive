// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/auth"
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

	// Place the resolved canonical user in the request context so callback storage
	// calls that carry no tokens argument — GetAllUpstreamTokens during chain
	// consistency, DeleteUpstreamTokens on cleanup — can resolve the user from
	// context. We use WithPlatformUser, not WithIdentity: no ToolHive bearer has
	// been issued at the callback, so there is no authenticated identity to assert —
	// only the canonical user for storage keying. (StoreUpstreamTokens does not need
	// this; it keys off tokens.UserID below.)
	ctx = auth.WithPlatformUser(ctx, subject)

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

	h.maybeCarryForwardRefreshToken(ctx, storageTokens, subject, providerSubject, providerID, result.Synthetic)

	if err := h.storage.StoreUpstreamTokens(ctx, sessionID, providerID, storageTokens); err != nil {
		slog.Error("failed to store upstream tokens",
			"error", err,
		)
		// Clean up any tokens stored by earlier legs of a multi-upstream chain.
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to store session"))
		return
	}

	// Build the credential-free principal for the optional upstream filter, keyed on
	// the identity the first leg just established. providerSubject is the claim-mapped
	// upstream subject; subject is the canonical ToolHive user ID. On subsequent legs
	// the filter is not consulted, so this only drives filtering for upstreams[0].
	// result.Claims carries the ID-token/userinfo claims (nil for synthetic upstreams).
	principal := auth.PrincipalInfo{
		Subject:        providerSubject,
		PlatformUserID: subject,
		Name:           userName,
		Email:          userEmail,
		Claims:         result.Claims,
	}

	h.continueChainOrComplete(ctx, w, req, ar, pending, sessionID, principal)
}

// maybeCarryForwardRefreshToken preserves a prior refresh token when the upstream IdP
// omits refresh_token on re-authorization (a common behavior, e.g. Google without
// prompt=consent). Without this, the new row would be written with an empty RefreshToken,
// orphaning the previously-issued RT and forcing the next refresh attempt to fail.
// Mirrors the preservation pattern in upstreamTokenRefresher.RefreshAndStore.
//
// The UpstreamSubject == providerSubject guard is defense-in-depth against account-linking
// edge cases where one internal user might have two distinct upstream subjects on the
// same provider. It is skipped for synthetic providers: a synthetic identity (OAuth2 with
// no userinfo/identity config) mints a fresh rotating subject every flow, so the equality
// can never hold and would block carry-forward entirely. There is no stable upstream
// subject to link by in that case, so the guard protects nothing — gating on a non-empty
// prior refresh token is sufficient.
//
// storageTokens is mutated in-place only when a carry-forward is warranted.
func (h *Handler) maybeCarryForwardRefreshToken(
	ctx context.Context,
	storageTokens *storage.UpstreamTokens,
	subject, providerSubject, providerID string,
	synthetic bool,
) {
	if storageTokens.RefreshToken != "" {
		return
	}
	prior, err := h.storage.GetLatestUpstreamTokensForUser(ctx, subject, providerID)
	switch {
	case err == nil:
		// Defensive: the contract returns ErrNotFound (handled below) on a miss
		// rather than a nil row, but guard anyway so a (nil, nil) return — or an
		// empty prior refresh token — can never be carried forward or panic. This
		// runs before the synthetic branch precisely so "synthetic" can never imply
		// "carry forward from a non-existent prior row".
		if prior == nil || prior.RefreshToken == "" {
			return
		}
		// Non-synthetic: defense-in-depth account-linking guard requires the prior
		// row's upstream subject to match. Synthetic providers mint a fresh rotating
		// subject every flow, so that equality can never hold and there is no stable
		// subject to link by — skip the guard and rely on the prior RT alone.
		if synthetic || prior.UpstreamSubject == providerSubject {
			storageTokens.RefreshToken = prior.RefreshToken
			slog.Debug("preserved upstream refresh token from prior row",
				"user_id", subject, "provider_id", providerID, "synthetic", synthetic,
			)
		}
	case errors.Is(err, storage.ErrNotFound):
		// First authorization for this user/provider — nothing to preserve.
	default:
		// Storage error — log and continue with empty RT. Failing the callback
		// would be a worse user experience than the (already broken) status quo.
		slog.Warn("failed to look up prior upstream tokens for RT preservation",
			"error", err, "user_id", subject, "provider_id", providerID,
		)
	}
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
			// On a subsequent leg the resolved user is carried in pending.ResolvedUserID;
			// place it in ctx via WithPlatformUser so a context-keyed storage decorator can
			// resolve the canonical user for this delete (DeleteUpstreamTokens takes no tokens
			// argument). WithPlatformUser, not WithIdentity: there is no authenticated identity
			// at the callback, only the storage-scoped user. A first-leg error has no resolved
			// user and no earlier-leg tokens to clean up, so the bare ctx is correct there.
			if pending.SessionID != "" {
				cleanupCtx := ctx
				if pending.ResolvedUserID != "" {
					cleanupCtx = auth.WithPlatformUser(ctx, pending.ResolvedUserID)
				}
				_ = h.storage.DeleteUpstreamTokens(cleanupCtx, pending.SessionID)
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
	principal auth.PrincipalInfo,
) {
	// subject is the canonical ToolHive user ID used for chain state, token keying,
	// and the cross-leg identity check. Note this is principal.PlatformUserID, NOT
	// principal.Subject (which is the upstream provider's subject).
	subject := principal.PlatformUserID
	name := principal.Name
	email := principal.Email

	// SingleLeg authorizations intentionally bypass chain continuation: the caller
	// scoped this flow to one specific upstream (e.g. a UI-initiated "connect one
	// backend" request), so other configured-but-tokenless upstreams must not
	// hijack it into a full chain walk. Issue the authorization code immediately.
	//
	// On failure we deliberately do NOT delete the stored upstream tokens. The
	// leg's token was already fetched and stored validly before we got here; the
	// errors writeAuthorizationResponse can return are all server-side and
	// unrelated to that credential (client lookup, redirect-URI parse, or fosite
	// failing to mint the authorization code). Wiping a good upstream token would
	// force a needless re-auth on what is a retryable error, so we keep it and
	// just surface the failure to the client.
	if pending.SingleLeg {
		if err := h.writeAuthorizationResponse(ctx, w, pending, sessionID, subject, name, email); err != nil {
			slog.Error("failed to create authorization response", "error", err)
			h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to create authorization code"))
		}
		return
	}

	// Resolve the effective chain of upstreams to walk. The first leg computes it
	// (consulting the optional filter with this leg's request context); every
	// subsequent leg reuses the validated chain the first leg carried forward, so
	// the filter is not re-run per leg. A subsequent leg whose pending predates the
	// chain is rejected rather than recomputed against a later leg's context.
	chain, err := h.resolveChain(ctx, pending, principal)
	if err != nil {
		slog.Error("failed to resolve upstream chain", "error", err)
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to determine authorization chain"))
		return
	}

	nextProvider, err := h.nextMissingUpstream(ctx, sessionID, chain)
	if err != nil {
		slog.Error("failed to determine next upstream", "error", err)
		_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to check authorization chain state"))
		return
	}

	if nextProvider == "" {
		if err := h.verifyChainIdentity(ctx, sessionID, chain, subject); err != nil {
			// verifyChainIdentity already logged the specific cause (with structured
			// fields for a mismatch); here we just clean up and fail closed.
			_ = h.storage.DeleteUpstreamTokens(ctx, sessionID)
			h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("identity verification failed"))
			return
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
		// Carry the effective chain forward so the filter is computed once, on the
		// first leg, and reused for every subsequent leg.
		ChainUpstreams: chain,
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

// verifyChainIdentity is a defense-in-depth check run once every leg of the
// effective chain is satisfied. Despite the "chain" framing, it reconciles only
// the first leg: it confirms the identity provider's stored token (chain[0]) still
// belongs to the subject carried through the flow. Intermediate/later legs are
// deliberately NOT identity-checked — those are connect-this-backend flows whose
// upstream identity can legitimately differ from the first leg's user.
//
// subject MUST be the canonical ToolHive user ID resolved from the first leg's
// upstream via that provider's configured subject-claim mapping (the OIDC
// SubjectClaim, or the "sub" claim by default) and carried forward unchanged
// through PendingAuthorization.ResolvedUserID. The caller resolves it exactly once,
// on the first leg, from the claim-mapped upstream subject. This cross-checks it
// against firstTokens.UserID — the same canonical ID persisted when the first leg
// stored its tokens — so a first leg whose stored user disagrees with the carried
// subject is rejected.
//
// It gates on the effective chain rather than the raw config: chain[0] is always
// the first (required) upstream, so a chain the filter narrowed to just that
// upstream has no first-leg cross-check to run and the check is a no-op. Returns a
// non-nil error when the storage lookup fails or an identity mismatch is detected;
// the caller maps either to a server error.
func (h *Handler) verifyChainIdentity(ctx context.Context, sessionID string, chain []string, subject string) error {
	if len(chain) <= 1 {
		return nil
	}
	allTokens, err := h.storage.GetAllUpstreamTokens(ctx, sessionID)
	if err != nil {
		slog.Error("failed to load upstream tokens for chain identity check", "error", err)
		return fmt.Errorf("failed to load upstream tokens for identity check: %w", err)
	}
	firstProvider := chain[0]
	if firstTokens, ok := allTokens[firstProvider]; ok && firstTokens.UserID != subject {
		// Emit the mismatch as discrete slog fields — not folded into the returned
		// error string — so log pipelines can filter/alert on this defense-in-depth
		// check, matching the logging from before this check was extracted here.
		slog.Error("identity mismatch between chain state and stored tokens",
			"expected", subject,
			"got", firstTokens.UserID,
			"provider", firstProvider,
		)
		return fmt.Errorf("identity mismatch on provider %q", firstProvider)
	}
	return nil
}
