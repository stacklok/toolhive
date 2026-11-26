// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// startHealthTestProxy starts a proxy in front of serverURL that checks the
// backend at the given interval and ping timeout and stops it when the test ends.
func startHealthTestProxy(
	t *testing.T,
	serverURL string,
	isRemote bool,
	interval time.Duration,
	pingTimeout time.Duration,
	callback types.HealthCheckFailedCallback,
) *TransparentProxy {
	t.Helper()

	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1",
		0,
		serverURL,
		nil,
		nil,
		nil,  // prefixHandlers
		true, // enableHealthCheck
		isRemote,
		"sse",
		callback,
		nil, // onHealthCheckRecovered
		nil,
		"",
		false,
		nil, // middlewares
		withHealthCheckInterval(interval),
		withHealthCheckRetryDelay(50*time.Millisecond),
		withHealthCheckPingTimeout(pingTimeout),
		withHealthCheckFailureThreshold(3),
	)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		_ = proxy.Stop(context.Background())
		cancel()
	})
	return proxy
}

// startRemoteMonitoredProxy starts a remote proxy in front of serverURL with
// fastHealthCheckOptions, followed by opts, and the given health callbacks. It
// stops the proxy when the test ends and returns the cancel func of the context
// the health monitor runs under.
func startRemoteMonitoredProxy(
	t *testing.T,
	serverURL string,
	onFailed types.HealthCheckFailedCallback,
	onRecovered types.HealthCheckRecoveredCallback,
	opts ...Option,
) (*TransparentProxy, context.CancelFunc) {
	t.Helper()

	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, serverURL,
		nil, nil, nil,
		true, // enableHealthCheck
		true, // isRemote
		"sse",
		onFailed,
		onRecovered,
		nil, "", false, nil,
		append(fastHealthCheckOptions(), opts...)...,
	)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		_ = proxy.Stop(context.Background())
		cancel()
	})
	return proxy, cancel
}

// fetchHealth requests /health from the proxy, failing the test unless it answers
// 200 within a second, and returns the decoded body and the time it took.
func fetchHealth(t *testing.T, proxy *TransparentProxy) (*healthcheck.HealthResponse, time.Duration) {
	t.Helper()

	client := &http.Client{Timeout: time.Second}
	start := time.Now()
	resp, err := client.Get("http://" + proxy.ListenerAddr() + "/health")
	elapsed := time.Since(start)
	require.NoError(t, err, "/health must answer within a second")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var health healthcheck.HealthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&health))
	return &health, elapsed
}

// TestTransparentProxy_HealthAnswersWhileBackendIsSlow tests that /health does
// not wait on a backend that takes longer to answer than a probe allows.
func TestTransparentProxy_HealthAnswersWhileBackendIsSlow(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(7 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(backend.Close)

	proxy := startHealthTestProxy(t, backend.URL, true, 100*time.Millisecond, 5*time.Second, nil)
	proxy.setServerInitialized()

	// The monitor's first check waits on the backend for up to the 5s ping
	// timeout; every request meanwhile answers at once.
	for range 3 {
		health, elapsed := fetchHealth(t, proxy)
		assert.Less(t, elapsed, 500*time.Millisecond)
		assert.Equal(t, healthcheck.StatusHealthy, health.Status)
		assert.Nil(t, health.MCP, "no check has completed yet")
		time.Sleep(100 * time.Millisecond)
	}
}

// TestTransparentProxy_HealthReportsFirstCheckBeforeFirstTick tests that the
// monitor checks the backend when it starts, so /health carries MCP details
// without waiting a full interval.
func TestTransparentProxy_HealthReportsFirstCheckBeforeFirstTick(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	proxy := startHealthTestProxy(t, backend.URL, true, time.Hour, time.Second, nil)

	deadline := time.Now().Add(2 * time.Second)
	health, _ := fetchHealth(t, proxy)
	for health.MCP == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		health, _ = fetchHealth(t, proxy)
	}
	require.NotNil(t, health.MCP, "/health must carry the first check's result long before the first tick")
	assert.Equal(t, healthcheck.StatusHealthy, health.Status)
	assert.True(t, health.MCP.Available)
}

// TestTransparentProxy_HealthCheckFailuresNotCountedBeforeInitialization tests
// that, for a local workload, checks before the MCP server is initialized feed
// /health but do not count toward shutdown, and that counting starts from zero
// once it is initialized. Remote workloads count failures from the start; see
// TestTransparentProxy_RemoteServerFailure_HTTP500.
func TestTransparentProxy_HealthCheckFailuresNotCountedBeforeInitialization(t *testing.T) {
	t.Parallel()

	var backendHits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)

	tracker, callback := newCallbackTracker()
	proxy := startHealthTestProxy(t, backend.URL, false, 100*time.Millisecond, 100*time.Millisecond, callback)

	// More failed checks than the threshold of 3, all before initialization.
	require.Eventually(t, func() bool { return backendHits.Load() >= 4 },
		2*time.Second, 10*time.Millisecond, "the monitor must check the backend before initialization")
	assert.False(t, tracker.isInvoked(), "failures before initialization must not trigger shutdown")
	running, _ := proxy.IsRunning()
	assert.True(t, running, "proxy must keep running before initialization")

	health, _ := fetchHealth(t, proxy)
	assert.Equal(t, healthcheck.StatusDegraded, health.Status)
	require.NotNil(t, health.MCP)
	assert.False(t, health.MCP.Available)

	// Three failed checks after initialization take at least two ticks (200ms).
	proxy.setServerInitialized()
	time.Sleep(150 * time.Millisecond)
	assert.False(t, tracker.isInvoked(), "failures before initialization must not carry over")

	callbackInvoked, proxyStopped := waitForShutdown(t, tracker, proxy, 3*time.Second)
	assert.True(t, callbackInvoked, "failures after initialization must trigger shutdown")
	assert.True(t, proxyStopped, "proxy must stop after failures following initialization")
}

// TestTransparentProxy_RemoteRecoveryRetriedUntilHandled tests that a recovery
// callback that fails is called again on each passing check until it reports the
// recovery handled, and not after that.
func TestTransparentProxy_RemoteRecoveryRetriedUntilHandled(t *testing.T) {
	t.Parallel()

	var healthy atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)

	const failedRecoveries = 2
	var failures, recoveries atomic.Int32
	onFailed := func() { failures.Add(1) }
	onRecovered := func() error {
		if recoveries.Add(1) <= failedRecoveries {
			return errors.New("status write failed")
		}
		return nil
	}
	startRemoteMonitoredProxy(t, backend.URL, onFailed, onRecovered)

	require.Eventually(t, func() bool { return failures.Load() >= 1 }, 2*time.Second, 10*time.Millisecond,
		"failure callback should fire after consecutive 500s")
	healthy.Store(true)

	require.Eventually(t, func() bool { return recoveries.Load() == failedRecoveries+1 }, 2*time.Second, 10*time.Millisecond,
		"recovery callback should be called again after each error until it returns nil")
	failuresAtRecovery := failures.Load()

	// Three more passing checks at the 100ms interval.
	assert.Never(t, func() bool { return recoveries.Load() > failedRecoveries+1 }, 350*time.Millisecond, 10*time.Millisecond,
		"recovery callback must not be called once it reported the recovery handled")
	assert.Equal(t, failuresAtRecovery, failures.Load(), "failure callback must not be called while healthy")
}

// TestTransparentProxy_RemoteRecoveryBeforeThresholdNotReported tests that a
// remote server that passes a check after fewer failures than the threshold
// triggers neither health callback.
func TestTransparentProxy_RemoteRecoveryBeforeThresholdNotReported(t *testing.T) {
	t.Parallel()

	// With fastHealthCheckOptions (threshold 3) and one request per check: the check
	// before the first tick, then two ticks each followed by a retry fail, so two
	// failures are counted before the third tick passes.
	const failingHits = 1 + 2*2
	var hits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) <= failingHits {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	var failures, recoveries atomic.Int32
	onFailed := func() { failures.Add(1) }
	onRecovered := func() error {
		recoveries.Add(1)
		return nil
	}
	startRemoteMonitoredProxy(t, backend.URL, onFailed, onRecovered)

	require.Eventually(t, func() bool { return hits.Load() >= failingHits+3 }, 3*time.Second, 10*time.Millisecond,
		"the monitor must keep checking after the server passes")
	assert.Zero(t, failures.Load(), "failures below the threshold must not report the server unhealthy")
	assert.Zero(t, recoveries.Load(), "a recovery before the threshold must not be reported")
}

// TestTransparentProxy_NoHealthCallbacksAfterShutdown tests that a check in flight
// when shutdown begins does not call the failure callback when it fails, so it
// cannot overwrite the status the shutdown path writes.
func TestTransparentProxy_NoHealthCallbacksAfterShutdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		shutdown func(proxy *TransparentProxy, cancel context.CancelFunc)
	}{
		{
			name:     "monitor context cancelled",
			shutdown: func(_ *TransparentProxy, cancel context.CancelFunc) { cancel() },
		},
		{
			name: "proxy stopped",
			shutdown: func(proxy *TransparentProxy, _ context.CancelFunc) {
				_ = proxy.Stop(context.Background())
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var block atomic.Bool
			inFlight := make(chan struct{}, 1)
			releaseCh := make(chan struct{})
			release := sync.OnceFunc(func() { close(releaseCh) })

			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if block.Load() {
					select {
					case inFlight <- struct{}{}:
					default:
					}
					select {
					case <-releaseCh:
					case <-r.Context().Done():
					}
				}
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(backend.Close)
			// Cleanups run last-in first-out, so blocked handlers return before Close waits on them.
			t.Cleanup(release)

			var failures atomic.Int32
			// A ping timeout well beyond the test keeps the blocked check in flight
			// until shutdown or release ends it.
			proxy, cancel := startRemoteMonitoredProxy(t, backend.URL, func() { failures.Add(1) }, nil,
				withHealthCheckPingTimeout(30*time.Second))

			// Reported unhealthy and notified again on a degraded tick.
			require.Eventually(t, func() bool { return failures.Load() >= 2 }, 2*time.Second, 10*time.Millisecond,
				"failure callback should fire on degraded ticks")

			block.Store(true)
			select {
			case <-inFlight:
			case <-time.After(2 * time.Second):
				require.FailNow(t, "timeout waiting for a health check to reach the backend")
			}
			// The monitor is blocked in that check, so no callback can be running.
			before := failures.Load()

			tc.shutdown(proxy, cancel)
			release()

			assert.Never(t, func() bool { return failures.Load() != before }, 500*time.Millisecond, 10*time.Millisecond,
				"failure callback must not be called after shutdown began")
		})
	}
}
