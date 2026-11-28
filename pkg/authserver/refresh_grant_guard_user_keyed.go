// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// guardCheckFailedMessage is the one WARN the user-keyed guard logs when it
// cannot decide and grants the refresh.
const guardCheckFailedMessage = "refresh grant guard could not check the upstream credential; granting"

// userKeyedCheck is what the guard needs only when upstream rows are keyed by
// user. A user's rows are shared by all of their clients, so one client's
// refresh grant must not depend on, or delete, a connection another client
// uses: the guard checks the identity provider's row alone and deletes no row.
// After a confirmed rejection it only clears the rejected refresh token from
// that row.
type userKeyedCheck struct {
	reader upstreamtoken.UserCredentialReader
	// identityProvider is the first configured upstream, the one every login
	// goes through. Later upstreams are connectors a user may add or lose
	// without ending the login.
	identityProvider string
}

// newUserKeyedUpstreamRefreshGuard returns the guard for user-keyed storage.
// stor must be the user-keyed layer and refresher the server's shared
// refresher; the guard reads through its own InProcessService over both, so
// its refreshes share the refresher's deduplication with every other caller.
// The service has its own ID-token suppression map, so an expired ID token can
// cost one extra upstream refresh per process.
// The identity provider is the first of providerNames.
func newUserKeyedUpstreamRefreshGuard(
	stor storage.Storage, refresher storage.UpstreamTokenRefresher, providerNames []string,
	opts ...func(*upstreamRefreshGuard),
) (*upstreamRefreshGuard, error) {
	if len(providerNames) == 0 {
		return nil, errors.New("user-keyed upstream refresh guard requires an identity provider")
	}
	guard, err := newUpstreamRefreshGuard(stor, refresher, providerNames, opts...)
	if err != nil {
		return nil, err
	}
	guard.userKeyed = &userKeyedCheck{
		reader:           upstreamtoken.NewInProcessService(stor, refresher),
		identityProvider: providerNames[0],
	}
	return guard, nil
}

// deadUserKeyedUpstream is deadUpstream for user-keyed storage. userID is the
// session's subject, which the callback also stores the user's rows under.
//
// It refuses and revokes the presented refresh token when the identity
// provider's row is absent, deleted during the refresh, a placeholder or
// without an access token, expired with no refresh token, or its refresh was
// rejected with invalid_grant and the rejection is confirmed. It refuses
// without revoking when the row belongs to another user, the session has no
// canonical user, or the identity provider is not configured on this replica.
// It grants, with one WARN, when the outcome is uncertain, and without one
// when it only got rejections remembered from earlier refreshes. It deletes no
// row; after a confirmed rejection it clears the rejected refresh token from
// the row. A live access token in the row is granted without calling the
// reader.
//
// A compliant client discards a refresh token refused with invalid_grant, so
// an unrevoked refused token would be held by no legitimate party and would
// work again once the user signs in on any client, where rotation's reuse
// detection can no longer catch a stolen copy. Revoking keeps a refused token
// refused. The causes left unrevoked are data or configuration faults that a
// new sign-in does not fix and whose tokens should recover once the fault is
// fixed. Right after the option is turned on no user has a row yet, so a
// client that refreshes before its user signs in again is refused and its
// token revoked; that user signs in again, which the missing row requires
// anyway.
func (g *upstreamRefreshGuard) deadUserKeyedUpstream(ctx context.Context, tsid, userID string) *refusal {
	idp := g.userKeyed.identityProvider
	if userID == "" {
		return &refusal{provider: idp, reason: "session has no canonical user"}
	}
	if _, ok := g.providers[idp]; !ok {
		// Defensive, and it documents that an identity provider missing from
		// the configuration refuses: under user keying the identity provider
		// comes from the same list as g.providers. Not revoked: this replica's
		// config decides it, as in deadUpstream.
		return &refusal{provider: idp, reason: "identity provider is not configured"}
	}

	reqCtx := ctx
	ctx, cancel := context.WithTimeout(identity.WithCanonicalUser(ctx, userID), g.checkTimeout)
	defer cancel()

	// The reader does not return the row it refreshed, so the row the recheck
	// compares against is read first. A failed read leaves no snapshot, and a
	// snapshot with an empty refresh token is never what was redeemed, so no
	// rejection is then confirmed and the grant goes through.
	//
	// Accepted race: another sign-in can install refresh token B after this
	// read and before the refresh, and the identity provider can reject B. The
	// recheck then sees B differing from the snapshot, takes it for a
	// successor and grants once; the next refresh grant reads the same dead
	// row and is refused and revoked. It needs the provider to reject a token
	// it just issued, and fixing it would need the refresher to report which
	// token it redeemed.
	snapshot, err := g.tokens.GetUpstreamTokens(ctx, tsid, idp)
	if snapshot == nil && err != nil && !errors.Is(err, storage.ErrNotFound) {
		slog.DebugContext(ctx, "refresh grant guard could not read the identity provider row", "error", err)
	}

	if holdsLiveToken(snapshot, userID) {
		return nil
	}
	if snapshot != nil && snapshot.UserID == userID && !storage.UpstreamIssuerMatches(snapshot.Issuer, g.providerIssuers[idp]) {
		return &refusal{
			provider: idp,
			reason:   "upstream access token expired and its issuer does not match the configured provider",
		}
	}

	err = g.askUserCredential(ctx, userID, idp)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, upstreamtoken.ErrInvalidBinding):
		// Not revoked: a foreign row is a data fault that a new sign-in does
		// not fix, and the token should recover once it is fixed.
		return &refusal{provider: idp, reason: "upstream row belongs to another user"}
	case errors.Is(err, upstreamtoken.ErrNoCanonicalUser):
		return &refusal{provider: idp, reason: "no canonical user for the upstream row"}
	case errors.Is(err, upstream.ErrGrantRejected):
		if !g.userKeyedRejectionConfirmed(ctx, tsid, idp, snapshot) {
			return nil
		}
		// The row stays, since other clients of the user share it, but the
		// rejected refresh token goes so a later sign-in does not carry it
		// forward.
		g.clearRejectedRefreshToken(ctx, tsid, idp, snapshot)
		return &refusal{
			provider: idp, revoke: true, rejected: true,
			reason: "upstream refresh was rejected with invalid_grant",
		}
	case errors.Is(err, upstreamtoken.ErrNoRefreshToken):
		return &refusal{
			provider: idp, revoke: true,
			reason: "upstream access token expired and no refresh token is stored",
		}
	case errors.Is(err, upstreamtoken.ErrNeedsAuthorization):
		return &refusal{provider: idp, revoke: true, reason: "no usable upstream credential stored for the user"}
	}
	if reqCtx.Err() != nil {
		slog.DebugContext(ctx, "refresh grant guard stopped by a cancelled request; granting", "provider_id", idp)
		return nil
	}
	// Also reached for a row that cannot be read at all (a corrupt Redis
	// value). That deliberately differs from the field-off guard, which skips
	// such a row in its bulk read and refuses: here the grant goes through,
	// and the bearer path still answers 401 until the row's TTL runs out.
	slog.WarnContext(ctx, guardCheckFailedMessage, "provider_id", idp, "error", err)
	return nil
}

// userKeyedRejectionConfirmed is rejectionConfirmed for the identity
// provider's row read before the refresh. No snapshot, or one without a
// refresh token, cannot be what was redeemed, so nothing is confirmed.
func (g *upstreamRefreshGuard) userKeyedRejectionConfirmed(
	ctx context.Context, tsid, idp string, snapshot *storage.UpstreamTokens,
) bool {
	return snapshot != nil && snapshot.RefreshToken != "" && g.rejectionConfirmed(ctx, tsid, idp, snapshot)
}

// holdsLiveToken reports whether the snapshot is the user's row with an
// unexpired access token. Such a row needs no refresh, so the reader is not
// called: it could run the ID-token refresh trigger and ask the IdP while the
// user's credential is fine.
func holdsLiveToken(snapshot *storage.UpstreamTokens, userID string) bool {
	return snapshot != nil && snapshot.UserID == userID && snapshot.AccessToken != "" &&
		!snapshot.IsExpired(time.Now())
}

// clearRejectedRefreshToken removes the refresh token the identity provider
// confirmed dead from the shared row, keeping the row. The swap expects the
// rejected token, so a sign-in that stored a new one in the meantime wins and
// is left alone. A failure is logged at DEBUG and the grant is still refused
// and revoked. It runs on a context detached from the request.
//
// Accepted race: a sign-in that carries refresh token A forward can swap on A
// after the recheck read and before this swap. This swap then overwrites its
// fresh access token with the snapshot copy, which is expired and has no
// refresh token, and the user signs in once more. Closing it needs a storage
// method that compares more than the refresh token, which is deliberately not
// added.
func (g *upstreamRefreshGuard) clearRejectedRefreshToken(
	ctx context.Context, tsid, idp string, snapshot *storage.UpstreamTokens,
) {
	cleared := *snapshot
	cleared.RefreshToken = ""
	casCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guardRevokeTimeout)
	defer cancel()
	err := g.tokens.CompareAndSwapUpstreamTokens(casCtx, tsid, idp, snapshot.RefreshToken, &cleared)
	if err != nil && !errors.Is(err, storage.ErrConcurrentRefresh) {
		slog.DebugContext(ctx, "refresh grant guard could not clear the rejected refresh token", "error", err)
	}
}

// askUserCredential reads the user's credential for provider, refreshing it
// when expired, the way checkRow refreshes a row: it skips the refresher's
// memory of rejected refreshes so the IdP is asked, and reads once more when
// it joined a refresh that answered from memory. If that answer is remembered
// too, the outcome is uncertain and it returns nil, so the grant goes through;
// a remembered rejection never proves the credential dead.
func (g *upstreamRefreshGuard) askUserCredential(ctx context.Context, userID, provider string) error {
	readCtx := withoutRememberedRejections(ctx)
	err := g.readUserCredential(readCtx, userID, provider)
	if !isRememberedRejection(err) {
		return err
	}
	err = g.readUserCredential(readCtx, userID, provider)
	if isRememberedRejection(err) {
		slog.DebugContext(ctx, "refresh grant guard only got remembered rejections; granting",
			"provider_id", provider)
		return nil
	}
	return err
}

// readUserCredential checks only whether the credential can be read. The
// reader bounds the refresh wait by ctx, but its first storage lookup does not
// always observe ctx. A failure that arrives after ctx has ended is therefore
// uncertain, whatever the reader reported, so it returns ctx.Err() and never a
// refusal cause such as ErrNeedsAuthorization. A nil result stays nil. The
// check can run past guardCheckTimeout by up to the storage client's own
// timeout (about 3s for Redis), because go-redis ignores context deadlines on
// socket I/O.
func (g *upstreamRefreshGuard) readUserCredential(ctx context.Context, userID, provider string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := g.userKeyed.reader.GetValidTokensForUser(ctx, userID, provider)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
