// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/container/docker"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestHTTPTransport_ShouldRestart tests the ShouldRestart logic
func TestHTTPTransport_ShouldRestart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		exitError      error
		expectedResult bool
	}{
		{
			name:           "container exited - should restart",
			exitError:      fmt.Errorf("container exited unexpectedly"),
			expectedResult: true,
		},
		{
			name:           "container removed - should not restart",
			exitError:      docker.NewContainerError(docker.ErrContainerRemoved, "test", "Container removed"),
			expectedResult: false,
		},
		{
			name:           "no error - should not restart",
			exitError:      nil,
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			transport := &HTTPTransport{
				containerName:    "test-container",
				containerExitErr: tt.exitError,
			}

			result := transport.ShouldRestart()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestHTTPTransport_SetOnUnauthorizedResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		callback          types.UnauthorizedResponseCallback
		expectCallbackNil bool
	}{
		{
			name: "set valid callback",
			callback: func() {
				// Test callback
			},
			expectCallbackNil: false,
		},
		{
			name:              "set nil callback",
			callback:          nil,
			expectCallbackNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			transport := &HTTPTransport{
				isMarkedUnauthorized: false,
			}

			transport.SetOnUnauthorizedResponse(tt.callback)

			if tt.expectCallbackNil {
				assert.Nil(t, transport.onUnauthorizedResponse)
			} else {
				assert.NotNil(t, transport.onUnauthorizedResponse)
			}
		})
	}
}

func TestHTTPTransport_checkAndMarkUnauthorized(t *testing.T) {
	t.Parallel()

	t.Run("first call marks as unauthorized", func(t *testing.T) {
		t.Parallel()

		transport := &HTTPTransport{
			isMarkedUnauthorized: false,
		}

		// First call should return false (not already marked, proceed with update)
		shouldSkip := transport.checkAndMarkUnauthorized()
		assert.False(t, shouldSkip, "First call should not skip")
		assert.True(t, transport.isMarkedUnauthorized, "Should be marked as unauthorized")
	})

	t.Run("subsequent calls skip update", func(t *testing.T) {
		t.Parallel()

		transport := &HTTPTransport{
			isMarkedUnauthorized: true,
		}

		// Subsequent call should return true (already marked, skip update)
		shouldSkip := transport.checkAndMarkUnauthorized()
		assert.True(t, shouldSkip, "Subsequent call should skip")
		assert.True(t, transport.isMarkedUnauthorized, "Should remain marked")
	})

	t.Run("concurrent calls are safe", func(t *testing.T) {
		t.Parallel()

		transport := &HTTPTransport{
			isMarkedUnauthorized: false,
		}

		const numGoroutines = 10
		var wg sync.WaitGroup
		results := make([]bool, numGoroutines)

		// Concurrent calls
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				results[idx] = transport.checkAndMarkUnauthorized()
			}(i)
		}

		wg.Wait()

		// Only one call should return false (not skip), rest should return true (skip)
		falseCount := 0
		for _, result := range results {
			if !result {
				falseCount++
			}
		}

		assert.Equal(t, 1, falseCount, "Only one call should proceed with update")
		assert.True(t, transport.isMarkedUnauthorized, "Should be marked as unauthorized")
	})
}

func TestHTTPTransport_UnauthorizedResponseCallback(t *testing.T) {
	t.Parallel()

	t.Run("callback invoked only once", func(t *testing.T) {
		t.Parallel()

		transport := &HTTPTransport{
			isMarkedUnauthorized: false,
		}

		callCount := 0
		var mu sync.Mutex

		callback := func() {
			mu.Lock()
			defer mu.Unlock()
			callCount++
		}

		transport.SetOnUnauthorizedResponse(callback)

		// Call multiple times
		for i := 0; i < 5; i++ {
			transport.onUnauthorizedResponse()
		}

		mu.Lock()
		actualCount := callCount
		mu.Unlock()

		assert.Equal(t, 1, actualCount, "Callback should be invoked only once")
	})

	t.Run("nil callback does not panic", func(t *testing.T) {
		t.Parallel()

		transport := &HTTPTransport{
			isMarkedUnauthorized: false,
		}

		transport.SetOnUnauthorizedResponse(nil)
		assert.Nil(t, transport.onUnauthorizedResponse)

		// Setting nil again should not panic
		transport.SetOnUnauthorizedResponse(nil)
		assert.Nil(t, transport.onUnauthorizedResponse)
	})
}

func TestHasTokenExchangeMiddleware(t *testing.T) {
	t.Parallel()

	dummyMiddleware := func(next http.Handler) http.Handler { return next }

	tests := []struct {
		name        string
		middlewares []types.NamedMiddleware
		want        bool
	}{
		{
			name:        "empty",
			middlewares: nil,
			want:        false,
		},
		{
			name: "not found",
			middlewares: []types.NamedMiddleware{
				{Name: "auth", Function: dummyMiddleware},
			},
			want: false,
		},
		{
			name: "found",
			middlewares: []types.NamedMiddleware{
				{Name: "auth", Function: dummyMiddleware},
				{Name: tokenexchange.MiddlewareType, Function: dummyMiddleware},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, hasTokenExchangeMiddleware(tt.middlewares))
		})
	}
}
