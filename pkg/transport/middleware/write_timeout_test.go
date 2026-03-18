// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware_test

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/middleware"
)

const testEndpointPath = "/mcp"

// deadlineTrackingResponseWriter wraps httptest.ResponseRecorder and implements
// the SetWriteDeadline method so http.ResponseController can call it.
// It records whether SetWriteDeadline was called and the deadline value passed.
type deadlineTrackingResponseWriter struct {
	*httptest.ResponseRecorder
	deadlineSet bool
	deadline    time.Time
}

func (d *deadlineTrackingResponseWriter) SetWriteDeadline(t time.Time) error {
	d.deadlineSet = true
	d.deadline = t
	return nil
}

func newDeadlineTracker() *deadlineTrackingResponseWriter {
	return &deadlineTrackingResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
	}
}

var noopHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func mw(next http.Handler) http.Handler {
	return middleware.WriteTimeout(testEndpointPath)(next)
}

// TestWriteTimeout_SSERequestClearsDeadline verifies that a qualifying SSE request
// (GET + Accept: text/event-stream + correct path) has its write deadline cleared
// (set to zero), overriding the server-level WriteTimeout.
func TestWriteTimeout_SSERequestClearsDeadline(t *testing.T) {
	t.Parallel()

	w := newDeadlineTracker()
	r := httptest.NewRequest(http.MethodGet, testEndpointPath, nil)
	r.Header.Set("Accept", "text/event-stream")

	mw(noopHandler).ServeHTTP(w, r)

	require.True(t, w.deadlineSet, "qualifying SSE request must call SetWriteDeadline")
	assert.True(t, w.deadline.IsZero(), "deadline must be zero (no deadline) to override server WriteTimeout")
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestWriteTimeout_GETWithoutAcceptHeaderLeavesDeadlineUntouched verifies that a GET
// request lacking Accept: text/event-stream is not treated as SSE and the middleware
// does not touch its write deadline, leaving http.Server.WriteTimeout in effect.
func TestWriteTimeout_GETWithoutAcceptHeaderLeavesDeadlineUntouched(t *testing.T) {
	t.Parallel()

	w := newDeadlineTracker()
	r := httptest.NewRequest(http.MethodGet, testEndpointPath, nil)

	mw(noopHandler).ServeHTTP(w, r)

	assert.False(t, w.deadlineSet, "non-SSE GET must not have its deadline touched; server WriteTimeout remains in effect")
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestWriteTimeout_GETOnWrongPathLeavesDeadlineUntouched verifies that a GET request
// with the SSE Accept header but targeting a non-MCP path (e.g. /health) is not treated
// as SSE and the middleware does not touch its write deadline.
func TestWriteTimeout_GETOnWrongPathLeavesDeadlineUntouched(t *testing.T) {
	t.Parallel()

	w := newDeadlineTracker()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.Header.Set("Accept", "text/event-stream")

	mw(noopHandler).ServeHTTP(w, r)

	assert.False(t, w.deadlineSet, "GET on non-MCP path must not have its deadline touched; server WriteTimeout remains in effect")
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestWriteTimeout_POSTLeavesDeadlineUntouched verifies that POST requests are not
// touched by the middleware — their deadline comes from http.Server.WriteTimeout.
func TestWriteTimeout_POSTLeavesDeadlineUntouched(t *testing.T) {
	t.Parallel()

	w := newDeadlineTracker()
	r := httptest.NewRequest(http.MethodPost, testEndpointPath, nil)

	mw(noopHandler).ServeHTTP(w, r)

	assert.False(t, w.deadlineSet, "POST deadline is managed by http.Server.WriteTimeout, not the middleware")
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestWriteTimeout_DELETELeavesDeadlineUntouched verifies DELETE is also left alone.
func TestWriteTimeout_DELETELeavesDeadlineUntouched(t *testing.T) {
	t.Parallel()

	w := newDeadlineTracker()
	r := httptest.NewRequest(http.MethodDelete, testEndpointPath, nil)

	mw(noopHandler).ServeHTTP(w, r)

	assert.False(t, w.deadlineSet, "DELETE deadline is managed by http.Server.WriteTimeout, not the middleware")
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestWriteTimeout_HandlerIsAlwaysCalled verifies the inner handler is invoked for
// every HTTP method, regardless of deadline management.
func TestWriteTimeout_HandlerIsAlwaysCalled(t *testing.T) {
	t.Parallel()

	cases := []struct {
		method string
		path   string
		accept string
	}{
		{http.MethodGet, testEndpointPath, "text/event-stream"}, // qualifying SSE
		{http.MethodGet, testEndpointPath, ""},                  // GET, no Accept
		{http.MethodGet, "/health", "text/event-stream"},        // GET, wrong path
		{http.MethodPost, testEndpointPath, ""},
		{http.MethodDelete, testEndpointPath, ""},
	}

	for _, tc := range cases {
		t.Run(tc.method+tc.path+tc.accept, func(t *testing.T) {
			t.Parallel()

			called := false
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			w := newDeadlineTracker()
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.accept != "" {
				r.Header.Set("Accept", tc.accept)
			}
			mw(handler).ServeHTTP(w, r)

			assert.True(t, called, "inner handler must be called for %s %s", tc.method, tc.path)
		})
	}
}

// TestWriteTimeout_SSEStreamSurvivesTimeout verifies over a real TCP connection (with
// http.Server.WriteTimeout set) that a qualifying SSE stream is NOT killed after the
// write timeout elapses.
//
// This is the end-to-end proof of the fix for the SSE connection drop bug
// (golang/go#16100): the middleware clears the per-connection write deadline for
// qualifying SSE requests via http.ResponseController.SetWriteDeadline(time.Time{}),
// keeping SSE streams alive past the server-level WriteTimeout.
func TestWriteTimeout_SSEStreamSurvivesTimeout(t *testing.T) {
	t.Parallel()

	const shortTimeout = 100 * time.Millisecond
	const streamDuration = 3 * shortTimeout

	sseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok, "ResponseWriter must implement http.Flusher")

		ticker := time.NewTicker(shortTimeout / 5)
		defer ticker.Stop()
		deadline := time.NewTimer(streamDuration)
		defer deadline.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				fmt.Fprintf(w, "data: ping\n\n")
				flusher.Flush()
			}
		}
	})

	ts := httptest.NewUnstartedServer(middleware.WriteTimeout(testEndpointPath)(sseHandler))
	ts.Config.WriteTimeout = shortTimeout
	ts.Start()
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+testEndpointPath, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")

	start := time.Now()

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	// tickInterval is shortTimeout/5; over the full streamDuration we expect
	// ~streamDuration/tickInterval = 15 events. If WriteTimeout fires early
	// (after shortTimeout = 100 ms) at most shortTimeout/tickInterval = 5
	// events could arrive before the connection is killed.
	const tickInterval = shortTimeout / 5
	minEvents := int(shortTimeout/tickInterval) + 1 // must exceed what's possible before WriteTimeout

	scanner := bufio.NewScanner(resp.Body)
	var events []string
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			events = append(events, scanner.Text())
		}
	}
	elapsed := time.Since(start)

	// A clean EOF with scanner.Err() == nil is necessary but not sufficient:
	// if WriteTimeout kills the stream at shortTimeout the client may still
	// observe a clean close with a handful of events already received.
	assert.NoError(t, scanner.Err(), "SSE stream must close cleanly, not with a connection error")

	// Elapsed time proves the stream ran for (at least) its intended lifetime.
	// If WriteTimeout had fired the handler would have been interrupted at ~100 ms,
	// far shorter than streamDuration (300 ms).
	assert.GreaterOrEqual(t, elapsed, streamDuration-50*time.Millisecond,
		"SSE stream must have lasted at least streamDuration (%v); elapsed %v suggests WriteTimeout fired early",
		streamDuration, elapsed)

	// Event count provides a second, independent signal: the stream must have
	// delivered more events than could possibly arrive within shortTimeout.
	assert.GreaterOrEqual(t, len(events), minEvents,
		"expected >= %d events (more than possible before WriteTimeout); got %d",
		minEvents, len(events))
}
