// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// refreshTimeout bounds how long a singleflight-deduplicated token refresh
// may take before being cancelled. It is deliberately detached from the
// triggering request's context so that waiting callers are not abandoned.
const refreshTimeout = 30 * time.Second

// upstreamStoreMaxAttempts is the maximum number of attempts to store refreshed
// upstream tokens before giving up.
const upstreamStoreMaxAttempts = 3

// upstreamStoreRetryBackoff is the fixed delay between store retry attempts.
const upstreamStoreRetryBackoff = 50 * time.Millisecond

// upstreamDeleteTimeout bounds how long the best-effort per-provider delete may
// take. A fresh detached context is used so a ctx deadline/cancel on the store
// attempts does not also kill the delete, which would leave the stale row behind.
const upstreamDeleteTimeout = 5 * time.Second

// upstreamRefreshLeaseTTL bounds a distributed refresh lease (see
// storage.UpstreamRefreshLocker). It must outlast the holder's whole
// read-redeem-write sequence, which refreshCtx caps at refreshTimeout, or the
// lease could expire mid-redemption and let a waiter redeem the same refresh
// token — the exact race the lease exists to prevent. The margin above
// refreshTimeout covers the gap between acquiring the lease and the holder's
// CAS write landing. It is also the longest a crashed holder can block the
// row, which is why it is not set far higher.
const upstreamRefreshLeaseTTL = refreshTimeout + 15*time.Second

// upstreamRefreshLeaseReleaseTimeout bounds the release of a refresh lease. A
// fresh detached context is used because the refresh context is usually
// already at or past its deadline by the time the holder unwinds, and a
// release skipped on that account would make every waiter sit out the full
// upstreamRefreshLeaseTTL.
const upstreamRefreshLeaseReleaseTimeout = 5 * time.Second

// upstreamRefreshLockPollInitial and upstreamRefreshLockPollMax bound the
// backoff a replica waits between attempts while another holder has the lease.
// Each attempt costs one acquisition and one row read, and the wait can run to
// refreshTimeout, so a fixed short interval would put hundreds of round-trips
// per waiting request on the storage backend. Starting small keeps the common
// case — the holder finishes in well under a second — responsive, and the cap
// keeps a long wait cheap.
const (
	upstreamRefreshLockPollInitial = 25 * time.Millisecond
	upstreamRefreshLockPollMax     = 500 * time.Millisecond
)

// upstreamConflictReadTimeout bounds the single re-read resolveConcurrentRefreshConflict
// performs after a CAS conflict. It is deliberately much shorter than refreshTimeout:
// the caller of RefreshAndStore is already blocked on refreshTimeout (30s) via the
// detached refreshCtx passed in here, so reusing refreshTimeout for this read would
// stack a second independent 30s window on top — doubling the worst-case wait for no
// benefit, since a single GetUpstreamTokens call needs nowhere near that budget.
const upstreamConflictReadTimeout = 5 * time.Second

// upstreamTokenRefresher implements storage.UpstreamTokenRefresher by wrapping
// a set of upstream OAuth2Providers (keyed by provider name) and
// UpstreamTokenStorage (for persisting the refreshed tokens). On each refresh
// call it dispatches to the correct provider based on the expired token's
// ProviderID.
//
// Two layers cooperate to coordinate redeeming a single-use, rotating upstream
// refresh token under concurrency:
//   - sfGroup deduplicates concurrent refreshes by the opaque storage-row
//     identity returned by UpstreamTokenStorage, but only WITHIN THIS PROCESS
//     — it saves redundant upstream calls and redis round-trips when this
//     process alone receives several concurrent requests for the same row.
//   - locker, when the storage backend provides one, serializes the whole
//     read-redeem-write sequence ACROSS PROCESSES with a lease over the same
//     row identity singleflight uses, so only the lease holder calls the
//     upstream provider. This is what keeps a strict single-use-rotation IdP
//     from seeing two replicas redeem one refresh token as a replay. A
//     backend that does not provide the capability leaves it nil, and the
//     refresher falls back to the layer below, which orders the stored row
//     but cannot prevent the duplicate redemption.
//   - CompareAndSwapUpstreamTokens (see refreshAndStore) makes the STORED row
//     deterministic ACROSS PROCESSES: every write is conditioned on the refresh
//     token this call redeemed still being the one stored, so a replica whose
//     write loses a cross-process race fails instead of clobbering a winning
//     replica's rotated token with a stale one. This is a storage-ordering
//     guarantee, not a guarantee that the redemption itself was safe: both
//     replicas still call the upstream provider before either writes, so a
//     strict single-use-rotation provider (RFC 9700 §4.14.2 replay detection)
//     can still see this as two redemptions of the same refresh token and
//     revoke the grant regardless of which write wins here — see
//     docs/arch/11-auth-server-storage.md's "Refresh Coordination Scope" for
//     the provider-behavior-dependent discussion. singleflight is an
//     optimization layered on top of the CAS guarantee, not a substitute for it.
type upstreamTokenRefresher struct {
	providers map[string]upstream.OAuth2Provider
	storage   storage.UpstreamTokenStorage

	// locker is the storage backend's optional cross-process refresh lease,
	// resolved once at construction. It is nil for a backend that does not
	// provide one, which is harmless for a single-process backend and degrades
	// to CAS-only coordination for any other.
	locker storage.UpstreamRefreshLocker

	refreshTokenLifespan time.Duration
	sfGroup              singleflight.Group
}

// Compile-time check that upstreamTokenRefresher implements storage.UpstreamTokenRefresher.
var _ storage.UpstreamTokenRefresher = (*upstreamTokenRefresher)(nil)

// RefreshAndStore validates deterministic input errors before resolving the
// opaque storage-row identity used to deduplicate a refresh within this process
// and, when the backend supports it, to take that row's cross-process refresh
// lease (see coordinatedRefresh).
//
// The leader re-reads that row under a detached timeout so it never redeems a
// stale refresh token supplied by a caller. It only short-circuits on that
// re-read when the row is unexpired AND differs from the caller's row, i.e.
// another caller already refreshed it. A storage backend that returns tokens
// without checking expiry does not fool it into skipping the refresh, and a
// caller that deliberately refreshes a still-current row (e.g. because its ID
// token expired before its access token) gets a real upstream refresh.
func (r *upstreamTokenRefresher) RefreshAndStore(
	ctx context.Context,
	sessionID string,
	expired *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	if expired == nil {
		return nil, errors.New("expired tokens are required")
	}
	if expired.RefreshToken == "" {
		return nil, errors.New("no refresh token available for upstream token refresh")
	}
	if _, ok := r.providers[expired.ProviderID]; !ok {
		return nil, fmt.Errorf("no upstream provider configured for %q", expired.ProviderID)
	}

	rowID, err := r.storage.ResolveUpstreamTokenRowID(ctx, sessionID, expired.ProviderID)
	if err != nil {
		return nil, err
	}
	if rowID == "" {
		return nil, errors.New("upstream token row ID cannot be empty")
	}

	result, err, _ := r.sfGroup.Do(string(rowID), func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()

		return r.coordinatedRefresh(refreshCtx, sessionID, rowID, expired)
	})
	if err != nil {
		return nil, err
	}
	refreshed, ok := result.(*storage.UpstreamTokens)
	if !ok || refreshed == nil {
		return nil, errors.New("unexpected nil result from upstream token refresh")
	}
	refreshedCopy := *refreshed
	return &refreshedCopy, nil
}

// coordinatedRefresh runs the leader half of RefreshAndStore under the storage
// backend's cross-process refresh lease, when it provides one.
//
// Holding the lease is what stops two replicas redeeming the same single-use
// refresh token. A replica that cannot take it must therefore never fall
// through to the upstream provider: it waits, re-reading the row, until either
// the holder's refreshed row appears — which is the result it was waiting for —
// or the lease becomes free, in which case the holder died or its lease expired
// and this replica takes over as leader. Running out of refreshCtx while
// waiting is a failure, not a licence to redeem uncoordinated.
//
// A backend without the capability (locker nil) refreshes directly, exactly as
// before this layer existed: correct for a single-process backend, and
// CAS-only coordination for any other.
func (r *upstreamTokenRefresher) coordinatedRefresh(
	ctx context.Context,
	sessionID string,
	rowID storage.UpstreamTokenRowID,
	caller *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	if r.locker == nil {
		return r.refreshAuthoritativeRow(ctx, sessionID, caller)
	}

	wait := upstreamRefreshLockPollInitial
	for {
		// Checked before each attempt so an expiry that coincides with a wait
		// is reported as the timeout it is, rather than as whatever error the
		// backend returns for an already-cancelled context.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf(
				"timed out waiting for another replica to refresh upstream tokens for provider %q: %w",
				caller.ProviderID, err,
			)
		}

		lease, err := r.locker.AcquireUpstreamRefreshLock(ctx, rowID, upstreamRefreshLeaseTTL)
		if err == nil {
			return r.refreshHoldingLease(ctx, sessionID, lease, caller)
		}
		if !errors.Is(err, storage.ErrUpstreamRefreshLockHeld) {
			// The backend could not be consulted, so this replica cannot know
			// whether another one is redeeming right now. Fail closed rather
			// than redeem and risk the IdP seeing a replay.
			return nil, fmt.Errorf(
				"could not acquire upstream refresh lock for provider %q: %w", caller.ProviderID, err,
			)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf(
				"timed out waiting for another replica to refresh upstream tokens for provider %q: %w",
				caller.ProviderID, ctx.Err(),
			)
		case <-time.After(wait):
		}
		wait = min(wait*2, upstreamRefreshLockPollMax)

		if winner := r.refreshedByLeaseHolder(ctx, sessionID, caller); winner != nil {
			slog.Debug("another replica refreshed the upstream token row while this one waited",
				"provider_id", caller.ProviderID,
			)
			return winner, nil
		}
	}
}

// refreshHoldingLease runs the leader half and gives the lease back, so a
// waiter can proceed without sitting out upstreamRefreshLeaseTTL. The release
// runs on every exit path, including the short-circuit where no upstream call
// was made at all.
func (r *upstreamTokenRefresher) refreshHoldingLease(
	ctx context.Context,
	sessionID string,
	lease storage.UpstreamRefreshLease,
	caller *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	defer func() {
		releaseCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), upstreamRefreshLeaseReleaseTimeout,
		)
		defer cancel()
		if err := lease.Release(releaseCtx); err != nil {
			// Not a correctness failure: the lease expires on its own, so this
			// only delays the next waiter by at most upstreamRefreshLeaseTTL.
			slog.Warn("failed to release upstream refresh lock; it expires on its own",
				"provider_id", caller.ProviderID,
				"error", err,
			)
		}
	}()

	return r.refreshAuthoritativeRow(ctx, sessionID, caller)
}

// refreshedByLeaseHolder reports the row a lease holder stored while this
// caller waited, or nil when there is nothing usable to return yet.
//
// The test is deliberately the same one refreshAuthoritativeRow short-circuits
// on — unexpired AND different from the row the caller arrived with — so a
// waiter accepts exactly what a leader would have accepted on its own re-read.
// Anything else (the row unchanged, still expired, or a read failure) means
// the holder has not finished, so the caller keeps waiting rather than
// treating a transient read as a refusal.
func (r *upstreamTokenRefresher) refreshedByLeaseHolder(
	ctx context.Context, sessionID string, caller *storage.UpstreamTokens,
) *storage.UpstreamTokens {
	stored, err := r.storage.GetUpstreamTokens(ctx, sessionID, caller.ProviderID)
	if err != nil || stored == nil {
		return nil
	}
	if stored.IsExpired(time.Now()) || !rowChanged(caller, stored) {
		return nil
	}
	return stored
}

// refreshAuthoritativeRow re-reads the row the caller is refreshing and either
// returns a row another caller already refreshed or redeems the refresh token
// itself. It is the leader half of RefreshAndStore, and with a locker present
// it only ever runs while this process holds that row's lease.
func (r *upstreamTokenRefresher) refreshAuthoritativeRow(
	ctx context.Context, sessionID string, caller *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	authoritative, readErr := r.storage.GetUpstreamTokens(ctx, sessionID, caller.ProviderID)
	if readErr == nil {
		// Some storage implementations may return tokens without checking
		// access-token expiry (the interface does not require it; see
		// storage.UpstreamTokenStorage.GetUpstreamTokens). Only short-circuit
		// when the re-read row is actually unexpired; otherwise fall through
		// and refresh using it as the authoritative row.
		if authoritative == nil {
			return nil, fmt.Errorf(
				"upstream token row missing on re-read for provider %q", caller.ProviderID,
			)
		}
		if !authoritative.IsExpired(time.Now()) && rowChanged(caller, authoritative) {
			return authoritative, nil
		}
		return r.refreshAndStore(ctx, sessionID, authoritative)
	}
	if !errors.Is(readErr, storage.ErrExpired) || authoritative == nil {
		return nil, readErr
	}
	return r.refreshAndStore(ctx, sessionID, authoritative)
}

// refreshAndStore performs the actual token refresh and storage write.
func (r *upstreamTokenRefresher) refreshAndStore(
	ctx context.Context,
	sessionID string,
	expired *storage.UpstreamTokens,
) (*storage.UpstreamTokens, error) {
	if expired.RefreshToken == "" {
		return nil, errors.New("no refresh token available for upstream token refresh")
	}

	slog.Debug("attempting upstream token refresh",
		"session_id", sessionID,
		"provider_id", expired.ProviderID,
	)

	// Look up the provider that issued this token
	provider, ok := r.providers[expired.ProviderID]
	if !ok {
		return nil, fmt.Errorf("no upstream provider configured for %q", expired.ProviderID)
	}

	// Refresh tokens via the upstream provider
	newTokens, err := provider.RefreshTokens(ctx, expired.RefreshToken, expired.UpstreamSubject)
	if err != nil {
		if errors.Is(err, upstream.ErrGrantRejected) {
			if winner := r.rotatedElsewhere(ctx, sessionID, expired); winner != nil {
				return winner, nil
			}
		}
		return nil, fmt.Errorf("upstream token refresh failed: %w", err)
	}

	// Defensive re-anchor of SessionExpiresAt: the post-PR callback write sets
	// SessionExpiresAt unconditionally so it can be carried forward here as a
	// storage TTL bound. Pre-PR rows persisted without that field decode as
	// zero. If such a legacy row is refreshed and the upstream rotation drops
	// expires_in, both ExpiresAt and SessionExpiresAt would be zero, the row
	// would be stored without any TTL bound, and the Memory backend would
	// retain it indefinitely. Re-anchor to now+RefreshTokenLifespan to restore
	// the invariant. The Redis 30-day per-key TTL also caps the legacy
	// behavior, but Memory has no such backstop.
	sessionExpiresAt := expired.SessionExpiresAt
	if sessionExpiresAt.IsZero() && newTokens.ExpiresAt.IsZero() {
		sessionExpiresAt = time.Now().Add(r.refreshTokenLifespan)
		slog.Debug("re-anchored zero SessionExpiresAt on refresh of legacy upstream token row",
			"session_id", sessionID,
			"provider_id", expired.ProviderID,
			"refresh_token_lifespan", r.refreshTokenLifespan,
		)
	}

	// Build updated storage tokens preserving binding fields from the original
	updated := &storage.UpstreamTokens{
		ProviderID:       expired.ProviderID,
		AccessToken:      newTokens.AccessToken,
		RefreshToken:     newTokens.RefreshToken,
		IDToken:          newTokens.IDToken,
		ExpiresAt:        newTokens.ExpiresAt,
		SessionExpiresAt: sessionExpiresAt,
		UserID:           expired.UserID,
		UpstreamSubject:  expired.UpstreamSubject,
		ClientID:         expired.ClientID,
	}

	// Detect rotation BEFORE the old-RT backfill below so the original
	// (provider-issued) value is compared, not the backfilled fallback.
	rotated := updated.RefreshToken != "" && updated.RefreshToken != expired.RefreshToken

	// If the provider didn't rotate the refresh token, keep the original.
	if updated.RefreshToken == "" {
		updated.RefreshToken = expired.RefreshToken
	}

	// OIDC Core 1.0 §12.2 permits but does not require a new id_token on refresh.
	// When the provider omits one, keep the ID token captured at the initial login
	// so it is not erased from storage. The write below replaces the whole row,
	// so without this the persisted IDToken would be overwritten with "" and the
	// original login ID token would be lost for the remainder of the session.
	// Mirrors the RefreshToken carry-forward above.
	if updated.IDToken == "" {
		updated.IDToken = expired.IDToken
	}

	// expectedRefreshToken is the value this call actually redeemed with the
	// upstream provider. Writing through CompareAndSwapUpstreamTokens (rather
	// than an unconditional StoreUpstreamTokens) makes the STORED row
	// deterministic when another process — a different replica of this auth
	// server sharing this storage — is redeeming the same refresh token
	// concurrently: only the replica whose expected value still matches the
	// stored row may write, so a losing replica cannot clobber the winner's
	// rotated token with its own now-stale redemption. This orders the writes;
	// it does not by itself guarantee the upstream provider accepts both
	// redemptions (see the type-level doc comment above).
	expectedRefreshToken := expired.RefreshToken

	if err := r.compareAndSwapWithRetry(ctx, sessionID, expired.ProviderID, expectedRefreshToken, updated); err != nil {
		if errors.Is(err, storage.ErrConcurrentRefresh) {
			return r.resolveConcurrentRefreshConflict(ctx, sessionID, expired.ProviderID)
		}

		if !rotated {
			// The old refresh token is still valid in storage; the caller can
			// proceed with the refreshed access token for this request.
			slog.Warn("failed to persist refreshed upstream tokens; old refresh token still valid",
				"provider_id", expired.ProviderID,
				"error", err,
			)
			// Log session_id at DEBUG only; it is credential-adjacent.
			slog.Debug("upstream token persist failure session", "session_id", sessionID)
			return updated, nil
		}

		// The IdP rotated the refresh token: the new RT was redeemed but could
		// not be persisted. The old RT is now dead in storage and will trigger
		// reuse-detection lockout on the next refresh attempt.
		//
		// Residual window: if the process crashes here, the dead old RT stays in
		// storage. ErrNotFound on the next refresh attempt forces re-auth, which
		// is the backstop. Store-intent-before-redeem is out of scope for this fix.
		deleteCtx, deleteCancel := context.WithTimeout(context.WithoutCancel(ctx), upstreamDeleteTimeout)
		defer deleteCancel()
		if delErr := r.storage.DeleteUpstreamTokensForProvider(deleteCtx, sessionID, expired.ProviderID); delErr != nil {
			slog.Error("failed to delete stale upstream token row after rotation persist failure",
				"provider_id", expired.ProviderID,
				"error", delErr,
			)
			// Log session_id at DEBUG only; it is credential-adjacent.
			slog.Debug("stale upstream token row delete failure session", "session_id", sessionID)
		}
		return nil, fmt.Errorf(
			"failed to persist rotated upstream refresh token for provider %q: %w",
			expired.ProviderID, err,
		)
	}

	slog.Debug("upstream tokens refreshed successfully",
		"session_id", sessionID,
		"provider_id", expired.ProviderID,
	)

	return updated, nil
}

// resolveConcurrentRefreshConflict is reached after CompareAndSwapUpstreamTokens
// returns storage.ErrConcurrentRefresh: the stored refresh token no longer
// matched the value this call redeemed with. That mismatch has two distinct
// causes this function must tell apart on re-read, not just to pick the right
// return value but to log something a human can act on:
//   - Another process (typically a different replica of this auth server)
//     redeemed and persisted a rotation of the same row first. This IS a
//     genuine lost race, worth surfacing loudly.
//   - The row no longer exists at all — deleted by a logout, evicted by TTL,
//     or never created. This is NOT a race: nothing else needed to "win"
//     against this call, the row was simply gone before the write. Logging it
//     the same as the case above would send whoever is on call chasing a
//     concurrency bug that didn't happen.
//
// The re-read result decides which happened: an unexpired row means the other
// process's write is the correct result to hand back to this call — this
// call's own redemption produced a token pair the IdP (or the winning
// replica) already superseded, so it must not be used or written. A read that
// comes back storage.ErrNotFound means the row is gone; CompareAndSwapUpstreamTokens
// correctly refused to resurrect it (StoreUpstreamTokens would not have). Any
// other outcome (still expired, or a genuine read error) is ambiguous and
// treated as the loss it most likely is. In every non-recoverable case the
// refresh token this call redeemed is unusable going forward — dead at the
// IdP if it enforces single-use, and either superseded or deliberately absent
// in storage — so the caller must surface an error rather than retry the same
// redemption.
func (r *upstreamTokenRefresher) resolveConcurrentRefreshConflict(
	ctx context.Context, sessionID, providerID string,
) (*storage.UpstreamTokens, error) {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upstreamConflictReadTimeout)
	defer cancel()

	winner, err := r.storage.GetUpstreamTokens(readCtx, sessionID, providerID)
	if err == nil && winner != nil && !winner.IsExpired(time.Now()) {
		slog.Debug("lost concurrent upstream token refresh race; using winning replica's tokens",
			"session_id", sessionID,
			"provider_id", providerID,
		)
		return winner, nil
	}

	if errors.Is(err, storage.ErrNotFound) {
		slog.Warn("upstream token row no longer exists after a concurrent-refresh conflict; "+
			"this is expected on logout or TTL eviction and is not necessarily a lost race",
			"provider_id", providerID,
		)
	} else {
		slog.Error("lost concurrent upstream token refresh race and re-read found no usable winner; "+
			"the redeemed refresh token is unrecoverable for this attempt",
			"provider_id", providerID,
			"error", err,
		)
	}
	// Log session_id at DEBUG only; it is credential-adjacent.
	slog.Debug("concurrent upstream token refresh conflict session", "session_id", sessionID)
	if err != nil {
		return nil, fmt.Errorf(
			"concurrent upstream token refresh for provider %q: %w: %w",
			providerID, storage.ErrConcurrentRefresh, err,
		)
	}
	return nil, fmt.Errorf(
		"concurrent upstream token refresh for provider %q: %w",
		providerID, storage.ErrConcurrentRefresh,
	)
}

// rotatedElsewhere is consulted when the upstream provider rejected this
// call's refresh token with invalid_grant. A rotating provider answers that
// way to the replica that loses a cross-process refresh race (singleflight
// only deduplicates within this process), even though the winner has already
// stored a valid row. The refresh lease (see coordinatedRefresh) is what
// normally stops two replicas reaching the provider at all, so this path now
// covers the cases it cannot: a backend that provides no lease, and a lease
// that expired under a holder slow enough for a waiter to take over. If the stored row now holds a different refresh token
// and an unexpired access token, that row is the result to use, so a lost race
// is not reported as a dead upstream credential.
//
// "Another process refreshed" is tested by refresh-token inequality, unlike
// rowChanged, which also compares the access token, ID token and expiry. The
// rejection is of the refresh token itself, so only a row holding a different
// refresh token can be a successor; a row that changed in other fields but
// still holds the rejected refresh token is just as dead.
//
// This narrows the race but does not close it: when the winner's storage write
// has not landed by the time of the re-read, the rejection is still returned.
// Any read failure also returns nil, leaving the caller's original error.
func (r *upstreamTokenRefresher) rotatedElsewhere(
	ctx context.Context, sessionID string, expired *storage.UpstreamTokens,
) *storage.UpstreamTokens {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upstreamConflictReadTimeout)
	defer cancel()

	stored, err := r.storage.GetUpstreamTokens(readCtx, sessionID, expired.ProviderID)
	if err != nil {
		// ErrExpired and ErrNotFound are ordinary outcomes here: the row is
		// still the rejected one, or it is gone. Anything else is a storage
		// fault worth a diagnostic, though the caller's rejection still stands.
		if !errors.Is(err, storage.ErrExpired) && !errors.Is(err, storage.ErrNotFound) {
			slog.Debug("re-read after upstream invalid_grant failed; returning the rejection",
				"session_id", sessionID,
				"provider_id", expired.ProviderID,
				"error", err,
			)
		}
		return nil
	}
	if stored == nil || stored.RefreshToken == expired.RefreshToken || stored.IsExpired(time.Now()) {
		return nil
	}
	slog.Debug("upstream rejected a refresh token another process already rotated; using the stored row",
		"session_id", sessionID,
		"provider_id", expired.ProviderID,
	)
	return stored
}

// compareAndSwapWithRetry attempts CompareAndSwapUpstreamTokens up to
// upstreamStoreMaxAttempts times, waiting upstreamStoreRetryBackoff between
// attempts. Returns nil on first success. A storage.ErrConcurrentRefresh
// result is returned immediately without retrying: the comparison failed
// because expectedRefreshToken is now definitively stale, and retrying the
// identical compare-and-swap cannot change that outcome. For any other error,
// returns the last error after all attempts are exhausted; ctx-cancellation
// short-circuits between attempts and returns the last store error (not
// ctx.Err()).
func (r *upstreamTokenRefresher) compareAndSwapWithRetry(
	ctx context.Context,
	sessionID, providerID, expectedRefreshToken string,
	updated *storage.UpstreamTokens,
) error {
	var lastErr error
	for attempt := 1; attempt <= upstreamStoreMaxAttempts; attempt++ {
		casErr := r.storage.CompareAndSwapUpstreamTokens(ctx, sessionID, providerID, expectedRefreshToken, updated)
		if casErr == nil {
			return nil
		}
		if errors.Is(casErr, storage.ErrConcurrentRefresh) {
			return casErr
		}
		lastErr = casErr
		slog.Debug("failed to store refreshed upstream tokens",
			"session_id", sessionID,
			"provider_id", providerID,
			"attempt", attempt,
			"error", lastErr,
		)
		if attempt < upstreamStoreMaxAttempts {
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(upstreamStoreRetryBackoff):
			}
		}
	}
	return lastErr
}

// rowChanged reports whether the stored row was rewritten since the caller read
// it, i.e. another caller already refreshed it. Expiry is compared as well as
// the token values so a provider that re-issues the same access token string
// with a new expiry (and does not rotate the refresh token) still counts as a
// change, and the caller does not redeem again.
func rowChanged(callerRow, stored *storage.UpstreamTokens) bool {
	return callerRow.AccessToken != stored.AccessToken ||
		callerRow.RefreshToken != stored.RefreshToken ||
		callerRow.IDToken != stored.IDToken ||
		!callerRow.ExpiresAt.Equal(stored.ExpiresAt)
}
