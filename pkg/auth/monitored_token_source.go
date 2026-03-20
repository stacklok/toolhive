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
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

const (
	// tokenRefreshInitialRetryInterval is the default starting interval for
	// exponential backoff when a token refresh fails during background monitoring.
	// Override with TOOLHIVE_TOKEN_REFRESH_INITIAL_RETRY_INTERVAL (e.g. "10s", "1m").
	tokenRefreshInitialRetryInterval = 30 * time.Second
	// tokenRefreshMaxRetryInterval is the default cap on the exponential growth
	// of the retry interval.
	// Override with TOOLHIVE_TOKEN_REFRESH_MAX_RETRY_INTERVAL (e.g. "2m", "10m").
	tokenRefreshMaxRetryInterval = 5 * time.Minute

	// #nosec G101 — these are environment variable names, not credentials
	tokenRefreshInitialRetryIntervalEnv = "TOOLHIVE_TOKEN_REFRESH_INITIAL_RETRY_INTERVAL"
	tokenRefreshMaxRetryIntervalEnv     = "TOOLHIVE_TOKEN_REFRESH_MAX_RETRY_INTERVAL"
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

// Token retrieves a token from the token source and will mark the workload as unauthenticated
// if the token retrieval fails.
func (mts *MonitoredTokenSource) Token() (*oauth2.Token, error) {
	tok, err := mts.tokenSource.Token()
	if err != nil {
		mts.markAsUnauthenticated(fmt.Sprintf("Token retrieval failed: %v", err))
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

// onTick returns (shouldStop bool, nextDelay time.Duration).
//
// On a transient network error (DNS failure, TCP-level error, timeout) it retries
// with exponential backoff (respecting mts.monitoringCtx) before marking the workload
// as unauthenticated. Non-transient errors (OAuth 4xx, invalid_grant, etc.) cause an
// immediate failure. If the context is cancelled while retrying (e.g. the workload is
// removed), monitoring stops cleanly without marking the workload as unauthenticated.
func (mts *MonitoredTokenSource) onTick() (bool, time.Duration) {
	tok, err := mts.tokenSource.Token()
	if err == nil {
		if tok == nil || tok.Expiry.IsZero() {
			return true, 0
		}
		wait := time.Until(tok.Expiry)
		if wait < time.Second {
			wait = time.Second
		}
		return false, wait
	}

	if !isTransientNetworkError(err) {
		mts.markAsUnauthenticated(fmt.Sprintf("No valid token: %v", err))
		return true, 0
	}

	// Transient network error (e.g. VPN disconnected, DNS unavailable).
	// Retry with exponential backoff until the network recovers or monitoring stops.
	slog.Warn("token refresh failed due to transient network error, retrying with backoff",
		"workload", mts.workloadName,
		"error", err,
	)

	b := mts.getRetryBackOff()

	var refreshed *oauth2.Token

	_, retryErr := backoff.Retry(mts.monitoringCtx, func() (*oauth2.Token, error) {
		t, tokenErr := mts.tokenSource.Token()
		if tokenErr == nil {
			refreshed = t
			return t, nil
		}
		if !isTransientNetworkError(tokenErr) {
			// Non-transient error inside retry — stop immediately.
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
	)

	if retryErr != nil {
		if errors.Is(retryErr, context.Canceled) || errors.Is(retryErr, context.DeadlineExceeded) {
			// Monitoring context was cancelled (workload removed) — do not mark as unauthenticated.
			return true, 0
		}
		mts.markAsUnauthenticated(fmt.Sprintf("Token refresh failed after retries: %v", retryErr))
		return true, 0
	}

	// Retry succeeded — schedule next check at the new token's expiry.
	if refreshed == nil || refreshed.Expiry.IsZero() {
		return true, 0
	}
	wait := time.Until(refreshed.Expiry)
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// isTransientNetworkError reports whether err represents a transient network condition
// (DNS failure, TCP-level error, timeout) that is likely to resolve when the network
// recovers — for example, after a VPN reconnects.
//
// OAuth2 HTTP-level auth failures (invalid_grant, 401, 400) are NOT considered
// transient and return false so the workload is marked unauthenticated immediately.
func isTransientNetworkError(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, new(*oauth2.RetrieveError)) {
		return false
	}

	// Transient: DNS lookup failure, TCP-level (connection refused, unreachable), or timeout.
	_, isDNS := errors.AsType[*net.DNSError](err)
	_, isOp := errors.AsType[*net.OpError](err)
	netErr, isNet := errors.AsType[net.Error](err)

	isTransient := isDNS || isOp || (isNet && netErr.Timeout())

	return isTransient
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
