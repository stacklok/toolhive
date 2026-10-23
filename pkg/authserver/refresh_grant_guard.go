// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// rejectionRecheckDelay is how long the guard waits after an upstream
// invalid_grant before re-reading the row, giving a replica that won a
// cross-replica refresh race time to land its write. The refresher's own
// re-read runs immediately and misses a write still in flight.
const rejectionRecheckDelay = 500 * time.Millisecond

// guardRevokeTimeout bounds the revocation of a refused refresh token. It runs
// on a context detached from the request so a client that disconnects after
// the refusal does not leave the refresh token live.
const guardRevokeTimeout = 5 * time.Second

// upstreamRefreshGuard is the built-in refresh-grant validator. It refuses a
// refresh_token grant, and revokes the refresh token, when the token session's
// upstream credential is definitively dead. A refreshed access token keeps the
// token session (tsid), so granting it would hand the client a token the
// bearer validator answers with 401 again, and a client that only ever
// refreshes would loop. Refusing with invalid_grant sends the client to a full
// authorization, which creates a new tsid with fresh upstream rows.
//
// A credential is definitively dead when:
//   - the session has no upstream rows, or none holds an access token;
//   - a row is expired and its provider is no longer configured, so nothing
//     can refresh it;
//   - a row is expired and has no refresh token; or
//   - the IdP rejected a row's refresh with invalid_grant
//     (upstream.ErrGrantRejected).
//
// Any other failure (network, 5xx, storage) is transient and the grant goes
// through: the bearer validator answers those with 503 or a retryable 401
// anyway.
//
// Expired rows are refreshed before deciding, because upstream refresh is lazy
// (it runs only on a resource request). An idle client's stored token is often
// expired while its upstream refresh token still works, and refusing on stored
// state alone would force every idle client to sign in again.
//
// A rotating IdP also answers invalid_grant to a replica that loses a
// cross-replica refresh race. The refresher re-reads the row immediately; the
// guard re-reads it once more after rejectionRecheckDelay and treats a row now
// holding a different, unexpired refresh token as live. A recheck that cannot
// complete (the request is cancelled, or the read fails) leaves the outcome
// uncertain, so the grant is not refused and nothing is revoked.
type upstreamRefreshGuard struct {
	tokens       storage.UpstreamTokenStorage
	refresher    storage.UpstreamTokenRefresher
	providers    map[string]struct{}
	revoker      oauth2.TokenRevocationStorage
	recheckDelay time.Duration
}

var _ fosite.TokenEndpointHandler = (*upstreamRefreshGuard)(nil)

// newUpstreamRefreshGuard returns the guard, or nil when it is disabled or
// there is no refresher (no upstreams are configured, so no session has
// upstream rows to check). providerNames are the configured upstreams.
func newUpstreamRefreshGuard(
	disabled bool, stor storage.Storage, refresher storage.UpstreamTokenRefresher, providerNames []string,
) *upstreamRefreshGuard {
	if disabled || refresher == nil {
		return nil
	}
	providers := make(map[string]struct{}, len(providerNames))
	for _, name := range providerNames {
		providers[name] = struct{}{}
	}
	return &upstreamRefreshGuard{
		tokens:       stor,
		refresher:    refresher,
		providers:    providers,
		revoker:      stor,
		recheckDelay: rejectionRecheckDelay,
	}
}

// CanSkipClientAuth always returns false; see refreshGrantValidators.
func (*upstreamRefreshGuard) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return false
}

// CanHandleTokenEndpointRequest reports whether the request is a refresh_token
// grant.
func (*upstreamRefreshGuard) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return isRefreshTokenGrant(requester)
}

// HandleTokenEndpointRequest refuses the grant with invalid_grant and revokes
// the refresh token when the session's upstream credential is definitively
// dead. A session without a tsid (a session-less grant) is not checked.
func (g *upstreamRefreshGuard) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !g.CanHandleTokenEndpointRequest(ctx, requester) {
		return fosite.ErrUnknownRequest
	}
	sess, ok := requester.GetSession().(session.UpstreamSession)
	if !ok || sess.GetIDPSessionID() == "" {
		return nil
	}
	tsid := sess.GetIDPSessionID()

	provider, reason := g.deadUpstream(ctx, tsid)
	if reason == "" {
		return nil
	}
	g.revoke(ctx, requester.GetID())
	// The refresh token is revoked, so each refresh token logs this once.
	slog.WarnContext(ctx, "refusing refresh grant: upstream credential is no longer usable; re-authorization required",
		"client_id", requester.GetClient().GetID(),
		"provider_id", provider,
		"reason", reason,
	)
	// Log tsid at DEBUG only; it is credential-adjacent.
	slog.DebugContext(ctx, "refused refresh grant for token session", "tsid", tsid)
	return fosite.ErrInvalidGrant.WithHint(
		"The upstream identity provider session is no longer valid. Re-authorization is required.")
}

// PopulateTokenEndpointResponse contributes nothing; the guard only refuses.
func (*upstreamRefreshGuard) PopulateTokenEndpointResponse(
	context.Context, fosite.AccessRequester, fosite.AccessResponder,
) error {
	return fosite.ErrUnknownRequest
}

// deadUpstream returns a non-empty reason, and the provider it applies to (if
// any), when the session's upstream credential is definitively dead. It
// returns an empty reason when some row is usable or its state is uncertain.
// Rows are checked in provider-name order and checking stops at the first dead
// one, so a refusal costs no further upstream refreshes. The bearer validator
// answers a session without any upstream access token with 401, so a session
// with no usable row is dead too.
func (g *upstreamRefreshGuard) deadUpstream(ctx context.Context, tsid string) (provider, reason string) {
	rows, err := g.tokens.GetAllUpstreamTokens(ctx, tsid)
	if err != nil {
		slog.DebugContext(ctx, "refresh grant guard could not read upstream tokens; granting", "error", err)
		return "", ""
	}
	usable := false
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		state, reason := g.checkRow(ctx, tsid, name, rows[name])
		switch state {
		case rowDead:
			return name, reason
		case rowUsable:
			usable = true
		case rowUnusable:
		}
	}
	if !usable {
		return "", "no upstream access token stored"
	}
	return "", ""
}

// rowState is the guard's verdict on one upstream row.
type rowState int

const (
	// rowUnusable holds no access token but is not dead on its own.
	rowUnusable rowState = iota
	// rowUsable holds an access token after any refresh, or its state is
	// uncertain (a transient failure), which the guard does not refuse.
	rowUsable
	// rowDead can never yield an access token again.
	rowDead
)

// checkRow classifies one upstream row, refreshing it first when it is
// expired. A dead row comes with the reason it is dead.
func (g *upstreamRefreshGuard) checkRow(
	ctx context.Context, tsid, name string, row *storage.UpstreamTokens,
) (rowState, string) {
	if row == nil {
		return rowUnusable, ""
	}
	if !row.IsExpired(time.Now()) {
		if row.AccessToken == "" {
			return rowUnusable, ""
		}
		return rowUsable, ""
	}
	if _, ok := g.providers[name]; !ok {
		return rowDead, "upstream access token expired and its provider is no longer configured"
	}
	if row.RefreshToken == "" {
		return rowDead, "upstream access token expired and no refresh token is stored"
	}
	refreshed, err := g.refresher.RefreshAndStore(ctx, tsid, row)
	switch {
	case err == nil && (refreshed == nil || refreshed.AccessToken == ""):
		return rowUnusable, ""
	case err == nil:
		return rowUsable, ""
	case !errors.Is(err, upstream.ErrGrantRejected):
		slog.DebugContext(ctx, "upstream refresh failed transiently; refresh grant guard is not refusing",
			"provider_id", name, "error", err)
		return rowUsable, ""
	case !g.rejectionStands(ctx, tsid, name, row.RefreshToken):
		return rowUsable, ""
	}
	return rowDead, "upstream refresh was rejected with invalid_grant"
}

// rejectionStands waits recheckDelay, re-reads the row and reports whether
// the upstream rejection of rejectedRT is final. It is not when the row now
// holds a different refresh token with an unexpired access token, meaning
// another replica won a refresh race and its write has landed. It is not known
// when the wait is cancelled or the read fails with anything but ErrNotFound
// or ErrExpired; that reports false too, so a client disconnect or a storage
// fault never revokes a session that may be live.
func (g *upstreamRefreshGuard) rejectionStands(ctx context.Context, tsid, provider, rejectedRT string) bool {
	if g.recheckDelay > 0 {
		timer := time.NewTimer(g.recheckDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			slog.DebugContext(ctx, "refresh grant guard recheck cancelled; not refusing", "provider_id", provider)
			return false
		case <-timer.C:
		}
	}
	stored, err := g.tokens.GetUpstreamTokens(ctx, tsid, provider)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return true
	case errors.Is(err, storage.ErrExpired):
		// The row is still expired; only a different refresh token would mean
		// a successor, and an expired successor is no more usable.
		return true
	case err != nil:
		slog.DebugContext(ctx, "refresh grant guard recheck read failed; not refusing",
			"provider_id", provider, "error", err)
		return false
	case stored == nil:
		return true
	}
	return stored.RefreshToken == rejectedRT || stored.IsExpired(time.Now())
}

// revoke revokes the refused refresh token and the access tokens of the same
// grant, as an RFC 7009 revocation of the refresh token would. A failure is
// logged and the grant is still refused: the client is sent to a full
// authorization either way, and the guard refuses the token again if it is
// presented.
func (g *upstreamRefreshGuard) revoke(ctx context.Context, requestID string) {
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guardRevokeTimeout)
	defer cancel()
	if err := errors.Join(
		g.revoker.RevokeRefreshToken(revokeCtx, requestID),
		g.revoker.RevokeAccessToken(revokeCtx, requestID),
	); err != nil {
		slog.WarnContext(ctx, "failed to revoke refresh token refused by the upstream refresh guard", "error", err)
	}
}
