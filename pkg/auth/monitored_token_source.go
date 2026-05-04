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

	// newBackOff is a factory for the backoff used during retries.
	// Nil in production; overridable in tests for fast execution.
	newBackOff func() backoff.BackOff

	// beforeEntry and afterEntry are nil in production. Tests set them to
	// synchronise goroutines so that the singleflight group is fully formed
	// before the leader's retry returns.
	beforeEntry func()
	afterEntry  func()
}

// Refresh deduplicates concurrent callers via singleflight and retries the
// underlying token source with exponential backoff until the context is
// cancelled or a non-transient error is returned.
func (r *transientRefresher) Refresh(ctx context.Context, origErr error) (*oauth2.Token, error) {
	if r.beforeEntry != nil {
		r.beforeEntry()
	}
	v, err, _ := r.group.Do("token-refresh", func() (interface{}, error) {
		if r.afterEntry != nil {
			r.afterEntry()
		}
		return r.retry(ctx, origErr)
	})
	if err != nil {
		return nil, err
	}
	return v.(*oauth2.Token), nil
}

func (r *transientRefresher) retry(ctx context.Context, origErr error) (*oauth2.Token, error) {
	slog.Warn("token refresh failed due to transient network error, retrying with backoff",
		"workload", r.workload,
		"error", origErr,
	)

	b := r.getBackOff()

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
		backoff.WithBackOff(b),
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

func (r *transientRefresher) getBackOff() backoff.BackOff {
	if r.newBackOff != nil {
		return r.newBackOff()
	}
	eb := backoff.NewExponentialBackOff()
	eb.InitialInterval = resolveTokenRefreshInitialRetryInterval()
	eb.MaxInterval = resolveTokenRefreshMaxRetryInterval()
	eb.Reset()
	return eb
}

// MonitoredTokenSource is a wrapper around an oauth2.TokenSource that monitors authentication
// failures and automatically marks workloads as unauthenticated when tokens expire or fail.
// It provides both per-request token retrieval and background monitoring.
//
// When the background monitor encounters a token refresh failure it retries with exponential
// backoff rather than immediately marking the workload as unauthenticated. This handles
// scenarios like overnight VPN disconnects where the token refresh endpoint is temporarily
// unreachable.
type MonitoredTokenSource struct {
	tokenSource    oauth2.TokenSource
	workloadName   string
	statusUpdater  StatusUpdater
	monitoringCtx  context.Context
	stopMonitoring chan struct{}
	stopOnce       sync.Once
	refresher      *transientRefresher

	// stopped is closed when monitorLoop exits, regardless of the reason.
	stopped chan struct{}

	timer *time.Timer
}

// NewMonitoredTokenSource creates a new MonitoredTokenSource that wraps the provided
// oauth2.TokenSource and monitors it for authentication failures.
func NewMonitoredTokenSource(
	ctx context.Context,
	tokenSource oauth2.TokenSource,
	workloadName string,
	statusUpdater StatusUpdater,
) *MonitoredTokenSource {
	return &MonitoredTokenSource{
		tokenSource:    tokenSource,
		workloadName:   workloadName,
		statusUpdater:  statusUpdater,
		monitoringCtx:  ctx,
		stopMonitoring: make(chan struct{}),
		stopped:        make(chan struct{}),
		refresher:      &transientRefresher{source: tokenSource, workload: workloadName},
	}
}

// Stopped returns a channel that is closed when background monitoring has stopped,
// regardless of the reason (context cancellation, auth failure, or clean shutdown).
func (mts *MonitoredTokenSource) Stopped() <-chan struct{} {
	return mts.stopped
}

// Token retrieves a token, retrying with exponential backoff on transient
// errors and marking the workload as unauthenticated on non-transient errors.
// See isTransientNetworkError for the classification rule. Context
// cancellation (workload removal) stops the retry without marking the
// workload as unauthenticated.
//
// Concurrent callers are deduplicated via singleflight so that only one retry
// loop runs at a time during transient failures.
func (mts *MonitoredTokenSource) Token() (*oauth2.Token, error) {
	tok, err := mts.tokenSource.Token()
	if err == nil {
		return tok, nil
	}

	if !isTransientNetworkError(err) {
		mts.markAsUnauthenticated(fmt.Sprintf("Token retrieval failed: %v", err))
		return nil, err
	}

	// Transient network error — funnel all concurrent callers through a
	// single retry loop so we don't hammer the token endpoint.
	tok, err = mts.refresher.Refresh(mts.monitoringCtx, err)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			mts.markAsUnauthenticated(fmt.Sprintf("Token refresh failed after retries: %v", err))
		}
		return nil, err
	}
	return tok, nil
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

// onTick calls Token() to refresh the token and returns the next check delay.
// Token() handles transient error retries and marks the workload as unauthenticated
// on permanent failures.
func (mts *MonitoredTokenSource) onTick() (bool, time.Duration) {
	tok, err := mts.Token()
	if err != nil {
		return true, 0
	}
	if tok == nil || tok.Expiry.IsZero() {
		return true, 0
	}
	wait := time.Until(tok.Expiry)
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// isTransientNetworkError reports whether err represents a transient
// condition that is likely to resolve on its own. The categories are:
//
//   - Network-level failures: DNS lookup errors, TCP transport errors,
//     timeouts.
//   - OAuth token-endpoint responses classified as transient by
//     classifyOAuthRetrieveError.
//   - Unparsable token responses on a 2xx status (typically an HTML page
//     from a load balancer or CDN).
//
// All other errors return false, causing the workload to be marked
// unauthenticated. TLS failures (certificate verification, handshake
// failure) are intentionally non-transient even though they surface
// through net.OpError like transport-level errors.
func isTransientNetworkError(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	if retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		return classifyOAuthRetrieveError(retrieveErr)
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

// classifyOAuthRetrieveError reports whether an *oauth2.RetrieveError should
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
func classifyOAuthRetrieveError(retrieveErr *oauth2.RetrieveError) bool {
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

// markAsUnauthenticated marks the workload as unauthenticated and stops background monitoring.
func (mts *MonitoredTokenSource) markAsUnauthenticated(reason string) {
	_ = mts.statusUpdater.SetWorkloadStatus(
		context.Background(),
		mts.workloadName,
		runtime.WorkloadStatusUnauthenticated,
		reason,
	)
	mts.stopOnce.Do(func() { close(mts.stopMonitoring) })
}
