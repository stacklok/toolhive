// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
				nil, nil, "", false,
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
