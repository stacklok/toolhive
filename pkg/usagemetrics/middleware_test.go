// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

func TestMiddleware_Handler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		mcpMethod       string
		expectIncrement bool
	}{
		{
			name:            "tool call increments counter",
			mcpMethod:       "tools/call",
			expectIncrement: true,
		},
		{
			name:            "non-tool call does not increment",
			mcpMethod:       "tools/list",
			expectIncrement: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Initialize a collector for this test
			collector, err := NewCollector()
			assert.NoError(t, err)
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				collector.Shutdown(ctx)
			}()

			// Create middleware with collector instance
			mw := &Middleware{
				collector: collector,
			}
			handler := mw.Handler()

			// Create a test request with MCP context
			req := httptest.NewRequest(http.MethodPost, "/messages", nil)

			// Add parsed MCP request to context
			parsedReq := &mcp.ParsedMCPRequest{
				Method:    tt.mcpMethod,
				IsRequest: true,
			}
			ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, parsedReq)
			req = req.WithContext(ctx)

			// Record initial count
			initialCount := collector.GetCurrentCount()

			// Create response recorder
			rr := httptest.NewRecorder()

			// Create a test handler that just returns 200
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			// Wrap with middleware
			wrappedHandler := handler(testHandler)

			// Execute request
			wrappedHandler.ServeHTTP(rr, req)

			// Verify count
			expectedCount := initialCount
			if tt.expectIncrement {
				expectedCount++
			}
			assert.Equal(t, expectedCount, collector.GetCurrentCount())
		})
	}
}

func TestMiddleware_Close(t *testing.T) {
	t.Parallel()

	// Initialize collector
	collector, err := NewCollector()
	assert.NoError(t, err)

	middleware := &Middleware{
		collector: collector,
	}

	// Test that Close returns nil and shuts down collector
	err = middleware.Close()
	assert.NoError(t, err)
}

func TestCreateMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    *types.MiddlewareConfig
		setupMock func(*mocks.MockMiddlewareRunner) *Middleware
	}{
		{
			name: "success",
			config: func() *types.MiddlewareConfig {
				params := MiddlewareParams{}
				paramsJSON, _ := json.Marshal(params)
				return &types.MiddlewareConfig{
					Type:       MiddlewareType,
					Parameters: paramsJSON,
				}
			}(),
			setupMock: func(mockRunner *mocks.MockMiddlewareRunner) *Middleware {
				var capturedMw *Middleware
				mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Do(func(_ string, mw types.Middleware) {
					typedMw, ok := mw.(*Middleware)
					assert.True(t, ok, "Expected middleware to be of type *Middleware")
					capturedMw = typedMw
				})
				return capturedMw
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mock controller and runner
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
			capturedMw := tt.setupMock(mockRunner)

			// Execute
			err := CreateMiddleware(tt.config, mockRunner)
			assert.NoError(t, err)

			// Cleanup the middleware if it was created
			if capturedMw != nil && capturedMw.collector != nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				capturedMw.collector.Shutdown(ctx)
			}
		})
	}
}
