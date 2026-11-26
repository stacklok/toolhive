// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowBodyReader trickles bytes one at a time with a delay before each, so a full
// request body takes far longer than the server ReadTimeout to upload.
type slowBodyReader struct {
	total   int
	delay   time.Duration
	emitted int
}

func (r *slowBodyReader) Read(p []byte) (int, error) {
	if r.emitted >= r.total {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.emitted++
	if len(p) > 0 {
		p[0] = ' '
		return 1, nil
	}
	return 0, nil
}

// TestTransparentProxy_ReadTimeoutConfigured verifies the read timeout option is
// wired onto the proxy's http.Server, and that non-positive values keep the
// default. WriteTimeout is intentionally not set on this proxy because it
// forwards arbitrary backend paths whose long-lived streams it cannot protect.
func TestTransparentProxy_ReadTimeoutConfigured(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	tests := []struct {
		name     string
		opts     []Option
		wantRead time.Duration
	}{
		{"default", nil, defaultReadTimeout},
		{"override applied", []Option{WithReadTimeout(5 * time.Second)}, 5 * time.Second},
		{"non-positive ignored", []Option{WithReadTimeout(0)}, defaultReadTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proxy := NewTransparentProxyWithOptions(
				"127.0.0.1", 0, backend.URL,
				nil, nil, nil,
				false, false, "sse",
				nil, nil, nil, "", false,
				nil,
				tt.opts...,
			)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() {
				cancel()
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer stopCancel()
				_ = proxy.Stop(stopCtx)
			})
			require.NoError(t, proxy.Start(ctx))

			require.NotNil(t, proxy.server)
			assert.Equal(t, tt.wantRead, proxy.server.ReadTimeout)
			// WriteTimeout must remain unset on the transparent proxy.
			assert.Zero(t, proxy.server.WriteTimeout)
		})
	}
}

// TestTransparentProxy_SlowBodyUploadTerminatedByReadTimeout proves a slow trickle
// upload is terminated near ReadTimeout rather than forwarded to the backend.
func TestTransparentProxy_SlowBodyUploadTerminatedByReadTimeout(t *testing.T) {
	t.Parallel()

	const readTimeout = 300 * time.Millisecond

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, backend.URL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, nil, "", false,
		nil,
		WithReadTimeout(readTimeout),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	require.NoError(t, proxy.Start(ctx))

	body := &slowBodyReader{total: 15, delay: 200 * time.Millisecond}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s/mcp", proxy.listener.Addr().String()), body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.total)

	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if resp != nil {
		_ = resp.Body.Close()
	}

	if err == nil {
		assert.NotEqual(t, http.StatusOK, resp.StatusCode,
			"slow upload should not be accepted as a successful request")
	}
	assert.Less(t, elapsed, 2*time.Second,
		"connection should be terminated near ReadTimeout, not held for the full upload")
}
