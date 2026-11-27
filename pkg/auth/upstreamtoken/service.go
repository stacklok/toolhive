// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto/oautherr"
)

const (
	// idTokenExpirySkew is how far ahead of the ID token's `exp` the
	// ID-token trigger fires, so a token is not handed out moments before it
	// expires.
	idTokenExpirySkew = 30 * time.Second

	// defaultIDTokenSuppression bounds how long the ID-token trigger stays
	// suppressed for a row whose access token asserts no expiry and whose
	// session expiry is unknown, so the suppression map cannot grow without
	// bound.
	defaultIDTokenSuppression = time.Hour

	// idTokenRefreshFailureBackoff is how long the ID-token trigger stays
	// suppressed after a failed ID-token-triggered refresh. It is short so a
	// transient upstream error does not reopen the stale-ID-token window for
	// the rest of the access token's lifetime.
	idTokenRefreshFailureBackoff = time.Minute

	// idTokenSuppressionSweepInterval rate-limits the full sweep of expired
	// suppression entries, which runs under mu. Entries are also dropped
	// lazily when read after expiry.
	idTokenSuppressionSweepInterval = time.Minute

	// refreshWorkerTimeout bounds a per-user refresh worker after its caller
	// left: the refresher's row resolution before it starts the refresh, and
	// the worker's own suppression lookups afterwards. It is longer than the
	// refresher's own refresh timeout (30s in pkg/authserver), which bounds the
	// provider call separately and is not cut short by this one.
	refreshWorkerTimeout = time.Minute
)

// errCallerLeft marks a per-user refresh whose caller's context ended before
// the result arrived, or before the refresh started. Any refresh worker
// already running then owns recording its outcome.
var errCallerLeft = errors.New("caller left before the refresh finished")

// InProcessService implements the Service interface for in-process use.
// It composes storage (read) and refresher (refresh + persist) to provide a
// single GetValidTokens call. Concurrent-refresh deduplication is delegated to
// the refresher's own singleflight.Group keyed by an opaque storage-row identity,
// so the same Group covers both the handler's chain-walk path and the runtime
// token-swap path within this process.
//
// Refresh is triggered by an expired access token or by an expired ID token
// (its `exp` claim, read without re-verifying the signature it was validated
// with when stored). The ID-token trigger serves providers whose ID-token
// lifetime is shorter than their access-token lifetime (e.g. Microsoft Entra
// ID), which would otherwise hand consumers an expired ID token for part of
// every access-token lifetime. It never fires when the row has no refresh
// token, or for an empty, unparsable or `exp`-less ID token.
//
// It cannot help when the provider omits id_token on refresh (OIDC Core 1.0
// §12.2): the expired ID token is then carried forward, and the trigger is
// suppressed for that row until the access token next expires so reads do not
// loop on refresh. After a failed ID-token-triggered refresh the trigger backs
// off briefly (idTokenRefreshFailureBackoff). Consumers that need a currently
// valid ID token must therefore still check `exp` themselves.
//
// Suppression state is held in process memory: replicas sharing storage each
// keep their own, so each may perform one extra refresh per access-token
// lifetime for a provider that omits id_token. Likewise, refresh
// deduplication is per process; replicas that refresh the same row at the same
// moment can each redeem its refresh token (see upstreamTokenRefresher), which
// a provider enforcing single-use refresh-token rotation may treat as reuse.
// The ID-token trigger adds refresh events, but no new race shape.
type InProcessService struct {
	storage   storage.UpstreamTokenStorage
	refresher storage.UpstreamTokenRefresher
	// userKeyed is set when storage is, or decorates, the user-keyed layer.
	userKeyed bool

	// mu guards idTokenSuppressions and lastSuppressionSweep.
	mu sync.Mutex
	// idTokenSuppressions records rows for which an ID-token-triggered refresh
	// should not be attempted again until the access token next expires,
	// because a previous refresh left the ID token expired (the provider
	// omitted id_token, or the refresh failed). Keyed by suppressionKeyFor.
	idTokenSuppressions  map[string]idTokenSuppression
	lastSuppressionSweep time.Time
}

// idTokenSuppression pins a suppression to the exact stale ID token it was
// recorded for, so a rotated ID token is never suppressed.
type idTokenSuppression struct {
	idToken string
	until   time.Time
}

// Compile-time checks.
var (
	_ Service              = (*InProcessService)(nil)
	_ TokenReader          = (*InProcessService)(nil)
	_ UserCredentialReader = (*InProcessService)(nil)
	_ UserKeyedReader      = (*InProcessService)(nil)
)

// NewInProcessService creates a new InProcessService.
// The refresher may be nil if upstream token refresh is not configured;
// expired tokens will return ErrNoRefreshToken in that case.
func NewInProcessService(
	stor storage.UpstreamTokenStorage,
	refresher storage.UpstreamTokenRefresher,
) *InProcessService {
	return &InProcessService{
		storage:             stor,
		refresher:           refresher,
		userKeyed:           storage.IsUserKeyed(stor),
		idTokenSuppressions: make(map[string]idTokenSuppression),
	}
}

// UserKeyed reports whether the service reads storage keyed by user: the
// user-keyed layer itself, or a decorator above it that exposes
// Unwrap() storage.Storage or Unwrap() storage.UpstreamTokenStorage.
func (s *InProcessService) UserKeyed() bool {
	return s.userKeyed
}

// GetValidTokens returns a valid upstream credential for a session and provider.
// It transparently refreshes expired access tokens using the refresh token,
// and refreshes expired ID tokens where possible (see InProcessService).
// Unlike GetValidTokensForUser, it waits for an in-flight refresh to return even
// if ctx ends, because the caller may hold a generation lease on ctx until the
// read returns.
func (s *InProcessService) GetValidTokens(ctx context.Context, sessionID, providerName string) (*UpstreamCredential, error) {
	tokens, err := s.storage.GetUpstreamTokens(ctx, sessionID, providerName)
	if err != nil {
		// ErrExpired returns tokens (including refresh token) alongside the error.
		// Attempt a refresh before giving up.
		if errors.Is(err, storage.ErrExpired) {
			if tokens != nil {
				return s.refreshOrFail(ctx, sessionID, providerName, tokens, true, "", false)
			}
			// Expired but storage returned nil tokens — can't refresh.
			return nil, ErrNoRefreshToken
		}
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrSessionNotFound
		}
		if errors.Is(err, storage.ErrInvalidBinding) {
			return nil, ErrInvalidBinding
		}
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	// Defense in depth: some storage implementations may return tokens
	// without checking expiry (the interface does not require it).
	if !tokens.ExpiresAt.IsZero() && tokens.IsExpired(time.Now()) {
		return s.refreshOrFail(ctx, sessionID, providerName, tokens, true, "", false)
	}

	if s.shouldRefreshForIDToken(ctx, sessionID, providerName, tokens) {
		return s.refreshForIDToken(ctx, sessionID, providerName, tokens, true, "")
	}

	return &UpstreamCredential{AccessToken: tokens.AccessToken, IDToken: tokens.IDToken}, nil
}

// GetValidTokensForUser returns a valid upstream credential for one user and
// provider, refreshing an expired row. It sets the canonical user on the
// context, so over user-keyed storage the user's row is read whatever login
// session the caller has. It compares the row's UserID with userID itself
// before any refresh, so it is also safe over storage that is not user-keyed.
//
// It returns, all wrapped so the cause stays reachable with errors.Is:
//   - ErrSessionNotFound for an empty userID
//   - ErrNeedsAuthorization (with ErrSessionNotFound, ErrNoRefreshToken or
//     ErrRefreshFailed and its upstream.ErrGrantRejected cause) when the user
//     has no usable connection: no row, a placeholder row, a live row with an
//     empty access token, an expired row that cannot be refreshed, or a
//     refresh the provider rejected with invalid_grant. A refresh that
//     succeeds but yields an empty access token counts as a row with no
//     access token
//   - ErrInvalidBinding for a row owned by another user, whether found on the
//     first read or during the refresh
//   - ErrNoCanonicalUser (from storage, passed through) is a separate case
//     that callers must refuse: it is not a missing connection
//   - any other error for a storage failure or a transient refresh failure
//
// When only the ID token needed refreshing and the access token is still
// live, a failed refresh, including invalid_grant, is not an error: the stored
// credential is returned and the failure is logged at WARN, because the
// caller never sees it. If ctx ended during the refresh, see below.
// Needs-authorization follows once the access token expires. The exception is
// a refresh after which the refresher removed the row, or cleared its refresh
// token, because it could not save a rotated refresh token: that needs
// authorization at once, unless ctx ends during the refresh. In that case this
// read still returns the stored credential and the next read reports
// needs-authorization. The refresher still logs on its own paths, so a rare refresher
// failure (a lost refresh race, or a row it could not delete or clear) shows up both
// in its own line and in this WARN.
//
// For a failed refresh of an expired access token it adds no WARN of its own:
// the caller decides how loudly that failure is reported, so one failure does
// not produce two WARN lines. The refresher still logs on its own paths.
//
// The read returns promptly when ctx ends, even if a refresh is in progress.
// A detached refresh can finish and persist the updated row in the background
// under the refresher's own timeout. Cancellation while waiting for an expired
// access token to refresh matches both ErrRefreshFailed and ctx.Err(). When only
// the ID token needed refreshing and ctx ends during the refresh, the read
// returns the stored credential with a nil error and does not re-read storage.
// In every ID-token-only case, if the stored access token has expired by the
// time the refresh fails, the read returns the refresh error instead; when ctx
// ended, that error also matches ErrRefreshFailed and ctx.Err(). If an
// ID-token-only refresh fails in the background after the caller left, the
// failure is logged once at WARN, and the ID-token trigger backs off only
// while the access token is still live. A failure that reports the row missing
// or owned by another user is not logged or backed off there; the next read
// reports it. The initial read, row-ID lookup and ID-token re-read use the
// caller's ctx; the refresher runs detached from its cancellation and
// deadline, under its own deadline of refreshWorkerTimeout. The worker's
// recording resolves the row ID with its own bounded context, not the caller's
// ctx, and shares that refreshWorkerTimeout deadline with the refresh.
//
// Error text can include provider-supplied detail, such as an OAuth
// error_description. Never return it to a client. Wrapped causes stay
// reachable with errors.Is and errors.As.
func (s *InProcessService) GetValidTokensForUser(
	ctx context.Context, userID, providerName string,
) (*UpstreamCredential, error) {
	if userID == "" {
		return nil, ErrSessionNotFound
	}
	ctx = identity.WithCanonicalUser(ctx, userID)
	rowKey := userRowKey(userID)

	tokens, err := s.storage.GetUpstreamTokens(ctx, rowKey, providerName)
	if err != nil && !errors.Is(err, storage.ErrExpired) {
		return nil, classifyUserReadError(err)
	}
	if tokens == nil {
		if err != nil {
			return nil, needsAuthorization(ErrNoRefreshToken, err)
		}
		return nil, needsAuthorization(ErrSessionNotFound)
	}
	if tokens.UserID != userID {
		return nil, ErrInvalidBinding
	}

	expired := err != nil || (!tokens.ExpiresAt.IsZero() && tokens.IsExpired(time.Now()))
	switch {
	case !expired && tokens.AccessToken == "":
		return nil, needsAuthorization(ErrSessionNotFound)
	case expired:
		cred, refreshErr := s.refreshOrFail(ctx, rowKey, providerName, tokens, false, userID, false)
		return usableUserCredential(cred, classifyUserRefreshError(refreshErr))
	case s.shouldRefreshForIDToken(ctx, rowKey, providerName, tokens):
		cred, refreshErr := s.refreshForIDToken(ctx, rowKey, providerName, tokens, false, userID)
		return usableUserCredential(cred, classifyUserRefreshError(refreshErr))
	}
	return &UpstreamCredential{AccessToken: tokens.AccessToken, IDToken: tokens.IDToken}, nil
}

// GetAllUpstreamCredentials returns access tokens and ID tokens for all upstream
// providers in a session in a single storage round-trip. Expired access tokens
// are refreshed transparently; providers whose access token cannot be refreshed
// are included in the returned failed map, keyed by provider name with the
// refresh error as the value, so downstream middleware can return a clean 401
// for the access-token use case. The IDToken on a returned entry is
// the rotated ID token when a refresh produced one (OIDC Core 1.0 §12.2),
// otherwise the original JWT captured at the initial OIDC login; it is not
// independently validated for freshness and may be empty if the upstream login
// never yielded one. An expired ID token also triggers a refresh (see
// InProcessService); a failed ID-token-triggered refresh does not put the
// provider in the failed map, since its access token is still valid, unless the
// refresher removed the row or cleared its refresh token because it could not
// save a rotated refresh token. Callers
// that PRESENT it as a credential must expect expiry to be rejected; callers
// that only READ ITS CLAIMS are unaffected by expiry. See
// Identity.UpstreamIDTokens for both consumers.
//
// Returns an empty map and nil failed map (not error) for unknown sessions.
//
// Unlike GetValidTokensForUser, it waits for an in-flight refresh to return even
// if ctx ends, because the caller may hold a generation lease on ctx until the
// read returns.
func (s *InProcessService) GetAllUpstreamCredentials(
	ctx context.Context, sessionID string,
) (map[string]UpstreamCredential, map[string]error, error) {
	allTokens, err := s.storage.GetAllUpstreamTokens(ctx, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("bulk read upstream tokens: %w", err)
	}

	if len(allTokens) == 0 {
		return map[string]UpstreamCredential{}, nil, nil
	}

	result := make(map[string]UpstreamCredential, len(allTokens))
	var failed map[string]error
	fail := func(providerName string, err error) {
		if failed == nil {
			failed = make(map[string]error)
		}
		failed[providerName] = err
	}
	// TODO(auth): Refresh providers in parallel using errgroup to avoid
	// worst-case latency of N refreshes when multiple providers need refresh.
	for providerName, tokens := range allTokens {
		if tokens == nil {
			continue
		}
		cred, credErr := s.credentialFor(ctx, sessionID, providerName, tokens)
		if credErr != nil {
			fail(providerName, credErr)
			continue
		}
		result[providerName] = cred
	}

	// The returned ID tokens are deliberately not checked for expiry here. Neither
	// of today's two consumers wants that pre-check: one presents the token as an
	// RFC 8693 subject_token and relies on the IdP to reject an expired assertion
	// (pkg/vmcp/auth/strategies surfaces the resulting invalid_grant), and the other
	// only reads its claims, for which expiry is irrelevant. Enforcing it here would
	// break the second while duplicating what the IdP already does for the first.
	// A future consumer that presents the token to a sink which does NOT validate
	// `exp` would change that calculus — such a caller must check it itself.
	// An expired ID token is refreshed when the provider can rotate it, but this
	// still does not filter what it returns: an ID token the
	// provider declined to rotate is passed through expired.
	// See Identity.UpstreamIDTokens for both.
	return result, failed, nil
}

// credentialFor returns the usable credential for one stored row, refreshing it
// when the access token is expired or the ID token needs rotating. A refresh
// failure is logged here and returned so the caller records it as failed.
func (s *InProcessService) credentialFor(
	ctx context.Context, sessionID, providerName string, tokens *storage.UpstreamTokens,
) (UpstreamCredential, error) {
	// If token is not expired, use it directly.
	if tokens.ExpiresAt.IsZero() || !tokens.IsExpired(time.Now()) {
		if s.shouldRefreshForIDToken(ctx, sessionID, providerName, tokens) {
			cred, idErr := s.refreshForIDToken(ctx, sessionID, providerName, tokens, true, "")
			if idErr != nil {
				slog.WarnContext(ctx, "upstream credential unusable after a failed refresh; provider will require re-authentication",
					"session_id", sessionID,
					"provider", providerName,
					"error", idErr,
				)
				return UpstreamCredential{}, idErr
			}
			return *cred, nil
		}
		return UpstreamCredential{
			AccessToken: tokens.AccessToken,
			IDToken:     tokens.IDToken,
		}, nil
	}

	// Token is expired — attempt refresh.
	refreshed, refreshErr := s.refreshOrFail(ctx, sessionID, providerName, tokens, true, "", false)
	if refreshErr != nil {
		slog.WarnContext(ctx, "upstream token refresh failed; provider will require re-authentication",
			"session_id", sessionID,
			"provider", providerName,
			"error", refreshErr,
		)
		return UpstreamCredential{}, refreshErr
	}
	// refreshOrFail carries through the rotated ID token when the provider
	// issued one, otherwise the original login ID token (see its doc).
	return *refreshed, nil
}

// refreshOrFail attempts a refresh via the shared refresher and maps errors to
// the service's sentinel errors. Deduplication of concurrent refreshes for the
// same logical upstream-token row is handled inside the refresher using an
// opaque storage-row identity. warnOnFailure controls the WARN for a failed
// refresh: a caller that logs the failure itself passes false so one failure
// is not logged twice. idTokenOnly is true when the access token is still
// live and only the ID token triggered the refresh; on the per-user path it
// tells the refresh worker whether to record a failure after the caller left.
func (s *InProcessService) refreshOrFail(
	ctx context.Context,
	sessionID string,
	providerName string,
	expired *storage.UpstreamTokens,
	warnOnFailure bool,
	expectedUserID string,
	idTokenOnly bool,
) (*UpstreamCredential, error) {
	if expired.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}

	if s.refresher == nil {
		slog.Debug("token refresher not configured, cannot refresh upstream tokens",
			"session_id", sessionID,
			"provider", providerName,
		)
		return nil, ErrNoRefreshToken
	}

	// Session reads must wait for RefreshAndStore: the caller may hold a
	// generation lease on ctx that it releases when the read returns.
	var refreshed *storage.UpstreamTokens
	var err error
	if expectedUserID != "" {
		refreshed, err = s.refreshDetached(ctx, sessionID, providerName, expired, expectedUserID, idTokenOnly)
	} else {
		refreshed, err = s.refresher.RefreshAndStore(ctx, sessionID, expired)
	}
	if err != nil {
		if warnOnFailure {
			slog.Warn("upstream token refresh failed",
				"session_id", sessionID,
				"provider", providerName,
				"error", err,
			)
		}
		return nil, fmt.Errorf("%w: %w", ErrRefreshFailed, err)
	}

	if refreshed == nil {
		return nil, ErrRefreshFailed
	}

	if expectedUserID != "" && refreshed.UserID != expectedUserID {
		slog.DebugContext(ctx, "refreshed upstream row is owned by another user",
			"provider", providerName,
		)
		return nil, ErrInvalidBinding
	}

	idToken := s.recordRefreshOutcome(ctx, sessionID, providerName, expired, refreshed)

	return &UpstreamCredential{AccessToken: refreshed.AccessToken, IDToken: idToken}, nil
}

// recordRefreshOutcome returns the ID token for a refreshed row and, when that
// token is still expired, suppresses the ID-token trigger for it. Prefer the ID
// token from the refresh response when the provider rotated it (OIDC Core 1.0
// §12.2 permits, but does not require, a new id_token on refresh). Fall back to
// the original login ID token when the response omitted one. The primary
// carry-forward into storage is in upstreamTokenRefresher.refreshAndStore; this
// fallback is defense-in-depth so the caller never sees an empty subject token.
//
// A refresh that still leaves the ID token expired (the provider omitted
// id_token) must not be retried on every read. The suppression covers this exact
// ID token until the new access token expires, when the access-token trigger
// refreshes anyway. Recording the same outcome twice is harmless.
func (s *InProcessService) recordRefreshOutcome(
	ctx context.Context, sessionID, providerName string, expired, refreshed *storage.UpstreamTokens,
) string {
	idToken := refreshed.IDToken
	if idToken == "" {
		idToken = expired.IDToken
	}
	if idTokenExpired(idToken, time.Now()) {
		s.suppressIDTokenRefresh(s.suppressionKeyFor(ctx, sessionID, providerName),
			idToken, refreshed.ExpiresAt, refreshed.SessionExpiresAt)
	}
	return idToken
}

// refreshDetached bounds the wait, not the refresh. The worker runs the
// refresher detached from ctx's cancellation and deadline (keeping its values)
// under its own refreshWorkerTimeout, because the refresher uses its context
// for row resolution before it detaches the refresh itself, and a caller
// leaving in that window must not abort a refresh that continues.
//
// Exactly one side records the outcome, decided by which branch the caller's
// select takes. If the caller receives the result, the caller records it. If
// ctx ends first, the caller closes abandoned and returns an error wrapping
// errCallerLeft and ctx.Err(); the worker then records the ID-token
// suppression for a success on the expected owner's row, or the failure
// backoff for an ID-token-only refresh. The worker never blocks: done is
// received or abandoned is closed on every return path of the caller.
func (s *InProcessService) refreshDetached(
	ctx context.Context, sessionID, providerName string, expired *storage.UpstreamTokens,
	expectedUserID string, idTokenOnly bool,
) (*storage.UpstreamTokens, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", errCallerLeft, err)
	}
	type result struct {
		tokens *storage.UpstreamTokens
		err    error
	}
	done := make(chan result)
	abandoned := make(chan struct{})
	go func() {
		workerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshWorkerTimeout)
		defer cancel()
		tokens, err := s.refresher.RefreshAndStore(workerCtx, sessionID, expired)
		select {
		case done <- result{tokens, err}:
			return
		case <-abandoned:
		}
		switch {
		case err == nil && tokens != nil && tokens.UserID == expectedUserID:
			s.recordRefreshOutcome(workerCtx, sessionID, providerName, expired, tokens)
		case idTokenOnly && (err != nil || tokens == nil):
			if err == nil {
				err = errors.New("refresh returned no tokens")
			}
			s.recordAbandonedIDTokenFailure(workerCtx, sessionID, providerName, expired, err)
		}
	}()
	select {
	case r := <-done:
		return r.tokens, r.err
	case <-ctx.Done():
		close(abandoned)
		return nil, fmt.Errorf("%w: %w", errCallerLeft, ctx.Err())
	}
}

// recordAbandonedIDTokenFailure handles an ID-token-only refresh that failed
// after its caller left. refreshForIDToken returned the stored credential
// without recording a backoff, so the worker records it and logs the failure
// once. A failure that reports the row missing or owned by another user is
// skipped: the next read reports it.
func (s *InProcessService) recordAbandonedIDTokenFailure(
	ctx context.Context, sessionID, providerName string, expired *storage.UpstreamTokens, err error,
) {
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrInvalidBinding) || errors.Is(err, ErrInvalidBinding) {
		return
	}
	// The backoff is skipped when the access token has expired since, because
	// the next read refreshes for that anyway; the failure is still logged.
	s.recordIDTokenRefreshFailure(ctx, sessionID, providerName, expired)
	// No session ID: on the per-user path it is derived from the user ID.
	slog.WarnContext(ctx, "upstream ID token refresh failed after the caller left",
		"provider", providerName,
		"reason", refreshFailureReason(err),
	)
}

// recordIDTokenRefreshFailure backs the ID-token trigger off for the stored ID
// token of tokens for idTokenRefreshFailureBackoff, or until the access token
// expires if sooner. It records nothing when that time is not in the future.
func (s *InProcessService) recordIDTokenRefreshFailure(
	ctx context.Context, sessionID, providerName string, tokens *storage.UpstreamTokens,
) {
	now := time.Now()
	until := now.Add(idTokenRefreshFailureBackoff)
	if !tokens.ExpiresAt.IsZero() && tokens.ExpiresAt.Before(until) {
		until = tokens.ExpiresAt
	}
	if !until.After(now) {
		return
	}
	s.suppressIDTokenRefresh(s.suppressionKeyFor(ctx, sessionID, providerName), tokens.IDToken, until, time.Time{})
}

// shouldRefreshForIDToken reports whether the ID-token trigger applies to a
// row whose access token is still valid: a refresh token exists, the stored ID token is expired, and the trigger is not
// suppressed for that ID token.
func (s *InProcessService) shouldRefreshForIDToken(
	ctx context.Context, sessionID, providerName string, tokens *storage.UpstreamTokens,
) bool {
	if tokens.RefreshToken == "" {
		return false
	}
	now := time.Now()
	if !idTokenExpired(tokens.IDToken, now) {
		return false
	}
	return !s.idTokenRefreshSuppressed(s.suppressionKeyFor(ctx, sessionID, providerName), tokens.IDToken, now)
}

// refreshForIDToken refreshes a row whose access token is still valid but whose
// ID token has expired. A failed refresh is deliberately not fatal: the stored
// credential is returned unchanged, since its access token is still usable and
// a transient upstream error should not force re-authentication. If the stored
// access token has expired by the time the refresh fails, the refresh error is
// returned instead, whether or not the caller is still waiting. The trigger
// then backs off for idTokenRefreshFailureBackoff (or until the access token
// expires, if sooner) so a failing upstream is not hammered on every read.
//
// When the caller left before the refresh finished (on the per-user path, the
// error wraps errCallerLeft; on session paths, ctx has ended), this function
// records no backoff and returns the stored credential without re-reading
// storage. On the per-user path the refresh worker records the backoff, and
// logs the failure once, if the background refresh fails; the backoff is
// skipped once the access token has expired.
//
// The one fatal case is a row the failed refresh removed: the refresher deletes
// it when the provider rotated the refresh token but the rotation could not be
// persisted, leaving the old one dead. Serving the stored copy would then hand
// out a credential that no longer exists, so the row is re-read and, if it is
// gone, the refresh error is returned wrapped together with the read's
// storage.ErrNotFound. With user-keyed storage the refresher keeps the row,
// clears its refresh token and expires its access token instead; that error
// wraps storage.ErrNotFound as well and is returned before the re-read, so the
// stored credential is not served.
//
// A non-empty expectedUserID is the per-user path. The owner check inside
// refreshOrFail returns ErrInvalidBinding, and a binding error from the
// re-read (storage.ErrInvalidBinding or ErrInvalidBinding) is returned as
// ErrInvalidBinding together with the read error. Neither falls back to the
// stored credential, since the row is not the user's.
func (s *InProcessService) refreshForIDToken(
	ctx context.Context, sessionID, providerName string, tokens *storage.UpstreamTokens, warnOnFailure bool,
	expectedUserID string,
) (*UpstreamCredential, error) {
	refreshed, err := s.refreshOrFail(ctx, sessionID, providerName, tokens, warnOnFailure, expectedUserID, true)
	if err == nil {
		return refreshed, nil
	}
	if expectedUserID != "" && (errors.Is(err, ErrInvalidBinding) || errors.Is(err, storage.ErrInvalidBinding)) {
		return nil, err
	}
	if s.userKeyed && errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	// A refresh can outlive the stored access token; never hand out an expired one.
	if !tokens.ExpiresAt.IsZero() && tokens.IsExpired(time.Now()) {
		return nil, err
	}
	// With warnOnFailure, refreshOrFail has already logged the failure at WARN.
	if callerLeftRefresh(ctx, err, expectedUserID) {
		return &UpstreamCredential{AccessToken: tokens.AccessToken, IDToken: tokens.IDToken}, nil
	}
	_, readErr := s.storage.GetUpstreamTokens(ctx, sessionID, providerName)
	if errors.Is(readErr, storage.ErrNotFound) {
		return nil, fmt.Errorf("%w: %w", err, readErr)
	}
	if expectedUserID != "" &&
		(errors.Is(readErr, storage.ErrInvalidBinding) || errors.Is(readErr, ErrInvalidBinding)) {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBinding, readErr)
	}
	// No session ID: on the per-user path it is derived from the user ID.
	slog.DebugContext(ctx, "returning stored upstream tokens after failed refresh for expired ID token",
		"provider", providerName,
	)
	if !warnOnFailure {
		// The caller gets a credential and never sees the failure, so this is
		// the only place a provider that fails every ID-token refresh shows up.
		// No session ID: on the per-user path it is derived from the user ID.
		slog.WarnContext(ctx, "upstream ID token refresh failed; returning the stored credential",
			"provider", providerName,
			"reason", refreshFailureReason(err),
		)
	}
	s.recordIDTokenRefreshFailure(ctx, sessionID, providerName, tokens)
	return &UpstreamCredential{AccessToken: tokens.AccessToken, IDToken: tokens.IDToken}, nil
}

// callerLeftRefresh reports whether the caller stopped waiting for the
// refresh that failed with err. On the per-user path only errCallerLeft says
// so, because ctx can end after the caller already received the failure, and
// the refresh worker records the outcome only in the errCallerLeft case.
// Session paths wait for the refresher, so there ctx having ended is the test.
func callerLeftRefresh(ctx context.Context, err error, expectedUserID string) bool {
	if expectedUserID != "" {
		return errors.Is(err, errCallerLeft)
	}
	return ctx.Err() != nil
}

// suppressIDTokenRefresh records that the ID-token trigger must not fire for
// idToken on the row identified by key (see suppressionKeyFor) until the access
// token expires. When the access token
// asserts no expiry, the session expiry (or defaultIDTokenSuppression) bounds
// the entry instead. Expired entries are swept at most once per
// idTokenSuppressionSweepInterval, which keeps the map bounded by the rows
// suppressed within roughly the last interval plus those still live.
func (s *InProcessService) suppressIDTokenRefresh(
	key, idToken string, accessExpiresAt, sessionExpiresAt time.Time,
) {
	now := time.Now()
	until := accessExpiresAt
	if until.IsZero() {
		until = sessionExpiresAt
	}
	if until.IsZero() {
		until = now.Add(defaultIDTokenSuppression)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.lastSuppressionSweep) >= idTokenSuppressionSweepInterval {
		for k, v := range s.idTokenSuppressions {
			if !now.Before(v.until) {
				delete(s.idTokenSuppressions, k)
			}
		}
		s.lastSuppressionSweep = now
	}
	s.idTokenSuppressions[key] = idTokenSuppression{idToken: idToken, until: until}
}

// idTokenRefreshSuppressed reports whether the ID-token trigger is suppressed
// for idToken on the row identified by key at now. An entry found expired is
// dropped.
func (s *InProcessService) idTokenRefreshSuppressed(key, idToken string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sup, ok := s.idTokenSuppressions[key]
	if !ok {
		return false
	}
	if !now.Before(sup.until) {
		delete(s.idTokenSuppressions, key)
		return false
	}
	return sup.idToken == idToken
}

// suppressionKeyFor returns the suppression map key for a provider's row in a
// session: the storage row ID when it resolves, so every path that reads one
// row shares one entry. Under user-keyed storage the per-user read (session
// ID "user:"+userID) and the session-level reads (the tsid) address the same
// row, and keying by session ID would let a switch between them miss the
// suppression and refresh again. When no row ID resolves (for example, no
// canonical user on ctx under user-keyed storage), it falls back to
// suppressionKey. The two key shapes cannot collide: a row ID key starts with
// "row" and a NUL, a fallback key with "session" and a NUL.
func (s *InProcessService) suppressionKeyFor(ctx context.Context, sessionID, providerName string) string {
	rowID, err := s.storage.ResolveUpstreamTokenRowID(ctx, sessionID, providerName)
	if err != nil || rowID == "" {
		return suppressionKey(sessionID, providerName)
	}
	return "row\x00" + string(rowID)
}

// suppressionKey is the fallback suppression map key, by session and provider.
func suppressionKey(sessionID, providerName string) string {
	return "session\x00" + sessionID + "\x00" + providerName
}

// idTokenExpired reports whether idToken carries an `exp` claim that has
// passed, allowing idTokenExpirySkew. The token's signature is not verified:
// it was validated when stored, and only its expiry is read here. An empty or
// unparsable token, or one without `exp`, is not treated as expired, so the
// trigger never fires for a row it cannot reason about.
func idTokenExpired(idToken string, now time.Time) bool {
	if idToken == "" {
		return false
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(idToken, jwt.MapClaims{})
	if err != nil {
		return false
	}
	exp, err := parsed.Claims.GetExpirationTime()
	if err != nil || exp == nil {
		return false
	}
	return !now.Add(idTokenExpirySkew).Before(exp.Time)
}

// needsAuthorization wraps ErrNeedsAuthorization together with its causes, so
// the message reads as one line and every cause stays reachable.
func needsAuthorization(causes ...error) error {
	err := ErrNeedsAuthorization
	for _, cause := range causes {
		err = fmt.Errorf("%w: %w", err, cause)
	}
	return err
}

// userRowKey is the session-ID argument the per-user read passes to storage and
// the refresher. User-keyed storage ignores it. It is unique per user, so the
// ID-token suppression map never shares an entry between users when the row ID
// cannot be resolved and suppressionKeyFor falls back to it.
func userRowKey(userID string) string {
	return "user:" + userID
}

// usableUserCredential applies the per-user read's empty access token rule to
// a refreshed credential, the same classification as for a stored row that
// holds no access token.
func usableUserCredential(cred *UpstreamCredential, err error) (*UpstreamCredential, error) {
	if err != nil {
		return nil, err
	}
	if cred.AccessToken == "" {
		return nil, needsAuthorization(ErrSessionNotFound)
	}
	return cred, nil
}

// refreshFailureReason names the category of a refresh failure for logs. Error
// text can carry provider-supplied detail (an OAuth error_description), so
// callers log this instead of the error.
func refreshFailureReason(err error) string {
	switch {
	case errors.Is(err, upstream.ErrGrantRejected), oautherr.IsRejectedRefreshGrant(err):
		return "grant_rejected"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "network_or_timeout"
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return "network_or_timeout"
	}
	if _, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		return "token_endpoint_error"
	}
	return "refresh_failed"
}

// classifyUserReadError maps a storage read failure to the per-user read's
// errors.
func classifyUserReadError(err error) error {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return needsAuthorization(ErrSessionNotFound, err)
	case errors.Is(err, storage.ErrInvalidBinding):
		return fmt.Errorf("%w: %w", ErrInvalidBinding, err)
	}
	return fmt.Errorf("failed to get upstream tokens: %w", err)
}

// classifyUserRefreshError maps a refresh failure to the per-user read's
// errors. Only a failure that more retries cannot fix needs authorization: a
// row gone (including one the refresher deleted, or cleared of its refresh token,
// after it could not save a rotated refresh token, which carries storage.ErrNotFound), an expired row
// with nothing to refresh it with, or a refresh the provider rejected with
// invalid_grant. A 5xx, a timeout or a lost refresh race is returned as it
// came.
func classifyUserRefreshError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrInvalidBinding), errors.Is(err, ErrInvalidBinding):
		return fmt.Errorf("%w: %w", ErrInvalidBinding, err)
	case errors.Is(err, storage.ErrNotFound),
		errors.Is(err, ErrNoRefreshToken),
		errors.Is(err, upstream.ErrGrantRejected):
		return needsAuthorization(err)
	}
	return err
}
