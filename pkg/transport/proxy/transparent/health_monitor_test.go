// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		true, // isRemote
		"sse",
		callback,
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

	proxy := startHealthTestProxy(t, backend.URL, 100*time.Millisecond, 5*time.Second, nil)
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

	proxy := startHealthTestProxy(t, backend.URL, time.Hour, time.Second, nil)

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
// that checks before the MCP server is initialized feed /health but do not count
// toward shutdown, and that counting starts from zero once it is initialized.
func TestTransparentProxy_HealthCheckFailuresNotCountedBeforeInitialization(t *testing.T) {
	t.Parallel()

	var backendHits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)

	tracker, callback := newCallbackTracker()
	proxy := startHealthTestProxy(t, backend.URL, 100*time.Millisecond, 100*time.Millisecond, callback)

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
