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

// guardCheckTimeout bounds the guard's whole check, including the upstream
// refreshes of expired rows. A check that runs out of time is uncertain, so
// the grant goes through. A refresh still in flight when it does keeps
// running on the refresher's own detached context and stores its result.
const guardCheckTimeout = 10 * time.Second

// guardRevokeTimeout bounds the revocation of a refused refresh token and the
// deletion of a session's rows after a confirmed upstream rejection. Both run
// on a context detached from the request so a client that disconnects after
// the refusal does not skip them.
const guardRevokeTimeout = 5 * time.Second

// upstreamRefreshGuard is the built-in refresh-grant validator. It refuses a
// refresh_token grant when the token session's upstream credential is
// definitively dead. A refreshed access token keeps the token session (tsid),
// so granting it would hand the client a token the bearer validator answers
// with 401 again, and a client that only ever refreshes would loop. Refusing
// with invalid_grant sends the client to a full authorization, which creates a
// new tsid with fresh upstream rows.
//
// See Config.DisableUpstreamRefreshGuard for when a credential is dead, and
// docs/arch/11-auth-server-storage.md for the full behavior. Any other failure
// (network, 5xx, storage, the check running out of time) is uncertain and the
// grant goes through: the bearer validator answers those with 503 or a
// retryable 401 anyway.
//
// Expired rows are refreshed, one provider at a time, before deciding, because
// upstream refresh is lazy (it runs only on a resource request). An idle
// client's stored token is often expired while its upstream refresh token
// still works, and refusing on stored state alone would force every idle
// client to sign in again.
//
// The guard revokes the refused refresh token only when the stored rows prove
// the credential can never be used again. It refuses without revoking when the
// cause could be wrong and later correct itself: the IdP rejected a refresh
// (a replica that loses a cross-replica refresh race to a rotating IdP sees
// the same rejection), no row could be read (a storage backend may skip rows
// it cannot parse), or a row's provider is not configured (a decision from
// this replica's config, which can lag a rolling deploy that adds the
// provider). A refusal that turns out wrong then costs the client one
// re-authorization, not its grant.
//
// Before refusing on an IdP rejection, the guard re-reads the row once more
// after rejectionRecheckDelay and treats a row now holding a different refresh
// token as live. A recheck that cannot complete (the check is cancelled or
// times out, or the read fails) leaves the outcome uncertain, so the grant
// goes through. Once the rejection is confirmed, the guard deletes all of the
// session's upstream rows: the client is sent to a new tsid, so nothing reads
// them again, and a replay of the kept refresh token is then refused as having
// no rows, without another IdP call or recheck, on every replica.
//
// A refusal is logged at WARN only when it revokes or follows a confirmed IdP
// rejection; the row deletion makes the latter happen once per session. The
// other refusals repeat on every replay of a kept refresh token, so they are
// logged at DEBUG.
type upstreamRefreshGuard struct {
	tokens       storage.UpstreamTokenStorage
	refresher    storage.UpstreamTokenRefresher
	providers    map[string]struct{}
	revoker      oauth2.TokenRevocationStorage
	recheckDelay time.Duration
	checkTimeout time.Duration
}

var _ fosite.TokenEndpointHandler = (*upstreamRefreshGuard)(nil)

// newUpstreamRefreshGuard returns the guard for the given storage, the
// server's shared refresher and the names of the configured upstreams.
func newUpstreamRefreshGuard(
	stor storage.Storage, refresher storage.UpstreamTokenRefresher, providerNames []string,
) (*upstreamRefreshGuard, error) {
	if stor == nil {
		return nil, errors.New("upstream refresh guard requires storage")
	}
	if refresher == nil {
		return nil, errors.New("upstream refresh guard requires an upstream token refresher")
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
		checkTimeout: guardCheckTimeout,
	}, nil
}

// CanSkipClientAuth always returns false. refreshGrantValidators never calls
// it; it is part of fosite.TokenEndpointHandler.
func (*upstreamRefreshGuard) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return false
}

// CanHandleTokenEndpointRequest reports whether the request is a refresh_token
// grant.
func (*upstreamRefreshGuard) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return isRefreshTokenGrant(requester)
}

// HandleTokenEndpointRequest refuses the grant with invalid_grant when the
// session's upstream credential is definitively dead, revoking the refresh
// token when the refusal is final. A session without a tsid (a session-less
// grant) is not checked.
func (g *upstreamRefreshGuard) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !g.CanHandleTokenEndpointRequest(ctx, requester) {
		return fosite.ErrUnknownRequest
	}
	sess, ok := requester.GetSession().(session.UpstreamSession)
	if !ok || sess.GetIDPSessionID() == "" {
		return nil
	}
	tsid := sess.GetIDPSessionID()

	refused := g.deadUpstream(ctx, tsid)
	if refused == nil {
		return nil
	}
	clientID := requester.GetClient().GetID()
	revoked := refused.revoke && g.revoke(ctx, requester.GetID(), clientID)
	level := slog.LevelDebug
	if refused.revoke || refused.rejected {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "refusing refresh grant: upstream credential is no longer usable; re-authorization required",
		"client_id", clientID,
		"provider_id", refused.provider,
		"reason", refused.reason,
		"revoked", revoked,
	)
	// Log tsid at DEBUG only; it is credential-adjacent.
	slog.DebugContext(ctx, "refused refresh grant for token session", "tsid", tsid)
	return fosite.ErrInvalidGrant.WithHint(
		"The upstream identity provider session is no longer valid. Re-authorization is required.")
}

// PopulateTokenEndpointResponse contributes nothing; the guard only refuses.
// refreshGrantValidators never calls it; it is part of
// fosite.TokenEndpointHandler.
func (*upstreamRefreshGuard) PopulateTokenEndpointResponse(
	context.Context, fosite.AccessRequester, fosite.AccessResponder,
) error {
	return fosite.ErrUnknownRequest
}

// refusal is why the guard refuses a grant.
type refusal struct {
	// provider is the upstream the refusal applies to, or empty when it
	// applies to the session as a whole.
	provider string
	reason   string
	// revoke is set when the stored rows prove the credential can never be
	// used again, so the refused refresh token is revoked too.
	revoke bool
	// rejected is set when the IdP's rejection of the refresh was confirmed
	// and the session's rows were deleted.
	rejected bool
}

// deadUpstream returns why the session's upstream credential is definitively
// dead, or nil when some row is usable or its state is uncertain. Rows are
// checked in provider-name order and checking stops at the first dead one, so
// a refusal costs no further upstream refreshes. The bearer validator answers
// a session without any upstream access token with 401, so a session with no
// usable row is dead too.
func (g *upstreamRefreshGuard) deadUpstream(ctx context.Context, tsid string) *refusal {
	ctx, cancel := context.WithTimeout(ctx, g.checkTimeout)
	defer cancel()

	rows, err := g.tokens.GetAllUpstreamTokens(ctx, tsid)
	if err != nil {
		slog.DebugContext(ctx, "refresh grant guard could not read upstream tokens; granting", "error", err)
		return nil
	}
	if len(rows) == 0 {
		// Not revoked: a storage backend may skip rows it cannot parse and
		// report none, which reads the same as an absent session.
		return &refusal{reason: "no upstream tokens stored"}
	}
	anyUsable := false
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		usable, refused := g.checkRow(ctx, tsid, name, rows[name])
		if refused != nil {
			return refused
		}
		anyUsable = anyUsable || usable
	}
	if !anyUsable {
		return &refusal{reason: "no upstream access token stored", revoke: true}
	}
	return nil
}

// checkRow classifies one upstream row, refreshing it first when it is
// expired. It returns a refusal when the row is dead. Otherwise usable reports
// whether the row holds an access token or its state is uncertain (a transient
// failure), which the guard does not refuse; a row that is neither is not
// dead on its own.
func (g *upstreamRefreshGuard) checkRow(
	ctx context.Context, tsid, name string, row *storage.UpstreamTokens,
) (usable bool, refused *refusal) {
	if row == nil {
		return false, nil
	}
	if !row.IsExpired(time.Now()) {
		return row.AccessToken != "", nil
	}
	if _, ok := g.providers[name]; !ok {
		// Not revoked: this replica's config decides it, and another replica
		// may have the provider configured during a rolling deploy.
		return false, &refusal{
			provider: name,
			reason:   "upstream access token expired and its provider is no longer configured",
		}
	}
	if row.RefreshToken == "" {
		return false, &refusal{
			provider: name, revoke: true,
			reason: "upstream access token expired and no refresh token is stored",
		}
	}

	refreshed, err := g.refresh(ctx, tsid, row)
	if err == nil {
		return refreshed != nil && refreshed.AccessToken != "", nil
	}
	if errors.Is(err, storage.ErrNotFound) {
		// The row was deleted after the guard read it (logout, expiry, another
		// replica's cleanup). Not revoked, for the same reason as no rows.
		return false, &refusal{provider: name, reason: "upstream token row was deleted during refresh"}
	}
	if !errors.Is(err, upstream.ErrGrantRejected) {
		slog.DebugContext(ctx, "upstream refresh failed transiently; refresh grant guard is not refusing",
			"provider_id", name, "error", err)
		return true, nil
	}
	if !g.rejectionConfirmed(ctx, tsid, name, row.RefreshToken) {
		return true, nil
	}
	g.dropSession(ctx, tsid)
	return false, &refusal{provider: name, reason: "upstream refresh was rejected with invalid_grant", rejected: true}
}

// refresh runs the refresher, returning ctx's error without calling the IdP
// when ctx has already ended, and early when ctx ends first. The refresher
// detaches from ctx, so without this a hung IdP would hold the token endpoint
// for the refresher's full timeout. ctx's error is not ErrGrantRejected, so
// the caller treats it as uncertain.
func (g *upstreamRefreshGuard) refresh(
	ctx context.Context, tsid string, row *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		tokens *storage.UpstreamTokens
		err    error
	}
	// Buffered so the goroutine never blocks once the guard stops waiting.
	done := make(chan result, 1)
	go func() {
		tokens, err := g.refresher.RefreshAndStore(ctx, tsid, row)
		done <- result{tokens: tokens, err: err}
	}()
	select {
	case r := <-done:
		return r.tokens, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// rejectionConfirmed waits recheckDelay, re-reads the row and reports whether
// the upstream rejection of rejectedRT is confirmed: the row still holds
// rejectedRT, or is gone. A row holding any other refresh token means another
// replica won a refresh race and its write has landed, so the session may be
// live even if that row's access token has already expired. rejectedRT is the
// guard's own snapshot, which the refresher may have replaced with a newer
// re-read before redeeming; a mismatch then reads as a successor too, which
// errs toward granting. The outcome is not known when the wait is cancelled or
// the read fails with anything but ErrNotFound; that reports false too, so a
// client disconnect or a storage fault never refuses a session that may be
// live.
func (g *upstreamRefreshGuard) rejectionConfirmed(ctx context.Context, tsid, provider, rejectedRT string) bool {
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
	case errors.Is(err, storage.ErrExpired) && stored != nil:
		// Storage returns an expired row alongside ErrExpired; compare it.
	case err != nil:
		slog.DebugContext(ctx, "refresh grant guard recheck read failed; not refusing",
			"provider_id", provider, "error", err)
		return false
	case stored == nil:
		return true
	}
	return stored.RefreshToken == rejectedRT
}

// dropSession deletes all of the session's upstream rows after a confirmed
// upstream rejection. Every row goes, not only the rejected provider's: with
// the rejected row gone and its siblings kept, a replay would see only usable
// rows and be granted a token that never gets a credential for the rejected
// provider. A replica that wins a refresh race after the recheck then fails
// its compare-and-swap, which costs nothing more, since this client is
// already refused. A failure is logged at DEBUG and the grant is still
// refused; a replay then repeats the check.
func (g *upstreamRefreshGuard) dropSession(ctx context.Context, tsid string) {
	delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guardRevokeTimeout)
	defer cancel()
	if err := g.tokens.DeleteUpstreamTokens(delCtx, tsid); err != nil && !errors.Is(err, storage.ErrNotFound) {
		slog.DebugContext(ctx, "refresh grant guard could not delete rejected upstream rows", "error", err)
	}
}

// revoke revokes the refused refresh token and the access tokens of the same
// grant, as an RFC 7009 revocation of the refresh token would, and reports
// whether it succeeded. A failure is logged and the grant is still refused:
// the client is sent to a full authorization either way, and the guard
// refuses the token again if it is presented.
func (g *upstreamRefreshGuard) revoke(ctx context.Context, requestID, clientID string) bool {
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guardRevokeTimeout)
	defer cancel()
	if err := errors.Join(
		g.revoker.RevokeRefreshToken(revokeCtx, requestID),
		g.revoker.RevokeAccessToken(revokeCtx, requestID),
	); err != nil {
		slog.WarnContext(ctx, "failed to revoke refresh token refused by the upstream refresh guard",
			"client_id", clientID, "error", err)
		return false
	}
	return true
}
