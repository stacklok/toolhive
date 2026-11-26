// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package healthcheck

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
)

// firstAnswerPinger answers its first ping at once with err and blocks every
// later ping until its context ends or release is closed, so a handler that
// pings inline cannot answer.
type firstAnswerPinger struct {
	err      error
	answered atomic.Bool
	release  chan struct{}
}

func newFirstAnswerPinger(t *testing.T, err error) *firstAnswerPinger {
	t.Helper()
	p := &firstAnswerPinger{err: err, release: make(chan struct{})}
	t.Cleanup(func() { close(p.release) })
	return p
}

func (p *firstAnswerPinger) Ping(ctx context.Context) (time.Duration, error) {
	if p.answered.CompareAndSwap(false, true) {
		return 10 * time.Millisecond, p.err
	}
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return 0, errors.New("blocked ping released")
}

// serveWithin runs ServeHTTP and fails the test if it does not return within
// the timeout.
func serveWithin(t *testing.T, hc *HealthChecker, timeout time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hc.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("ServeHTTP did not answer from the last result in time")
	}
	return w
}

func TestHealthChecker_ServeHTTPWithLastResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		record    bool
		pingErr   error
		status    HealthStatus
		expectMCP bool
		available bool
	}{
		{
			name:   "no result recorded yet reports the proxy without MCP details",
			status: StatusHealthy,
		},
		{
			name:      "answers the recorded healthy result",
			record:    true,
			status:    StatusHealthy,
			expectMCP: true,
			available: true,
		},
		{
			name:      "answers the recorded degraded result",
			record:    true,
			pingErr:   errors.New("backend did not answer"),
			status:    StatusDegraded,
			expectMCP: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pinger := newFirstAnswerPinger(t, tt.pingErr)
			hc := NewHealthChecker("streamable-http", pinger, WithLastResult())

			var recorded *HealthResponse
			if tt.record {
				recorded = hc.CheckHealth(context.Background())
			}

			w := serveWithin(t, hc, 2*time.Second)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			assertNoFingerprint(t, w.Body.Bytes())

			var response HealthResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			assert.Equal(t, tt.status, response.Status)
			assert.Equal(t, "streamable-http", response.Transport)
			if !tt.expectMCP {
				assert.Nil(t, response.MCP)
				assert.False(t, pinger.answered.Load(), "ServeHTTP must not ping the MCP server")
				return
			}
			require.NotNil(t, response.MCP)
			assert.Equal(t, tt.available, response.MCP.Available)
			assert.True(t, recorded.Timestamp.Equal(response.Timestamp), "body must be the recorded result")
			assert.True(t, recorded.MCP.LastChecked.Equal(response.MCP.LastChecked))
			if tt.pingErr != nil {
				assert.Equal(t, tt.pingErr.Error(), response.MCP.Error)
			}
		})
	}
}

func TestHealthChecker_LastResultIsACopy(t *testing.T) {
	t.Parallel()

	hc := NewHealthChecker("sse", &mockMCPPinger{pingDuration: time.Millisecond}, WithLastResult())

	returned := hc.CheckHealth(context.Background())
	returned.Status = StatusUnhealthy
	returned.MCP.Available = false
	*returned.MCP.ResponseTime = 999

	w := serveWithin(t, hc, 2*time.Second)

	assert.Equal(t, http.StatusOK, w.Code)
	var response HealthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Equal(t, StatusHealthy, response.Status)
	require.NotNil(t, response.MCP)
	assert.True(t, response.MCP.Available)
	require.NotNil(t, response.MCP.ResponseTime)
	assert.Equal(t, int64(1), *response.MCP.ResponseTime)
}

func TestHealthChecker_LastResultConcurrentAccess(t *testing.T) {
	t.Parallel()

	hc := NewHealthChecker("sse", &mockMCPPinger{pingDuration: time.Millisecond}, WithLastResult())

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				hc.CheckHealth(context.Background())
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				w := httptest.NewRecorder()
				hc.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
				assert.Equal(t, http.StatusOK, w.Code)
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent health checks")
	}
}
