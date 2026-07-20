// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

const (
	// tokenRefreshInitialRetryInterval is the default starting interval for
	// exponential backoff when a token refresh fails during background monitoring.
	// Override with TOOLHIVE_TOKEN_REFRESH_INITIAL_RETRY_INTERVAL (e.g. "10s", "1m").
	tokenRefreshInitialRetryInterval = 10 * time.Second
	// tokenRefreshMaxRetryInterval is the default cap on the exponential growth
	// of the retry interval.
	// Override with TOOLHIVE_TOKEN_REFRESH_MAX_RETRY_INTERVAL (e.g. "2m", "10m").
	tokenRefreshMaxRetryInterval = 2 * time.Minute
	// tokenRefreshMaxTries is the default maximum number of retry attempts.
	// Override with TOOLHIVE_TOKEN_REFRESH_MAX_TRIES (e.g. "10").
	tokenRefreshMaxTries = 5
	// tokenRefreshMaxElapsedTime is the default maximum elapsed time for all retry attempts.
	// Override with TOOLHIVE_TOKEN_REFRESH_MAX_ELAPSED_TIME (e.g. "10m").
	tokenRefreshMaxElapsedTime = 5 * time.Minute
	// authRetryingTickInterval is the default cadence between background refresh
	// attempts once the short retry has exhausted on a transient error. The
	// short retry inside transientRefresher.retry already performs exponential
	// backoff; this cross-tick layer uses a fixed cadence to avoid layering
	// exponential on top of exponential and to give operators a predictable
	// retry signature when investigating long outages.
	// Override with TOOLHIVE_TOKEN_AUTH_RETRYING_TICK_INTERVAL (e.g. "5m", "30m").
	authRetryingTickInterval = 10 * time.Minute
	// authRetryingMaxElapsed is the ceiling on how long the monitor will stay
	// in the AuthRetrying transient-failure window before giving up and
	// marking the workload unauthenticated.
	// Override with TOOLHIVE_TOKEN_AUTH_RETRYING_MAX_ELAPSED (e.g. "12h", "48h").
	authRetryingMaxElapsed = 24 * time.Hour
)

const (
	// #nosec G101 — not credentials, just initial retry interval
	tokenRefreshInitialRetryIntervalEnv = "TOOLHIVE_TOKEN_REFRESH_INITIAL_RETRY_INTERVAL"
	// #nosec G101 — not credentials, just max retry interval
	tokenRefreshMaxRetryIntervalEnv = "TOOLHIVE_TOKEN_REFRESH_MAX_RETRY_INTERVAL"
	// #nosec G101 — not credentials, just max elapsed time
	tokenRefreshMaxElapsedTimeEnv = "TOOLHIVE_TOKEN_REFRESH_MAX_ELAPSED_TIME"
	// #nosec G101 — not credentials, just max tries
	tokenRefreshMaxTriesEnv = "TOOLHIVE_TOKEN_REFRESH_MAX_TRIES"
	// #nosec G101 — not credentials, just retry tick interval
	authRetryingTickIntervalEnv = "TOOLHIVE_TOKEN_AUTH_RETRYING_TICK_INTERVAL"
	// #nosec G101 — not credentials, just max elapsed time
	authRetryingMaxElapsedEnv = "TOOLHIVE_TOKEN_AUTH_RETRYING_MAX_ELAPSED"
)

// resolveTokenRefreshInitialRetryInterval returns the initial retry interval for
// token refresh backoff, reading from TOOLHIVE_TOKEN_REFRESH_INITIAL_RETRY_INTERVAL
// if set, otherwise returning the default.
func resolveTokenRefreshInitialRetryInterval() time.Duration {
	return resolveDurationEnv(
		tokenRefreshInitialRetryIntervalEnv,
		tokenRefreshInitialRetryInterval,
	)
}

// resolveTokenRefreshMaxRetryInterval returns the max retry interval for token
// refresh backoff, reading from TOOLHIVE_TOKEN_REFRESH_MAX_RETRY_INTERVAL if
// set, otherwise returning the default.
func resolveTokenRefreshMaxRetryInterval() time.Duration {
	return resolveDurationEnv(
		tokenRefreshMaxRetryIntervalEnv,
		tokenRefreshMaxRetryInterval,
	)
}

// resolveTokenRefreshMaxTries returns the maximum number of retry attempts for
// token refresh backoff, reading from TOOLHIVE_TOKEN_REFRESH_MAX_TRIES if
// set, otherwise returning the default.
func resolveTokenRefreshMaxTries() uint {
	v := os.Getenv(tokenRefreshMaxTriesEnv)
	if v == "" {
		return uint(tokenRefreshMaxTries)
	}
	n, err := strconv.ParseUint(v, 10, strconv.IntSize)
	if err != nil {
		return uint(tokenRefreshMaxTries)
	}
	return uint(n)
}

// resolveTokenRefreshMaxElapsedTime returns the maximum elapsed time for all retry attempts for
// token refresh backoff, reading from TOOLHIVE_TOKEN_REFRESH_MAX_ELAPSED_TIME if
// set, otherwise returning the default.
func resolveTokenRefreshMaxElapsedTime() time.Duration {
	return resolveDurationEnv(
		tokenRefreshMaxElapsedTimeEnv,
		tokenRefreshMaxElapsedTime,
	)
}

// resolveAuthRetryingTickInterval returns the cadence between monitor refresh
// attempts during the AuthRetrying transient-failure window, reading from
// TOOLHIVE_TOKEN_AUTH_RETRYING_TICK_INTERVAL if set, otherwise the default.
func resolveAuthRetryingTickInterval() time.Duration {
	return resolveDurationEnv(authRetryingTickIntervalEnv, authRetryingTickInterval)
}

// resolveAuthRetryingMaxElapsed returns the ceiling on how long the monitor
// will stay in AuthRetrying before giving up, reading from
// TOOLHIVE_TOKEN_AUTH_RETRYING_MAX_ELAPSED if set, otherwise the default.
func resolveAuthRetryingMaxElapsed() time.Duration {
	return resolveDurationEnv(authRetryingMaxElapsedEnv, authRetryingMaxElapsed)
}

// resolveDurationEnv reads a duration from the given environment variable.
// Returns defaultVal if the variable is unset or its value is not a valid
// positive duration.
func resolveDurationEnv(envVar string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(envVar)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		slog.Warn("invalid duration env var, using default",
			"env_var", envVar, "value", v, "default", defaultVal)
		return defaultVal
	}
	slog.Debug("using custom token refresh interval", "env_var", envVar, "value", d)
	return d
}

// StatusUpdater is an interface for updating workload authentication status.
// This abstraction allows the monitored token source to work with any status management system
// without creating import cycles.
type StatusUpdater interface {
	SetWorkloadStatus(ctx context.Context, workloadName string, status runtime.WorkloadStatus, reason string) error
}

// transientRefresher deduplicates concurrent token fetches during transient
// network failures and retries with exponential backoff. It is owned by
// MonitoredTokenSource and can be tested in isolation.
type transientRefresher struct {
	group    singleflight.Group
	source   oauth2.TokenSource
	workload string

	// upstream identifies the upstream authorization server that issued the
	// token, and clientID is the OAuth 2.0 client_id used by this workload.
	// Both are optional and are only surfaced in structured logs (notably the
	// DCR/CIMD remediation warning emitted from MonitoredTokenSource on the
	// transition to Unauthenticated). Empty strings are acceptable and are
	// omitted from log output by the slog handler.
	upstream string
	clientID string

	// newBackOff constructs the exponential backoff applied during transient-error
	// retries. Always non-nil; set by the constructor.
	newBackOff func() backoff.BackOff
}

// Refresh deduplicates concurrent callers via singleflight and retries the
// underlying token source with exponential backoff until the context is
// cancelled or a non-transient error is returned.
func (r *transientRefresher) Refresh(ctx context.Context, origErr error) (*oauth2.Token, error) {
	v, err, _ := r.group.Do("token-refresh", func() (interface{}, error) {
		return r.retry(ctx, origErr)
	})
	if err != nil {
		return nil, err
	}
	return v.(*oauth2.Token), nil
}

func newTransientRefresher(
	source oauth2.TokenSource,
	workload, upstream, clientID string,
	newBackOff func() backoff.BackOff,
) *transientRefresher {
	return &transientRefresher{
		source:     source,
		workload:   workload,
		upstream:   upstream,
		clientID:   clientID,
		newBackOff: newBackOff,
	}
}

func defaultBackOff() backoff.BackOff {
	eb := backoff.NewExponentialBackOff()
	eb.InitialInterval = resolveTokenRefreshInitialRetryInterval()
	eb.MaxInterval = resolveTokenRefreshMaxRetryInterval()
	eb.Reset()
	return eb
}

func (r *transientRefresher) retry(ctx context.Context, origErr error) (*oauth2.Token, error) {
	slog.Warn("token refresh failed due to transient network error, retrying with backoff",
		"workload", r.workload,
		"error", origErr,
	)

	return backoff.Retry(ctx, func() (*oauth2.Token, error) {
		t, tokenErr := r.source.Token()
		if tokenErr == nil {
			return t, nil
		}
		if !isTransientNetworkError(tokenErr) {
			return nil, backoff.Permanent(tokenErr)
		}
		return nil, tokenErr
	},
		backoff.WithBackOff(r.newBackOff()),
		backoff.WithNotify(func(retryErr error, d time.Duration) {
			slog.Warn("token refresh retry failed",
				"workload", r.workload,
				"retry_in", d,
				"error", retryErr,
			)
		}),
		backoff.WithMaxTries(resolveTokenRefreshMaxTries()),
		backoff.WithMaxElapsedTime(resolveTokenRefreshMaxElapsedTime()),
	)
}

// MonitoredTokenSource is a wrapper around an oauth2.TokenSource that monitors
// authentication failures and surfaces them through workload status transitions.
// It provides both per-request token retrieval and background monitoring.
//
// Failure handling has two layers:
//
//   - Short retry (inside transientRefresher.retry): exponential backoff up to
//     a ~5min total window. Retries transient failures (DNS errors, dropped
//     connections, OAuth server 5xx/429, WAF 4xx-with-HTML) without changing
//     workload status. Failures that resolve within the window never leave
//     Running; failures that outlast the window fall through to AuthRetrying.
//
//   - AuthRetrying ceiling (this struct): after the short retry exhausts on a
//     still-transient error, the workload transitions to AuthRetrying and the
//     monitor keeps running on a longer cadence (default 10min). On the next
//     successful refresh, the workload returns to Running. After a ceiling
//     (default 24h) the workload is finally marked Unauthenticated and the
//     monitor stops.
//
// Hot callers — request-path Token() calls, e.g. from the token injection
// middleware serving a live MCP request — fast-fail with the cached error
// during AuthRetrying so they return 503+Retry-After immediately rather
// than blocking on a singleflight retry that would exhaust again on every
// call.
//
// Hot callers and the background monitor can both drive state transitions
// independently (the monitor via its tick, hot callers via short-retry
// exhaustion in Token). When the upstream alternates between transient
// failures and brief recoveries, the workload status oscillates with it.
type MonitoredTokenSource struct {
	tokenSource    oauth2.TokenSource
	workloadName   string
	upstream       string
	clientID       string
	statusUpdater  StatusUpdater
	monitoringCtx  context.Context
	stopMonitoring chan struct{}
	stopOnce       sync.Once
	// dcrWarnOnce gates the DCR/CIMD remediation Warn so it fires at most
	// once per workload lifetime. Kept separate from stopOnce so the gate
	// is independent of which code path first closes stopMonitoring: a
	// zero-expiry exit in onTick may close the channel without ever
	// transitioning to Unauthenticated, and a later markAsUnauthenticated
	// must still be able to emit the Warn for a permanent 4xx.
	dcrWarnOnce sync.Once
	refresher   *transientRefresher

	// stopped is closed when monitorLoop exits, regardless of the reason.
	stopped chan struct{}

	timer *time.Timer

	// AuthRetrying state, guarded by mu. A non-zero transientStartedAt
	// means the workload is in the AuthRetrying window (short retry
	// exhausted, ceiling not yet reached). lastTransientErr is the error
	// returned to hot callers via Token() during the window so they fail
	// fast instead of joining a singleflight that would block for the
	// full short-retry duration on every tick. Written by the monitor
	// goroutine and by Token() callers who reach the short-retry
	// exhaustion branch; read by Token() callers during the fast-fail
	// check. mu is held only across pure field reads/writes — never
	// across a statusUpdater call, which does I/O.
	mu                 sync.Mutex
	transientStartedAt time.Time
	lastTransientErr   error
}

// NewMonitoredTokenSource creates a new MonitoredTokenSource that wraps the provided
// oauth2.TokenSource and monitors it for authentication failures.
//
// upstream and clientID annotate structured logs emitted by the token source,
// most importantly the DCR/CIMD remediation warning fired on the transition
// to Unauthenticated when the token endpoint returns a permanent 4xx (which
// frequently indicates stale cached credentials). Pass empty strings when
// the workload does not use DCR/CIMD or the upstream issuer is unknown; the
// remediation log will be suppressed when clientID is empty since its
// operator-correlation field would be blank.
//
// The fields are fixed at construction time rather than exposed via a setter
// so there is no data race between a late writer and the readers in Token()
// / transientRefresher.retry() — both of which may run on the background
// monitor goroutine started by StartBackgroundMonitoring.
func NewMonitoredTokenSource(
	ctx context.Context,
	tokenSource oauth2.TokenSource,
	workloadName string,
	upstream string,
	clientID string,
	statusUpdater StatusUpdater,
) *MonitoredTokenSource {
	return newMonitoredTokenSourceWithBackOff(ctx, tokenSource, workloadName, upstream, clientID, statusUpdater, defaultBackOff)
}

func newMonitoredTokenSourceWithBackOff(
	ctx context.Context,
	tokenSource oauth2.TokenSource,
	workloadName string,
	upstream string,
	clientID string,
	statusUpdater StatusUpdater,
	newBackOff func() backoff.BackOff,
) *MonitoredTokenSource {
	return &MonitoredTokenSource{
		tokenSource:    tokenSource,
		workloadName:   workloadName,
		upstream:       upstream,
		clientID:       clientID,
		statusUpdater:  statusUpdater,
		monitoringCtx:  ctx,
		stopMonitoring: make(chan struct{}),
		stopped:        make(chan struct{}),
		refresher:      newTransientRefresher(tokenSource, workloadName, upstream, clientID, newBackOff),
	}
}

// Stopped returns a channel that is closed when background monitoring has stopped,
// regardless of the reason (context cancellation, auth failure, or clean shutdown).
func (mts *MonitoredTokenSource) Stopped() <-chan struct{} {
	return mts.stopped
}

// Token retrieves a token. On a non-transient error, it marks the workload
// unauthenticated and returns immediately. On a transient error, behavior
// depends on monitor state — see the paragraphs below. Context cancellation
// (workload removal) stops any in-flight retry without marking the workload
// unauthenticated. See isTransientNetworkError for the classification rule.
//
// When the monitor is in the AuthRetrying transient-failure window
// (short retry exhausted, ceiling not yet reached), Token() fast-fails
// with the cached error rather than joining a singleflight retry that
// would hang for the full short-retry duration on every call. Hot
// callers see 503+Retry-After until the next monitor tick observes
// upstream recovery and clears the state — only the monitor's onTick
// (which calls the raw token source directly) can do that.
//
// If a hot caller's own short retry exhausts on a still-transient error,
// the workload transitions to AuthRetrying and the monitor stays alive —
// subsequent hot callers fast-fail until the next monitor tick clears the
// state or extends it. If the monitor has already stopped (a prior
// permanent error closed it), enterAuthRetrying is a no-op; the hot
// caller returns the error without changing workload status.
//
// During the short retry on a transient failure, concurrent callers
// joining at the same time are deduplicated via singleflight so that
// only one retry loop runs at a time. Callers that arrive AFTER the
// leader's singleflight call has returned start their own retry — they
// are not deduplicated against past calls. This is why hot-caller-
// driven AuthRetrying entry (after the retry exhausts) is load-bearing:
// without it, sequential hot callers would each pay the full short-
// retry duration against a broken endpoint.
func (mts *MonitoredTokenSource) Token() (*oauth2.Token, error) {
	if err := mts.fastFailIfAuthRetrying(); err != nil {
		return nil, err
	}

	tok, err := mts.tokenSource.Token()
	if err == nil {
		return tok, nil
	}

	if !isTransientNetworkError(err) {
		mts.markAsUnauthenticated(
			fmt.Sprintf("Token retrieval failed: %v", err),
			isPermanentTokenEndpointError(err),
		)
		return nil, err
	}

	// If the monitor has already stopped (prior permanent error or 24h
	// ceiling), do not enter the short retry. The workload is terminal;
	// making hot callers each pay the full short-retry duration against
	// a known-broken endpoint is exactly the pathology AuthRetrying was
	// introduced to avoid.
	select {
	case <-mts.stopMonitoring:
		return nil, err
	default:
	}

	// Transient network error — funnel all concurrent callers through a
	// single retry loop so we don't hammer the token endpoint.
	tok, err = mts.refresher.Refresh(mts.monitoringCtx, err)
	if err == nil {
		return tok, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if !isTransientNetworkError(err) {
		mts.markAsUnauthenticated(
			fmt.Sprintf("Token refresh failed after retries: %v", err),
			isPermanentTokenEndpointError(err),
		)
		return nil, err
	}
	// Short retry exhausted on a still-transient error → enter AuthRetrying
	// and keep the monitor alive. Subsequent hot callers fast-fail; the
	// next monitor tick will decide whether to recover or extend.
	mts.enterAuthRetrying(err)
	return nil, err
}

// StartBackgroundMonitoring starts the background monitoring goroutine that checks
// token validity at expiry time and marks the workload as unauthenticated on the failure.
func (mts *MonitoredTokenSource) StartBackgroundMonitoring() {
	if mts.timer == nil {
		mts.timer = time.NewTimer(time.Millisecond) // kick immediately
	}
	go mts.monitorLoop()
}

func (mts *MonitoredTokenSource) monitorLoop() {
	defer close(mts.stopped)
	for {
		select {
		case <-mts.monitoringCtx.Done():
			mts.stopTimer()
			return
		case <-mts.stopMonitoring:
			mts.stopTimer()
			return
		case <-mts.timer.C:
			shouldStop, next := mts.onTick()
			if shouldStop {
				mts.stopTimer()
				return
			}
			mts.resetTimer(next)
		}
	}
}

func (mts *MonitoredTokenSource) stopTimer() {
	if mts.timer != nil && !mts.timer.Stop() {
		select {
		case <-mts.timer.C:
		default:
		}
	}
}

func (mts *MonitoredTokenSource) resetTimer(d time.Duration) {
	mts.stopTimer()
	mts.timer.Reset(d)
}

// onTick performs a token refresh attempt on the background monitor goroutine
// and returns whether to stop monitoring and the next tick delay.
//
// State transitions handled here:
//
//   - Success: clear any AuthRetrying state (→ Running) and schedule the
//     next tick at the new token's expiry.
//   - Permanent error: mark Unauthenticated immediately and stop monitoring
//     (existing semantics).
//   - Transient error while in AuthRetrying: check the ceiling. If exceeded,
//     mark Unauthenticated. Otherwise refresh the cached error and reschedule
//     at the AuthRetrying cadence.
//   - Transient error while NOT in AuthRetrying: run the existing short retry
//     once (singleflight + exponential backoff). If it succeeds, return to
//     normal expiry-based scheduling. If it exhausts on a still-transient
//     error, enter AuthRetrying.
func (mts *MonitoredTokenSource) onTick() (bool, time.Duration) {
	// Call the raw token source directly — NOT Token() — so the monitor
	// bypasses the AuthRetrying fast-fail and the singleflight retry. The
	// monitor owns those state transitions itself.
	tok, err := mts.tokenSource.Token()

	if err == nil {
		mts.exitAuthRetrying()
		if tok == nil || tok.Expiry.IsZero() {
			// Token has no expiry, so there's nothing for the monitor to
			// schedule against — exit cleanly. Close stopMonitoring so the
			// Token() hot path's stopMonitoring gate sees the monitor is
			// gone: without this, a future transient error would burn the
			// full short-retry window before enterAuthRetrying's gate
			// (under mu) finally aborted, leaving the workload in
			// AuthRetrying with no monitor to drive the ceiling. We do
			// NOT emit Unauthenticated here — the current token is valid.
			mts.stopMonitoringOnce()
			return true, 0
		}
		return false, waitUntilExpiry(tok.Expiry)
	}

	if !isTransientNetworkError(err) {
		mts.markAsUnauthenticated(
			fmt.Sprintf("Token retrieval failed: %v", err),
			isPermanentTokenEndpointError(err),
		)
		return true, 0
	}

	if mts.inAuthRetrying() {
		if mts.ceilingExceeded() {
			mts.markAsUnauthenticated(
				fmt.Sprintf("Token refresh failed transiently for over %s: %v",
					resolveAuthRetryingMaxElapsed(), err),
				false, // transient by definition → DCR Warn correctly silent
			)
			return true, 0
		}
		mts.enterAuthRetrying(err)
		return false, resolveAuthRetryingTickInterval()
	}

	// First transient failure on this tick: run the existing short retry once
	// so we benefit from singleflight + exponential backoff for brief blips.
	tok, err = mts.refresher.Refresh(mts.monitoringCtx, err)
	if err == nil {
		// A hot caller may have entered AuthRetrying between this tick's
		// inAuthRetrying() check and Refresh returning; clear that state
		// now so subsequent hot Token() calls don't keep fast-failing
		// against the cached transient error until the next monitor tick
		// reaches the inAuthRetrying branch. No-op when not in
		// AuthRetrying.
		mts.exitAuthRetrying()
		if tok == nil || tok.Expiry.IsZero() {
			mts.stopMonitoringOnce()
			return true, 0
		}
		return false, waitUntilExpiry(tok.Expiry)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true, 0
	}
	if !isTransientNetworkError(err) {
		mts.markAsUnauthenticated(
			fmt.Sprintf("Token refresh failed after retries: %v", err),
			isPermanentTokenEndpointError(err),
		)
		return true, 0
	}

	// Short retry exhausted on a still-transient error → enter AuthRetrying.
	mts.enterAuthRetrying(err)
	return false, resolveAuthRetryingTickInterval()
}

// waitUntilExpiry returns the duration until expiry, clamped to a minimum
// of one second so the monitor doesn't spin on a stale or near-past expiry.
func waitUntilExpiry(expiry time.Time) time.Duration {
	wait := time.Until(expiry)
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}

// isTransientNetworkError reports whether err represents a transient
// condition that is likely to resolve on its own. The categories are:
//
//   - Network-level failures: DNS lookup errors, TCP transport errors,
//     timeouts.
//   - OAuth token-endpoint responses classified as transient by
//     isTransientRetrieveError.
//   - Unparsable token responses on a 2xx status (typically an HTML page
//     from a load balancer or CDN).
//
// All other errors return false, causing the workload to be marked
// unauthenticated. TLS failures (certificate verification, handshake
// failure) are intentionally non-transient even though they surface
// through net.OpError like transport-level errors.
//
// The function is side-effect free; callers that want to emit a DCR
// remediation hint on a permanent 4xx must do so themselves at the
// state-transition boundary using isPermanentTokenEndpointError to
// classify, so a tight Token() loop does not spam the same record.
func isTransientNetworkError(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	if retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		return isTransientRetrieveError(retrieveErr)
	}

	// Non-JSON responses from the OAuth server (e.g. load balancer HTML pages).
	// The oauth2 library returns a plain error (not *RetrieveError) when the
	// HTTP status is 2xx but the body cannot be parsed as JSON.
	if isOAuthParseError(err) {
		return true
	}

	// DNS lookup failures — covers VPN-disconnect scenarios where the corporate DNS
	// resolver is unreachable.
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return true
	}

	// *net.OpError covers both transport-level errors (connection refused, network
	// unreachable) AND TLS errors (certificate invalid, handshake failure). Only the
	// former are transient; TLS errors do not wrap syscall errors, so we use that
	// to distinguish them.
	if opErr, ok := errors.AsType[*net.OpError](err); ok {
		_, isSyscall := errors.AsType[*os.SyscallError](opErr)
		_, isErrno := errors.AsType[syscall.Errno](opErr)
		return isSyscall || isErrno
	}

	// Generic net.Error timeout (catches any remaining net.Error implementations).
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return true
	}

	return false
}

// isPermanentTokenEndpointError reports whether err is an *oauth2.RetrieveError
// whose response carries a structured RFC 6749 'error' code, implying the
// OAuth server itself rendered a verdict on the cached credentials
// (invalid_grant, invalid_client, etc.). Used at state-transition
// boundaries to decide whether to emit a DCR/CIMD remediation hint
// alongside the unauthentication.
//
// This is the strict inverse of isTransientRetrieveError on the
// *oauth2.RetrieveError branch: a response is "permanent" iff the
// classifier would NOT call it transient. Concretely, the Warn fires
// only when ErrorCode is populated. 4xx responses without an OAuth
// error code (HTML pages from a WAF, CDN, or reverse proxy) — like
// 5xx, 429, 408, and nil-Response shapes — are treated as
// non-permanent because we have no OAuth-protocol verdict to act on.
// Recommending the user delete cached credentials based on a non-
// spec-compliant response would frequently mislead operators whose
// real problem is upstream of the OAuth server.
func isPermanentTokenEndpointError(err error) bool {
	retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err)
	if !ok || retrieveErr.Response == nil {
		return false
	}
	return !isTransientRetrieveError(retrieveErr)
}

// isTransientRetrieveError reports whether an *oauth2.RetrieveError should
// be treated as transient. The classification rules are:
//
//   - nil Response: non-transient. There is no signal to act on, so we fall
//     through to the unauthenticated path rather than retry blindly.
//   - 5xx status: transient (server-side issue, likely to resolve).
//   - 429 Too Many Requests: transient regardless of body (HTTP standard).
//   - 4xx with an empty ErrorCode: transient. The oauth2 library populates
//     ErrorCode from the RFC 6749 'error' field in a JSON response body. An
//     empty ErrorCode means the response was not a parseable OAuth error —
//     typically an HTML page from a WAF, CDN, or reverse proxy that
//     intercepted the request before it reached the OAuth server. These
//     infrastructure errors (Cloudflare blocks, residential-IP allowlist
//     misses, transient bad-config deploys) commonly resolve on their own.
//   - 4xx with a populated ErrorCode: permanent. The OAuth server returned
//     a structured error code (invalid_grant, invalid_client, etc.) telling
//     us specifically what's wrong; retrying won't help.
func isTransientRetrieveError(retrieveErr *oauth2.RetrieveError) bool {
	if retrieveErr.Response == nil {
		return false
	}
	statusCode := retrieveErr.Response.StatusCode

	if statusCode >= 500 {
		slog.Debug("treating OAuth server error as transient",
			"status_code", statusCode,
		)
		return true
	}

	if statusCode == http.StatusTooManyRequests {
		slog.Debug("treating OAuth rate-limit response as transient",
			"status_code", statusCode,
			"error_code", retrieveErr.ErrorCode,
		)
		return true
	}

	if retrieveErr.ErrorCode == "" {
		slog.Debug("treating OAuth 4xx without error code as transient",
			"status_code", statusCode,
		)
		return true
	}

	return false
}

// isOAuthParseError detects errors from the oauth2 library that indicate the
// token endpoint returned an unparsable response body on a 2xx status. This
// typically happens when a load balancer, CDN, or reverse proxy intercepts the
// request and returns its own HTML page instead of the expected JSON token
// response. The oauth2 library uses fmt.Errorf with %v (not %w) for these
// errors, so string matching is the only reliable detection method.
func isOAuthParseError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "oauth2: cannot parse json") ||
		strings.Contains(msg, "oauth2: cannot parse response")
}

// stopMonitoringOnce idempotently closes the stopMonitoring channel. It
// does NOT emit any workload status transition; the caller is responsible
// for that (or for explicitly skipping it, as the zero-expiry exit path
// does). The DCR/CIMD remediation Warn is gated by a separate sync.Once
// (dcrWarnOnce in markAsUnauthenticated) so callers that close the
// channel without transitioning to Unauthenticated do not consume the
// Warn slot.
func (mts *MonitoredTokenSource) stopMonitoringOnce() {
	mts.stopOnce.Do(func() {
		close(mts.stopMonitoring)
	})
}

// markAsUnauthenticated marks the workload as unauthenticated and stops
// background monitoring. If permanent4xx is true and the workload was
// constructed with a non-empty client_id, a one-shot DCR/CIMD remediation
// hint is emitted alongside the stop transition. The Warn is gated on
// dcrWarnOnce so a later call cannot re-emit it after the workload has
// already transitioned to Unauthenticated, and so the Warn remains
// emittable on the first real Unauthenticated transition even if an
// earlier code path (such as the zero-expiry exit in onTick) consumed
// the stopOnce slot. dcrWarnOnce is kept separate from stopOnce for the
// reason captured in the dcrWarnOnce field comment.
//
// Ordering matters: stopMonitoring is closed first so any future
// enterAuthRetrying call sees the gate closed before it acquires the
// field mutex. That alone only narrows the in-memory field race — an
// enterAuthRetrying that already passed the gate and is waiting on mu
// can still set transientStartedAt/lastTransientErr after we clear
// them. The residual race is closed by the defensive re-clear under
// mu below (the load-bearing step), so a hot caller cannot leave
// AuthRetrying state populated after we've written Unauthenticated.
// The Unauthenticated status emit follows the re-clear.
func (mts *MonitoredTokenSource) markAsUnauthenticated(reason string, permanent4xx bool) {
	mts.stopMonitoringOnce()

	mts.mu.Lock()
	mts.transientStartedAt = time.Time{}
	mts.lastTransientErr = nil
	mts.mu.Unlock()

	_ = mts.statusUpdater.SetWorkloadStatus(
		context.Background(),
		mts.workloadName,
		runtime.WorkloadStatusUnauthenticated,
		reason,
	)

	// Emit the DCR/CIMD remediation hint at most once per workload lifetime.
	// A permanent 4xx from the token endpoint commonly indicates the
	// cached client (DCR or CIMD) is no longer recognised — but the same
	// branch fires for revoked consent, disabled accounts, and statically
	// configured clients, so the message has to be honest about the
	// variability. Gating on clientID != "" suppresses the log entirely
	// for workloads where no client_id context is available; the
	// operator-correlation it provides would be empty.
	//
	// Gate the dcrWarnOnce.Do call on permanent4xx && clientID != ""
	// OUTSIDE the lambda: otherwise a non-permanent invocation (e.g. a
	// ceiling-triggered markAsUnauthenticated, permanent4xx=false) would
	// enter Do, early-return, and consume the once-slot — silently
	// suppressing the Warn for a later legitimate permanent 4xx.
	if permanent4xx && mts.clientID != "" {
		mts.dcrWarnOnce.Do(func() {
			//nolint:gosec // G706: client_id is public metadata per RFC 7591.
			slog.Warn(
				"token endpoint returned a permanent error; if this workload uses "+
					"cached DCR or CIMD credentials they may be stale — delete the "+
					"cached credentials and restart to re-register.",
				"workload", mts.workloadName,
				"upstream", mts.upstream,
				"client_id", mts.clientID,
			)
		})
	}
}

// inAuthRetrying reports whether the monitor is currently in the
// AuthRetrying transient-failure window.
func (mts *MonitoredTokenSource) inAuthRetrying() bool {
	mts.mu.Lock()
	defer mts.mu.Unlock()
	return !mts.transientStartedAt.IsZero()
}

// fastFailIfAuthRetrying returns the cached transient error when the
// monitor is in AuthRetrying so hot callers (e.g. the token injection
// middleware) return 503+Retry-After immediately rather than blocking
// on a singleflight retry that will exhaust again.
func (mts *MonitoredTokenSource) fastFailIfAuthRetrying() error {
	mts.mu.Lock()
	defer mts.mu.Unlock()
	if mts.transientStartedAt.IsZero() {
		return nil
	}
	// enterAuthRetrying sets transientStartedAt and lastTransientErr together
	// under mu, so under normal flow lastTransientErr is non-nil here. Guard
	// against a future refactor that breaks the invariant; fmt.Errorf("%w", nil)
	// returns a non-nil error whose formatted message is misleading
	// (%!w(<nil>)).
	if mts.lastTransientErr == nil {
		return fmt.Errorf("auth retrying since %s",
			mts.transientStartedAt.Format(time.RFC3339))
	}
	return fmt.Errorf("auth retrying since %s: %w",
		mts.transientStartedAt.Format(time.RFC3339), mts.lastTransientErr)
}

// ceilingExceeded reports whether the workload has been in AuthRetrying
// for longer than the configured ceiling. Returns false if not in
// AuthRetrying.
func (mts *MonitoredTokenSource) ceilingExceeded() bool {
	mts.mu.Lock()
	defer mts.mu.Unlock()
	if mts.transientStartedAt.IsZero() {
		return false
	}
	return time.Since(mts.transientStartedAt) > resolveAuthRetryingMaxElapsed()
}

// enterAuthRetrying marks the workload as AuthRetrying on the first call
// and refreshes the cached error on subsequent calls. The status
// transition is emitted only on first entry to avoid spamming the
// status file every tick.
//
// The gate check is performed under mts.mu so that markAsUnauthenticated
// (which closes the channel before acquiring mu) cannot interleave to
// re-populate transientStartedAt/lastTransientErr after clearing them.
// A narrower disk-write inversion is still possible (auth_retrying
// written briefly after Unauthenticated); the in-memory state is
// correct, and the disk entry resolves on the next workload restart
// (runner.go resets non-Unauthenticated statuses to Running).
func (mts *MonitoredTokenSource) enterAuthRetrying(err error) {
	mts.mu.Lock()
	select {
	case <-mts.stopMonitoring:
		mts.mu.Unlock()
		return
	default:
	}
	firstEntry := mts.transientStartedAt.IsZero()
	if firstEntry {
		mts.transientStartedAt = time.Now()
	}
	elapsed := time.Since(mts.transientStartedAt)
	mts.lastTransientErr = err
	mts.mu.Unlock()

	if firstEntry {
		slog.Warn("token refresh entering AuthRetrying after short retry exhaustion; will retry on a longer cadence",
			"workload", mts.workloadName,
			"tick_interval", resolveAuthRetryingTickInterval(),
			"max_elapsed", resolveAuthRetryingMaxElapsed(),
			"error", err,
		)
		_ = mts.statusUpdater.SetWorkloadStatus(
			context.Background(),
			mts.workloadName,
			runtime.WorkloadStatusAuthRetrying,
			fmt.Sprintf("Token refresh failing transiently; retrying every %s: %v",
				resolveAuthRetryingTickInterval(), err),
		)
		return
	}
	slog.Warn("token refresh still failing transiently",
		"workload", mts.workloadName,
		"elapsed", elapsed,
		"error", err,
	)
}

// exitAuthRetrying clears the AuthRetrying window and emits the Running
// status transition if the workload was previously in AuthRetrying. No-op
// if not in AuthRetrying.
func (mts *MonitoredTokenSource) exitAuthRetrying() {
	mts.mu.Lock()
	wasInAuthRetrying := !mts.transientStartedAt.IsZero()
	if wasInAuthRetrying {
		mts.transientStartedAt = time.Time{}
		mts.lastTransientErr = nil
	}
	mts.mu.Unlock()

	if !wasInAuthRetrying {
		return
	}
	slog.Info("token refresh recovered; exiting AuthRetrying state",
		"workload", mts.workloadName,
	)
	_ = mts.statusUpdater.SetWorkloadStatus(
		context.Background(),
		mts.workloadName,
		runtime.WorkloadStatusRunning,
		"",
	)
}
