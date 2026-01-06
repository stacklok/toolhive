// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package oauth provides OAuth 2.0 authorization server components including
// handlers for authorization, token exchange, and dynamic client registration.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/idp"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// AuthorizeHandler handles GET /oauth/authorize requests.
// It validates the client's authorization request and redirects to the upstream IDP.
func (r *Router) AuthorizeHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Parse query parameters
	clientID := req.URL.Query().Get("client_id")
	redirectURI := req.URL.Query().Get("redirect_uri")
	state := req.URL.Query().Get("state")
	codeChallenge := req.URL.Query().Get("code_challenge")
	codeChallengeMethod := req.URL.Query().Get("code_challenge_method")
	scope := req.URL.Query().Get("scope")
	responseType := req.URL.Query().Get("response_type")

	// Validate required parameters
	if clientID == "" {
		r.writeAuthorizeError(w, "invalid_request", "client_id is required")
		return
	}

	if redirectURI == "" {
		r.writeAuthorizeError(w, "invalid_request", "redirect_uri is required")
		return
	}

	// Validate client exists
	client, err := r.storage.GetClient(ctx, clientID)
	if err != nil {
		r.logger.WarnContext(ctx, "client not found",
			slog.String("client_id", clientID),
			slog.String("error", err.Error()),
		)
		r.writeAuthorizeError(w, "invalid_request", "client not found")
		return
	}

	// Validate redirect_uri matches client's registered URIs
	if !isValidRedirectURI(client, redirectURI) {
		r.logger.WarnContext(ctx, "invalid redirect_uri",
			slog.String("client_id", clientID),
			slog.String("redirect_uri", redirectURI),
		)
		r.writeAuthorizeError(w, "invalid_request", "redirect_uri does not match registered URIs")
		return
	}

	// From here on, we can redirect errors to the client's redirect_uri
	// Validate response_type is "code"
	if responseType != "code" {
		r.redirectWithError(w, redirectURI, state, "unsupported_response_type", "only response_type=code is supported")
		return
	}

	// Validate PKCE for public clients (required)
	if client.IsPublic() {
		if codeChallenge == "" {
			r.redirectWithError(w, redirectURI, state, "invalid_request", "code_challenge is required for public clients")
			return
		}
		if codeChallengeMethod != "S256" {
			r.redirectWithError(w, redirectURI, state, "invalid_request", "code_challenge_method must be S256")
			return
		}
	}

	// Validate state is present (recommended by OAuth 2.0 spec)
	if state == "" {
		r.logger.WarnContext(ctx, "authorization request missing state parameter",
			slog.String("client_id", clientID),
		)
	}

	// Check if upstream provider is configured
	if r.upstream == nil {
		r.logger.ErrorContext(ctx, "upstream provider not configured")
		r.redirectWithError(w, redirectURI, state, "server_error", "authorization server not configured")
		return
	}

	// Parse scopes
	var scopes []string
	if scope != "" {
		scopes = strings.Split(scope, " ")
	}

	r.logger.DebugContext(ctx, "parsed client-requested scopes",
		slog.String("raw_scope_param", scope),
		slog.Any("parsed_scopes", scopes),
	)

	// Generate internal state, upstream PKCE verifier/challenge, and nonce
	internalState, upstreamPKCEVerifier, upstreamPKCEChallenge, upstreamNonce, err := generateAuthorizationSecrets()
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to generate authorization secrets",
			slog.String("error", err.Error()),
		)
		r.redirectWithError(w, redirectURI, state, "server_error", "failed to generate authorization secrets")
		return
	}

	// Create and store pending authorization
	pending := &storage.PendingAuthorization{
		ClientID:             clientID,
		RedirectURI:          redirectURI,
		State:                state,
		PKCEChallenge:        codeChallenge,
		PKCEMethod:           codeChallengeMethod,
		Scopes:               scopes,
		InternalState:        internalState,
		UpstreamPKCEVerifier: upstreamPKCEVerifier,
		UpstreamNonce:        upstreamNonce,
		CreatedAt:            time.Now(),
	}

	if err := r.storage.StorePendingAuthorization(ctx, internalState, pending); err != nil {
		r.logger.ErrorContext(ctx, "failed to store pending authorization",
			slog.String("error", err.Error()),
		)
		r.redirectWithError(w, redirectURI, state, "server_error", "failed to store authorization request")
		return
	}

	// Build upstream authorization URL with PKCE challenge and nonce
	upstreamURL, err := r.upstream.AuthorizationURL(internalState, upstreamPKCEChallenge, upstreamNonce)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to build upstream authorization URL",
			slog.String("error", err.Error()),
		)
		// Clean up pending authorization
		_ = r.storage.DeletePendingAuthorization(ctx, internalState)
		r.redirectWithError(w, redirectURI, state, "server_error", "failed to build authorization URL")
		return
	}

	// Log upstream authorization URL for debugging
	r.logger.DebugContext(ctx, "upstream authorization URL",
		slog.String("url", upstreamURL),
	)
	// Log the redirect_uri separately for clarity (best effort - URL was just constructed so should be valid)
	parsedUpstreamURL, _ := url.Parse(upstreamURL)
	r.logger.DebugContext(ctx, "upstream redirect_uri",
		slog.String("redirect_uri", parsedUpstreamURL.Query().Get("redirect_uri")),
	)

	r.logger.InfoContext(ctx, "redirecting to upstream IDP",
		slog.String("client_id", clientID),
		slog.String("upstream_provider", r.upstream.Name()),
		slog.String("upstream_redirect_uri", parsedUpstreamURL.Query().Get("redirect_uri")),
	)

	// Redirect user to upstream IDP
	http.Redirect(w, req, upstreamURL, http.StatusFound)
}

// CallbackHandler handles GET /oauth/callback requests.
// It exchanges the upstream authorization code and issues our own authorization code.
func (r *Router) CallbackHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Parse query parameters
	code := req.URL.Query().Get("code")
	internalState := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")
	errorDescription := req.URL.Query().Get("error_description")

	// Handle upstream errors
	if errorParam != "" {
		r.logger.WarnContext(ctx, "upstream IDP returned error",
			slog.String("error", errorParam),
			slog.String("error_description", errorDescription),
		)

		// Try to load pending authorization to redirect to client
		if internalState != "" {
			pending, err := r.storage.LoadPendingAuthorization(ctx, internalState)
			if err == nil {
				_ = r.storage.DeletePendingAuthorization(ctx, internalState)
				r.redirectWithError(w, pending.RedirectURI, pending.State, errorParam, errorDescription)
				return
			}
		}

		// Cannot redirect to client, show error page
		http.Error(w, "upstream authentication failed: "+errorParam, http.StatusBadGateway)
		return
	}

	// Validate required parameters
	if internalState == "" {
		r.logger.WarnContext(ctx, "callback missing state parameter")
		http.Error(w, "missing state parameter", http.StatusBadRequest)
		return
	}

	if code == "" {
		r.logger.WarnContext(ctx, "callback missing code parameter")
		http.Error(w, "missing code parameter", http.StatusBadRequest)
		return
	}

	// Load and delete pending authorization (single-use)
	pending, err := r.storage.LoadPendingAuthorization(ctx, internalState)
	if err != nil {
		r.logger.WarnContext(ctx, "pending authorization not found",
			slog.String("state", internalState),
			slog.String("error", err.Error()),
		)
		http.Error(w, "authorization request not found or expired", http.StatusBadRequest)
		return
	}

	// Delete pending authorization immediately (single-use)
	if err := r.storage.DeletePendingAuthorization(ctx, internalState); err != nil {
		r.logger.WarnContext(ctx, "failed to delete pending authorization",
			slog.String("state", internalState),
			slog.String("error", err.Error()),
		)
	}

	// Check if upstream provider is configured
	if r.upstream == nil {
		r.logger.ErrorContext(ctx, "upstream provider not configured")
		r.redirectWithError(w, pending.RedirectURI, pending.State, "server_error", "authorization server not configured")
		return
	}

	// Exchange code with upstream IDP using the stored PKCE verifier
	idpTokens, err := r.upstream.ExchangeCode(ctx, code, pending.UpstreamPKCEVerifier)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to exchange code with upstream IDP",
			slog.String("error", err.Error()),
		)
		r.redirectWithError(w, pending.RedirectURI, pending.State, "server_error", "failed to exchange authorization code")
		return
	}

	// Validate ID token nonce and extract subject for UserInfo validation.
	idTokenSubject := r.validateIDTokenAndExtractSubject(ctx, idpTokens, pending)

	// Get user info from upstream IDP with subject validation per OIDC Core Section 5.3.4.
	userInfo := r.fetchUserInfoWithValidation(ctx, idpTokens.AccessToken, idTokenSubject)

	// Generate session ID and store IDP tokens
	sessionID, err := generateRandomState()
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to generate session ID",
			slog.String("error", err.Error()),
		)
		r.redirectWithError(w, pending.RedirectURI, pending.State, "server_error", "failed to generate session")
		return
	}

	// Convert IDP tokens to storage tokens with binding fields
	storageTokens := &storage.IDPTokens{
		AccessToken:  idpTokens.AccessToken,
		RefreshToken: idpTokens.RefreshToken,
		IDToken:      idpTokens.IDToken,
		ExpiresAt:    idpTokens.ExpiresAt,
		ClientID:     pending.ClientID,
	}
	if userInfo != nil {
		storageTokens.Subject = userInfo.Subject
	}

	if err := r.storage.StoreIDPTokens(ctx, sessionID, storageTokens); err != nil {
		r.logger.ErrorContext(ctx, "failed to store IDP tokens",
			slog.String("error", err.Error()),
		)
		r.redirectWithError(w, pending.RedirectURI, pending.State, "server_error", "failed to store session")
		return
	}

	// Create our authorization code using fosite
	ourCode, err := r.createAuthorizationCode(ctx, pending, sessionID, userInfo)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to create authorization code",
			slog.String("error", err.Error()),
		)
		// Clean up stored IDP tokens
		_ = r.storage.DeleteIDPTokens(ctx, sessionID)
		r.redirectWithError(w, pending.RedirectURI, pending.State, "server_error", "failed to create authorization code")
		return
	}

	r.logger.InfoContext(ctx, "authorization successful, redirecting to client",
		slog.String("client_id", pending.ClientID),
	)

	// Build client callback URL with our code and client's original state
	clientCallback := buildCallbackURL(pending.RedirectURI, ourCode, pending.State)

	// Redirect to client
	http.Redirect(w, req, clientCallback, http.StatusFound)
}

// createAuthorizationCode creates a fosite authorization code for the client.
func (r *Router) createAuthorizationCode(
	ctx context.Context,
	pending *storage.PendingAuthorization,
	sessionID string,
	userInfo *idp.UserInfo,
) (string, error) {
	// Get the client from storage
	client, err := r.storage.GetClient(ctx, pending.ClientID)
	if err != nil {
		return "", err
	}

	// Determine subject from user info
	subject := ""
	if userInfo != nil {
		subject = userInfo.Subject
	}

	// Create the session with IDP session reference and client ID for binding
	session := NewSession(subject, sessionID, pending.ClientID)
	if userInfo != nil && userInfo.Email != "" {
		session.SetUsername(userInfo.Email)
	}

	// Set expiration times
	now := time.Now()
	session.SetExpiresAt(fosite.AuthorizeCode, now.Add(r.config.AuthorizeCodeLifespan))
	session.SetExpiresAt(fosite.AccessToken, now.Add(r.config.AccessTokenLifespan))
	session.SetExpiresAt(fosite.RefreshToken, now.Add(r.config.RefreshTokenLifespan))

	// Build the authorization request form
	form := url.Values{
		"redirect_uri":          {pending.RedirectURI},
		"code_challenge":        {pending.PKCEChallenge},
		"code_challenge_method": {pending.PKCEMethod},
	}

	// Create an authorize request using fosite
	authorizeRequest := fosite.NewAuthorizeRequest()
	authorizeRequest.Form = form
	authorizeRequest.Client = client
	authorizeRequest.Session = session
	authorizeRequest.RequestedAt = now
	authorizeRequest.RedirectURI, _ = url.Parse(pending.RedirectURI)
	authorizeRequest.ResponseTypes = fosite.Arguments{"code"}

	// Set requested scopes
	for _, scope := range pending.Scopes {
		authorizeRequest.RequestedScope = append(authorizeRequest.RequestedScope, scope)
		authorizeRequest.GrantedScope = append(authorizeRequest.GrantedScope, scope)
	}

	// Generate the authorization response using fosite
	response, err := r.provider.NewAuthorizeResponse(ctx, authorizeRequest, session)
	if err != nil {
		return "", err
	}

	// Extract the authorization code from the response
	code := response.GetCode()
	if code == "" {
		return "", fosite.ErrServerError.WithHint("no authorization code generated")
	}

	return code, nil
}

// writeAuthorizeError writes an error response when we cannot redirect to the client.
func (*Router) writeAuthorizeError(w http.ResponseWriter, errorCode, description string) {
	// Cannot redirect to client, return an error page
	_ = errorCode // errorCode could be logged or included in the response
	http.Error(w, description, http.StatusBadRequest)
}

// redirectWithError redirects to the client with an error response.
func (*Router) redirectWithError(w http.ResponseWriter, redirectURI, state, errorCode, description string) {
	if redirectURI == "" {
		http.Error(w, description, http.StatusBadRequest)
		return
	}

	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect URI", http.StatusBadRequest)
		return
	}

	q := u.Query()
	q.Set("error", errorCode)
	if description != "" {
		q.Set("error_description", description)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()

	// Use manual redirect header instead of http.Redirect to avoid needing request
	w.Header().Set("Location", u.String())
	w.WriteHeader(http.StatusFound)
}

// generateRandomState generates a cryptographically secure random state string.
func generateRandomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateAuthorizationSecrets generates the internal state for callback correlation,
// the PKCE verifier/challenge pair for upstream IDP authorization (RFC 7636),
// and the nonce for ID Token replay protection (OIDC Core Section 3.1.2.1).
func generateAuthorizationSecrets() (internalState, pkceVerifier, pkceChallenge, nonce string, err error) {
	internalState, err = generateRandomState()
	if err != nil {
		return "", "", "", "", err
	}
	pkceVerifier, err = GeneratePKCEVerifier()
	if err != nil {
		return "", "", "", "", err
	}
	pkceChallenge = ComputePKCEChallenge(pkceVerifier)
	// Generate nonce for OIDC ID Token replay protection
	nonce, err = generateRandomState()
	if err != nil {
		return "", "", "", "", err
	}
	return internalState, pkceVerifier, pkceChallenge, nonce, nil
}

// isValidRedirectURI checks if the redirect URI matches one of the client's registered URIs.
// For LoopbackClient instances, uses RFC 8252 Section 7.3 compliant loopback matching.
func isValidRedirectURI(client fosite.Client, redirectURI string) bool {
	// Check if client supports loopback matching (RFC 8252)
	if loopbackClient, ok := client.(*LoopbackClient); ok {
		return loopbackClient.MatchRedirectURI(redirectURI)
	}

	// Fall back to exact string matching for other clients
	registeredURIs := client.GetRedirectURIs()
	for _, uri := range registeredURIs {
		if uri == redirectURI {
			return true
		}
	}
	return false
}

// buildCallbackURL builds the callback URL with code and state parameters.
func buildCallbackURL(redirectURI, code, state string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}

	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()

	return u.String()
}

// validateIDTokenAndExtractSubject validates the ID token nonce if present and extracts
// the subject claim for UserInfo validation per OIDC Core Section 5.3.4.
// Returns the subject from the ID token, or empty string if validation fails or no ID token.
func (r *Router) validateIDTokenAndExtractSubject(
	ctx context.Context,
	idpTokens *idp.Tokens,
	pending *storage.PendingAuthorization,
) string {
	// Skip if no ID token or nonce
	if idpTokens.IDToken == "" || pending.UpstreamNonce == "" {
		return ""
	}

	// Check if upstream provider supports nonce validation
	oidcProvider, ok := r.upstream.(idp.IDTokenNonceValidator)
	if !ok {
		return ""
	}

	// Validate ID token nonce per OIDC Core Section 3.1.3.7 step 11
	claims, err := oidcProvider.ValidateIDTokenWithNonce(idpTokens.IDToken, pending.UpstreamNonce)
	if err != nil {
		r.logger.WarnContext(ctx, "ID token nonce validation failed",
			slog.String("error", err.Error()),
		)
		// Log warning but don't fail - signature verification not yet implemented
		// Once signature verification is added, this should be a hard failure
		return ""
	}

	if claims == nil {
		return ""
	}

	return claims.Subject
}

// fetchUserInfoWithValidation fetches user info from the upstream IDP with optional
// subject validation per OIDC Core Section 5.3.4.
// If idTokenSubject is provided and the provider supports validation, validates that
// the UserInfo subject matches the ID token subject to prevent user impersonation.
// Returns nil if fetching fails (logged as warning, not fatal).
func (r *Router) fetchUserInfoWithValidation(
	ctx context.Context,
	accessToken string,
	idTokenSubject string,
) *idp.UserInfo {
	var userInfo *idp.UserInfo
	var err error

	if idTokenSubject != "" {
		// Use subject validation if provider supports it
		if validator, ok := r.upstream.(idp.UserInfoSubjectValidator); ok {
			userInfo, err = validator.UserInfoWithSubjectValidation(ctx, accessToken, idTokenSubject)
			if err != nil {
				r.logger.WarnContext(ctx, "failed to get user info with subject validation",
					slog.String("error", err.Error()),
				)
				return nil
			}
			return userInfo
		}
	}

	// Fall back to regular UserInfo without validation
	userInfo, err = r.upstream.UserInfo(ctx, accessToken)
	if err != nil {
		r.logger.WarnContext(ctx, "failed to get user info from upstream IDP",
			slog.String("error", err.Error()),
		)
		return nil
	}

	return userInfo
}
