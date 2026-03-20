// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/oauth2"

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
	// newRetryBackOff is a factory for the backoff used during transient-error retries.
	// It is nil by default (production path) and overridable in tests for fast execution.
	newRetryBackOff func() backoff.BackOff

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
	}
}

// Stopped returns a channel that is closed when background monitoring has stopped,
// regardless of the reason (context cancellation, auth failure, or clean shutdown).
func (mts *MonitoredTokenSource) Stopped() <-chan struct{} {
	return mts.stopped
}

// Token retrieves a token, retrying with exponential backoff on transient network
// errors (DNS failures, TCP errors). On non-transient errors (OAuth 4xx, TLS failures)
// it marks the workload as unauthenticated and returns immediately. Context cancellation
// (workload removal) stops the retry without marking the workload as unauthenticated.
func (mts *MonitoredTokenSource) Token() (*oauth2.Token, error) {
	tok, err := mts.tokenSource.Token()
	if err == nil {
		return tok, nil
	}

	if !isTransientNetworkError(err) {
		mts.markAsUnauthenticated(fmt.Sprintf("Token retrieval failed: %v", err))
		return nil, err
	}

	// Transient network error (e.g. VPN disconnected, DNS unavailable).
	// Retry with exponential backoff until the network recovers or monitoring stops.
	slog.Warn("token refresh failed due to transient network error, retrying with backoff",
		"workload", mts.workloadName,
		"error", err,
	)

	b := mts.getRetryBackOff()

	tok, retryErr := backoff.Retry(mts.monitoringCtx, func() (*oauth2.Token, error) {
		t, tokenErr := mts.tokenSource.Token()
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
				"workload", mts.workloadName,
				"retry_in", d,
				"error", retryErr,
			)
		}),
		backoff.WithMaxTries(resolveTokenRefreshMaxTries()),
		backoff.WithMaxElapsedTime(resolveTokenRefreshMaxElapsedTime()),
	)

	if retryErr != nil {
		if errors.Is(retryErr, context.Canceled) || errors.Is(retryErr, context.DeadlineExceeded) {
			// Context cancelled (workload removed) — don't mark as unauthenticated.
			return nil, retryErr
		}
		mts.markAsUnauthenticated(fmt.Sprintf("Token refresh failed after retries: %v", retryErr))
		return nil, retryErr
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

// getRetryBackOff returns the backoff to use for transient-error retries.
// Uses mts.newRetryBackOff if set (e.g. in tests); otherwise returns a default
// exponential backoff with no MaxElapsedTime (context cancellation is the stop signal).
func (mts *MonitoredTokenSource) getRetryBackOff() backoff.BackOff {
	if mts.newRetryBackOff != nil {
		return mts.newRetryBackOff()
	}
	eb := backoff.NewExponentialBackOff()
	eb.InitialInterval = resolveTokenRefreshInitialRetryInterval()
	eb.MaxInterval = resolveTokenRefreshMaxRetryInterval()
	// No MaxElapsedTime — context cancellation is the stop signal.
	eb.Reset()
	return eb
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

// isTransientNetworkError reports whether err represents a transient network condition
// (DNS failure, TCP transport error, timeout) that is likely to resolve when the network
// recovers — for example, after a VPN reconnects.
//
// OAuth2 HTTP-level auth failures (invalid_grant, 401, 400) and TLS errors
// (certificate verification, handshake failure) are NOT considered transient and
// return false so the workload is marked unauthenticated immediately.
func isTransientNetworkError(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, new(*oauth2.RetrieveError)) {
		return false
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
