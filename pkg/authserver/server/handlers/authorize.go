// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto"
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

	// Back-channel-only clients must be indistinguishable from unknown clients
	// at this unauthenticated endpoint. This runs before redirect URI matching
	// (including the loopback matcher below), which would otherwise expose that
	// the client is configured through an invalid_request response.
	if h.rejectBackChannelOnlyAuthorizeClient(ctx, w, req) {
		return
	}

	// See rewriteLoopbackRedirectURI's doc comment for what this does and why.
	rewrittenFrom := h.rewriteLoopbackRedirectURI(ctx, req)

	// Let fosite validate everything: client_id, redirect_uri, response_type, PKCE, scopes
	ar, err := h.provider.NewAuthorizeRequest(ctx, req)
	// If the rewrite above fired, fosite validated (and stored) the
	// registered portless literal, not the dynamic port the client actually
	// requested. Wrapping ar restores the real listener as the error-redirect
	// target for every error path below -- see loopbackAuthorizeRequester's
	// doc comment for how -- not just a NewAuthorizeRequest failure.
	errAr := wrapLoopbackErrorRequester(ar, rewrittenFrom)
	if err != nil {
		h.provider.WriteAuthorizeError(ctx, w, errAr, err)
		return
	}

	// Extract validated data from the authorize request
	clientID := ar.GetClient().GetID()
	// Use the original requested redirect_uri (before the loopback rewrite above)
	// so the dynamic port survives into PendingAuthorization and everything
	// downstream. rewrittenFrom is only non-empty when a rewrite actually
	// happened; every other case falls through to ar.GetRedirectURI() unchanged.
	redirectURI := ar.GetRedirectURI().String()
	if rewrittenFrom != "" {
		redirectURI = rewrittenFrom
	}
	state := ar.GetState()
	codeChallenge := ar.GetRequestForm().Get("code_challenge")
	codeChallengeMethod := ar.GetRequestForm().Get("code_challenge_method")
	scopes := []string(ar.GetRequestedScopes())

	// Token-only servers have no interactive authorization flow. Validation above
	// deliberately still runs first, so client and redirect errors retain their
	// normal OAuth semantics.
	if h.tokenOnly {
		h.provider.WriteAuthorizeError(ctx, w, errAr, fosite.ErrUnsupportedResponseType)
		return
	}

	slog.Debug("authorize request received",
		"client_id", clientID,
		"redirect_uri", redirectURI,
		"scope_count", len(scopes),
	)

	// Generate secrets for upstream authorization
	secrets := newUpstreamAuthSecrets()

	// Create and store pending authorization.
	// SessionID is generated here at the start of the chain so it can be
	// threaded through all legs of a multi-upstream authorization flow.
	// The first leg always targets upstreams[0].
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
		UpstreamProviderName: h.upstreams[0].Name,
		SessionID:            rand.Text(),
		CreatedAt:            time.Now(),
	}

	if err := h.storage.StorePendingAuthorization(ctx, secrets.State, pending); err != nil {
		slog.Error("failed to store pending authorization",
			"error", err,
		)
		h.provider.WriteAuthorizeError(ctx, w, errAr, fosite.ErrServerError.WithHint("failed to store authorization request"))
		return
	}

	// Build upstream authorization URL with PKCE challenge
	// Add nonce for OIDC providers that support ID token validation
	var authOpts []upstream.AuthorizationOption
	if secrets.Nonce != "" {
		authOpts = append(authOpts, upstream.WithAdditionalParams(map[string]string{"nonce": secrets.Nonce}))
	}
	upstreamURL, err := h.upstreams[0].Provider.AuthorizationURL(secrets.State, secrets.PKCEChallenge, authOpts...)
	if err != nil {
		slog.Error("failed to build upstream authorization URL",
			"error", err,
		)
		// Clean up pending authorization
		_ = h.storage.DeletePendingAuthorization(ctx, secrets.State)
		h.provider.WriteAuthorizeError(ctx, w, errAr, fosite.ErrServerError.WithHint("failed to build authorization URL"))
		return
	}

	// Redirect user to upstream IDP
	http.Redirect(w, req, upstreamURL, http.StatusFound)
}

// rejectBackChannelOnlyAuthorizeClient rejects a configured client that has no
// authorization response types before fosite validates redirect_uri. It returns
// false for unknown clients so fosite retains responsibility for its normal
// client lookup and error handling.
func (h *Handler) rejectBackChannelOnlyAuthorizeClient(ctx context.Context, w http.ResponseWriter, req *http.Request) bool {
	if err := req.ParseForm(); err != nil {
		return false
	}
	clientID := req.Form.Get("client_id")
	if clientID == "" {
		return false
	}
	client, err := h.storage.GetClient(ctx, clientID)
	if err != nil || client == nil || !isBackChannelOnlyClient(client) {
		return false
	}
	authorizeRequest := fosite.NewAuthorizeRequest()
	authorizeRequest.Client = client
	h.provider.WriteAuthorizeError(
		ctx,
		w,
		authorizeRequest,
		fosite.ErrInvalidClient.WithHint("The requested OAuth 2.0 Client does not exist."),
	)
	return true
}

func isBackChannelOnlyClient(client fosite.Client) bool {
	return len(client.GetResponseTypes()) == 0 ||
		client.GetGrantTypes().ExactOne(oauthproto.GrantTypeTokenExchange)
}

// loopbackAuthorizeRequester wraps a fosite.AuthorizeRequester to make the
// client's real, dynamic-port loopback redirect_uri the error-redirect target
// for fosite's WriteAuthorizeError, instead of the registered portless
// literal that rewriteLoopbackRedirectURI substituted for validation.
//
// WriteAuthorizeError touches the requester only through the
// fosite.AuthorizeRequester interface (GetResponseMode, IsRedirectURIValid,
// GetRedirectURI, GetState, plus a G11NContext type-assertion covered below)
// -- it never type-asserts to the concrete *fosite.AuthorizeRequest -- so an
// embedding wrapper survives it intact.
//
// Overriding GetRedirectURI is the only way to restore the dynamic port:
// fosite.AuthorizeRequester has no SetRedirectURI method, and RedirectURI is
// a plain field on the concrete *fosite.AuthorizeRequest, so it can't be
// mutated back without a concrete-type assertion.
//
// Overriding IsRedirectURIValid is required alongside it: WriteAuthorizeError
// gates the redirect on this check, and fosite's own implementation cannot
// recognize "localhost" as loopback any more than the original /authorize
// request could -- it would reject the dynamic-port URI and fall back to a
// bare JSON body. The override only ever widens fosite's own answer with the
// loopback matcher's; see the method's doc comment for why it can't narrow.
//
// Known side effect: fosite's getLangFromRequester (i18n_helper.go) type-
// asserts the requester to fosite.G11NContext to read GetLang(), which lives
// on the embedded concrete *fosite.Request, not on the AuthorizeRequester
// interface -- so the wrapper always falls back to language.English. This is
// harmless here because no MessageCatalog is configured on the provider, but
// it's a real behavioural difference from an unwrapped requester.
type loopbackAuthorizeRequester struct {
	fosite.AuthorizeRequester
	// redirectURI is the client's real, requested dynamic-port URI.
	redirectURI *url.URL
}

// wrapLoopbackErrorRequester wraps ar in loopbackAuthorizeRequester when
// rewrittenFrom is non-empty (i.e. rewriteLoopbackRedirectURI fired), so a
// later validation failure redirects the error to the client's real listener
// instead of the registered portless placeholder. Returns ar unwrapped when
// no rewrite happened, or when rewrittenFrom fails to parse.
func wrapLoopbackErrorRequester(ar fosite.AuthorizeRequester, rewrittenFrom string) fosite.AuthorizeRequester {
	if rewrittenFrom == "" {
		return ar
	}
	parsed, err := url.Parse(rewrittenFrom)
	if err != nil {
		return ar
	}
	return &loopbackAuthorizeRequester{AuthorizeRequester: ar, redirectURI: parsed}
}

// IsRedirectURIValid may only ever WIDEN what fosite considers a valid
// redirect target, never narrow it: it first defers to the embedded
// requester's own check, and falls back to the loopback matcher only when
// that check says no. Narrowing would stop WriteAuthorizeError from
// delivering an error to a client whose redirect_uri fosite's own logic
// would have accepted on its own -- degrading a proper error redirect to a
// bare JSON body.
//
// The fallback adds exactly the "localhost" dynamic-port case fosite's own
// matcher cannot recognize (see rewriteLoopbackRedirectURI). It cannot be
// the sole check: registration.RegisteredLoopbackRedirectURI is
// public-clients-only (it early-returns false for any confidential client),
// so relying on it alone would reject confidential clients that fosite's
// own check would have validated.
func (r *loopbackAuthorizeRequester) IsRedirectURIValid() bool {
	if r.AuthorizeRequester.IsRedirectURIValid() {
		return true
	}
	client := r.GetClient()
	if client == nil {
		return false
	}
	_, ok := registration.RegisteredLoopbackRedirectURI(client, r.redirectURI.String())
	return ok
}

// GetRedirectURI returns a copy of redirectURI, never the stored pointer:
// WriteAuthorizeError mutates the returned *url.URL in place (clearing
// Fragment, overwriting RawQuery), which would otherwise corrupt
// r.redirectURI on first use.
func (r *loopbackAuthorizeRequester) GetRedirectURI() *url.URL {
	u := *r.redirectURI
	return &u
}

// logClientLookupFailure logs a GetClient failure from rewriteLoopbackRedirectURI
// at a level determined by the error: Debug for a not-found client (an
// unknown client_id is ordinary traffic on this unauthenticated endpoint --
// any caller can send a random client_id alongside a localhost redirect_uri,
// so logging every one at Warn would make log flooding trivial) and Warn for
// any other error, which indicates an actual backend problem worth surfacing.
//
// Both storage.ErrNotFound and fosite.ErrNotFound are checked: an opaque
// DCR-issued client_id not-found comes back wrapping storage.ErrNotFound, but
// a CIMD client_id (an https:// URL resolved live via
// CIMDStorageDecorator.fetch) that fails to resolve wraps fosite.ErrNotFound
// instead -- the same log-flooding risk exists for a caller who sends a
// bogus or unreachable CIMD URL as client_id.
func logClientLookupFailure(ctx context.Context, clientID string, err error) {
	level := slog.LevelWarn
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, fosite.ErrNotFound) {
		level = slog.LevelDebug
	}
	slog.Log(ctx, level, "failed to look up client for loopback redirect_uri rewrite",
		"client_id", clientID,
		"error", err,
	)
}

// rewriteLoopbackRedirectURI checks whether req's redirect_uri is a genuine
// RFC 8252 §7.3 loopback dynamic-port match against a client's registered
// redirect_uri (via registration.RegisteredLoopbackRedirectURI, so this works
// regardless of which concrete fosite.Client type the storage backend
// returns) and, if so, rewrites req.Form's redirect_uri to the registered
// (portless) literal so fosite's exact-match validation accepts it. It
// returns the original requested redirect_uri if a rewrite was made, or ""
// otherwise (including when req.Form can't be parsed, the client can't be
// resolved, isn't a loopback client, or the hostname is an IP literal --
// fosite's own validation and native loopback matching handle those cases
// as they do today).
//
// Why this rewrite exists at all: fosite has no client-side hook for
// "localhost" loopback matching (see registration.RegisteredLoopbackRedirectURI)
// -- it only matches redirect_uri by exact string equality against a client's registered
// URIs (checked first) or its own IP-literal-only loopback exception, so this
// rewrite is what makes fosite's exact-match branch accept a "localhost"
// dynamic-port request. See loopbackAuthorizeRequester above for how the
// error path recovers the dynamic port that this rewrite hides from fosite.
func (h *Handler) rewriteLoopbackRedirectURI(ctx context.Context, req *http.Request) string {
	if err := req.ParseForm(); err != nil {
		return ""
	}

	requestedRedirectURI := req.Form.Get("redirect_uri")
	clientID := req.Form.Get("client_id")
	if requestedRedirectURI == "" || clientID == "" {
		return ""
	}

	// Cheap, storage-free pre-check before doing a second client lookup (on top
	// of fosite's own internal one): a rewrite can only ever be needed for an
	// http redirect_uri whose hostname is "localhost" specifically. IP
	// literals (127.0.0.1, [::1]) are intentionally excluded: fosite's own
	// isMatchingAsLoopback already matches those natively, dynamic port and
	// all, on both success and error paths, so treating them as needing the
	// rewrite below would only add risk for no benefit. This also keeps the
	// extra storage lookup below off the common case (non-loopback
	// redirect_uris), narrowing its cost/availability impact to genuine
	// "localhost" loopback requests only.
	parsed, err := url.Parse(requestedRedirectURI)
	if err != nil || parsed.Scheme != "http" || !registration.IsLocalhostHostname(parsed.Hostname()) {
		return ""
	}

	// A lookup error here is not necessarily fatal to the request: fosite's
	// own independent lookup below can still succeed and validate the
	// (unrewritten) redirect_uri normally. It only becomes user-visible as a
	// generic "redirect_uri does not match any pre-registered redirect urls"
	// when BOTH lookups fail, or when this is a transient error (e.g. a
	// storage timeout) that fosite's second lookup doesn't hit -- in which
	// case the rewrite silently never happens. Log it so that case is
	// diagnosable; client_id is not a secret.
	client, err := h.storage.GetClient(ctx, clientID)
	if err != nil {
		logClientLookupFailure(ctx, clientID, err)
		return ""
	}

	registered, ok := registration.RegisteredLoopbackRedirectURI(client, requestedRedirectURI)
	if !ok || registered == requestedRedirectURI {
		return ""
	}

	req.Form.Set("redirect_uri", registered)
	return requestedRedirectURI
}
