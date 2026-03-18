// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"crypto/rand"
	"log/slog"
	"net/http"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// upstreamAuthSecrets holds cryptographic values needed for upstream IDP authorization.
type upstreamAuthSecrets struct {
	// State is the internal state for correlating the upstream callback.
	State string
	// PKCEVerifier is the code_verifier for upstream PKCE (RFC 7636).
	PKCEVerifier string
	// PKCEChallenge is the code_challenge derived from PKCEVerifier.
	PKCEChallenge string
	// Nonce is the OIDC nonce for ID token replay protection.
	Nonce string
}

// newUpstreamAuthSecrets generates all cryptographic secrets needed for upstream authorization.
func newUpstreamAuthSecrets() *upstreamAuthSecrets {
	verifier := crypto.GeneratePKCEVerifier()
	return &upstreamAuthSecrets{
		State:         rand.Text(),
		PKCEVerifier:  verifier,
		PKCEChallenge: crypto.ComputePKCEChallenge(verifier),
		Nonce:         rand.Text(),
	}
}

// AuthorizeHandler handles GET /oauth/authorize requests.
// It validates the client's authorization request and redirects to the upstream IDP.
func (h *Handler) AuthorizeHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Let fosite validate everything: client_id, redirect_uri, response_type, PKCE, scopes
	ar, err := h.provider.NewAuthorizeRequest(ctx, req)
	if err != nil {
		h.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}

	// Extract validated data from the authorize request
	clientID := ar.GetClient().GetID()
	redirectURI := ar.GetRedirectURI().String()
	state := ar.GetState()
	codeChallenge := ar.GetRequestForm().Get("code_challenge")
	codeChallengeMethod := ar.GetRequestForm().Get("code_challenge_method")
	scopes := []string(ar.GetRequestedScopes())

	// Check if upstream provider is configured
	if h.upstream == nil {
		slog.Error("upstream provider not configured")
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("authorization server not configured"))
		return
	}

	slog.Debug("parsed client-requested scopes", //nolint:gosec // G706: scope count is an integer
		"scope_count", len(scopes),
	)

	// Generate secrets for upstream authorization
	secrets := newUpstreamAuthSecrets()

	// Create and store pending authorization
	pending := &storage.PendingAuthorization{
		ClientID:             clientID,
		RedirectURI:          redirectURI,
		State:                state,
		PKCEChallenge:        codeChallenge,
		PKCEMethod:           codeChallengeMethod,
		Scopes:               scopes,
		InternalState:        secrets.State,
		UpstreamPKCEVerifier: secrets.PKCEVerifier,
		UpstreamNonce:        secrets.Nonce,
		CreatedAt:            time.Now(),
	}

	if err := h.storage.StorePendingAuthorization(ctx, secrets.State, pending); err != nil {
		slog.Error("failed to store pending authorization",
			"error", err,
		)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to store authorization request"))
		return
	}

	// Build upstream authorization URL with PKCE challenge
	// Add nonce for OIDC providers that support ID token validation
	var authOpts []upstream.AuthorizationOption
	if secrets.Nonce != "" {
		authOpts = append(authOpts, upstream.WithAdditionalParams(map[string]string{"nonce": secrets.Nonce}))
	}
	upstreamURL, err := h.upstream.AuthorizationURL(secrets.State, secrets.PKCEChallenge, authOpts...)
	if err != nil {
		slog.Error("failed to build upstream authorization URL",
			"error", err,
		)
		// Clean up pending authorization
		_ = h.storage.DeletePendingAuthorization(ctx, secrets.State)
		h.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("failed to build authorization URL"))
		return
	}

	// Redirect user to upstream IDP
	http.Redirect(w, req, upstreamURL, http.StatusFound)
}
