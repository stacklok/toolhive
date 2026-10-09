// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func (h *Handler) consentCookieName() string {
	issuer := h.config.GetAccessTokenIssuer()
	base := h.config.GetAuthorizationEndpointBaseURL()
	return "__Host-thv_consent_" + consentDigest(fmt.Sprintf("%d:%s%d:%s", len(issuer), issuer, len(base), base))
}

func consentDigest(secret string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
}

// coveringConsent returns only approvals matching the exact client request envelope.
// A storage failure is never treated as a missing approval.
func (h *Handler) coveringConsent(ctx context.Context, req *http.Request, client fosite.Client,
	redirectURI string, scopes []string, resource string) (*storage.ConsentSession, string, error) {
	session, digest, err := h.rememberedSession(ctx, req)
	if session == nil || err != nil {
		return nil, "", err
	}
	approval, err := h.rememberedApproval(ctx, session.UserID, client.GetID())
	if approval == nil || err != nil {
		return nil, "", err
	}
	if !approvalCovers(client, approval, redirectURI, scopes, resource) {
		return nil, "", nil
	}
	return session, digest, nil
}

// rememberedSession loads the live consent session named by the browser cookie.
// It returns nil without error when there is no usable session.
func (h *Handler) rememberedSession(ctx context.Context, req *http.Request) (*storage.ConsentSession, string, error) {
	if h.rememberedStorage == nil {
		return nil, "", nil
	}
	cookie, err := req.Cookie(h.consentCookieName())
	if errors.Is(err, http.ErrNoCookie) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if cookie.Value == "" {
		return nil, "", nil
	}
	digest := consentDigest(cookie.Value)
	session, err := h.rememberedStorage.GetConsentSession(ctx, digest)
	if isNotFoundOrExpired(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if session == nil || session.UserID == "" || session.ProviderSubject == "" || session.ProviderID == "" ||
		session.ExpiresAt.IsZero() {
		return nil, "", nil
	}
	if !time.Now().Before(session.ExpiresAt) {
		return nil, "", nil
	}
	if session.ProviderID != h.upstreams[0].Name {
		return nil, "", nil
	}
	return session, digest, nil
}

// rememberedApproval loads the live client approval for the user.
// It returns nil without error when there is no usable approval.
func (h *Handler) rememberedApproval(ctx context.Context, userID, clientID string) (*storage.ClientApproval, error) {
	approval, err := h.rememberedStorage.GetClientApproval(ctx, userID, clientID)
	if isNotFoundOrExpired(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if approval == nil || approval.ExpiresAt.IsZero() {
		return nil, nil
	}
	if !time.Now().Before(approval.ExpiresAt) {
		return nil, nil
	}
	return approval, nil
}

// approvalCovers reports whether approval spans the requested redirect URI
// (including the registered loopback-port rule), resource and scopes.
func approvalCovers(client fosite.Client, approval *storage.ClientApproval,
	redirectURI string, scopes []string, resource string) bool {
	redirectMatches := approval.RedirectURI == redirectURI
	if !redirectMatches {
		approvedRegistered, approvedOK := registration.RegisteredLoopbackRedirectURI(client, approval.RedirectURI)
		requestedRegistered, requestedOK := registration.RegisteredLoopbackRedirectURI(client, redirectURI)
		redirectMatches = approvedOK && requestedOK && approvedRegistered == requestedRegistered
	}
	if !redirectMatches || approval.Resource != resource {
		return false
	}
	for _, scope := range scopes {
		if !slices.Contains(approval.Scopes, scope) {
			return false
		}
	}
	return true
}

func (h *Handler) maybeRememberConsent(ctx context.Context, w http.ResponseWriter,
	pending *storage.PendingAuthorization, userID string) {
	if h.rememberedStorage == nil || !pending.RememberConsent || pending.FirstProviderSubject == "" || userID == "" {
		return
	}
	now := time.Now()
	approval := &storage.ClientApproval{
		RedirectURI: pending.RedirectURI, Scopes: slices.Clone(pending.Scopes), Resource: pending.Resource,
		ExpiresAt: now.Add(storage.ClientApprovalTTL),
	}
	if err := h.rememberedStorage.StoreClientApproval(ctx, userID, pending.ClientID, approval); err != nil {
		slog.Warn("failed to remember client approval", "error", err)
		return
	}
	secret := rand.Text()
	session := &storage.ConsentSession{
		UserID: userID, ProviderID: pending.UpstreamProviderName, ProviderSubject: pending.FirstProviderSubject,
		ExpiresAt: now.Add(storage.ConsentSessionTTL),
	}
	// The final leg's provider can differ from the first provider.
	if len(h.upstreams) > 0 {
		session.ProviderID = h.upstreams[0].Name
	}
	if err := h.rememberedStorage.StoreConsentSession(ctx, consentDigest(secret), session); err != nil {
		slog.Warn("failed to remember browser session", "error", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: h.consentCookieName(), Value: secret, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(storage.ConsentSessionTTL.Seconds()),
	})
}
