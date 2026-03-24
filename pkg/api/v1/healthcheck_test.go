// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/server/discovery"
)

func TestGetHealthcheck(t *testing.T) {
	t.Parallel()

	t.Run("returns 204 when runtime is running", func(t *testing.T) {
		t.Parallel()
		// Create a new gomock controller for this subtest
		ctrl := gomock.NewController(t)
		t.Cleanup(func() {
			ctrl.Finish()
		})

		// Create a mock runtime
		mockRuntime := mocks.NewMockRuntime(ctrl)

		// Create healthcheck routes with the mock runtime
		routes := &healthcheckRoutes{containerRuntime: mockRuntime}

		// Setup mock to return nil (no error) when IsRunning is called
		mockRuntime.EXPECT().
			IsRunning(gomock.Any()).
			Return(nil)

		// Create a test request and response recorder
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		resp := httptest.NewRecorder()

		// Call the handler
		routes.getHealthcheck(resp, req)

		// Assert the response
		assert.Equal(t, http.StatusNoContent, resp.Code)
		assert.Empty(t, resp.Body.String())
	})

	t.Run("returns 503 when runtime is not running", func(t *testing.T) {
		t.Parallel()
		// Create a new gomock controller for this subtest
		ctrl := gomock.NewController(t)
		t.Cleanup(func() {
			ctrl.Finish()
		})

		// Create a mock runtime
		mockRuntime := mocks.NewMockRuntime(ctrl)

		// Create healthcheck routes with the mock runtime
		routes := &healthcheckRoutes{containerRuntime: mockRuntime}

		// Create an error to return
		expectedError := errors.New("container runtime is not available")

		// Setup mock to return an error when IsRunning is called
		mockRuntime.EXPECT().
			IsRunning(gomock.Any()).
			Return(expectedError)

		// Create a test request and response recorder
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		resp := httptest.NewRecorder()

		// Call the handler
		routes.getHealthcheck(resp, req)

		// Assert the response
		assert.Equal(t, http.StatusServiceUnavailable, resp.Code)
		assert.Equal(t, expectedError.Error()+"\n", resp.Body.String())
	})
}

func TestGetHealthcheck_ReturnsNonceHeader(t *testing.T) {
	t.Parallel()

	// Create a new gomock controller
	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		ctrl.Finish()
	})

	// Create a mock runtime
	mockRuntime := mocks.NewMockRuntime(ctrl)

	// Create healthcheck routes with a nonce value
	routes := &healthcheckRoutes{containerRuntime: mockRuntime, nonce: "test-nonce-value"}

	// Setup mock to return nil (healthy) when IsRunning is called
	mockRuntime.EXPECT().
		IsRunning(gomock.Any()).
		Return(nil)

	// Create a test request and response recorder
	req := httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(t.Context())
	resp := httptest.NewRecorder()

	// Call the handler
	routes.getHealthcheck(resp, req)

	// Assert the response status and nonce header
	assert.Equal(t, http.StatusNoContent, resp.Code)
	assert.Equal(t, "test-nonce-value", resp.Header().Get(discovery.NonceHeader))
}

func TestGetHealthcheck_OmitsNonceHeaderWhenEmpty(t *testing.T) {
	t.Parallel()

	// Create a new gomock controller
	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		ctrl.Finish()
	})

	// Create a mock runtime
	mockRuntime := mocks.NewMockRuntime(ctrl)

	// Create healthcheck routes with an empty nonce
	routes := &healthcheckRoutes{containerRuntime: mockRuntime, nonce: ""}

	// Setup mock to return nil (healthy) when IsRunning is called
	mockRuntime.EXPECT().
		IsRunning(gomock.Any()).
		Return(nil)

	// Create a test request and response recorder
	req := httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(t.Context())
	resp := httptest.NewRecorder()

	// Call the handler
	routes.getHealthcheck(resp, req)

	// Assert the response status and absence of nonce header
	assert.Equal(t, http.StatusNoContent, resp.Code)
	assert.Empty(t, resp.Header().Get(discovery.NonceHeader))
	assert.Empty(t, resp.Header().Values(discovery.NonceHeader))
}

func TestGetHealthcheck_NoNonceOnUnhealthy(t *testing.T) {
	t.Parallel()

	// Create a new gomock controller
	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		ctrl.Finish()
	})

	// Create a mock runtime
	mockRuntime := mocks.NewMockRuntime(ctrl)

	// Create healthcheck routes with a nonce value
	routes := &healthcheckRoutes{containerRuntime: mockRuntime, nonce: "test-nonce"}

	// Setup mock to return an error (unhealthy) when IsRunning is called
	mockRuntime.EXPECT().
		IsRunning(gomock.Any()).
		Return(errors.New("runtime unavailable"))

	// Create a test request and response recorder
	req := httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(t.Context())
	resp := httptest.NewRecorder()

	// Call the handler
	routes.getHealthcheck(resp, req)

	// Assert the response status and absence of nonce header
	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)
	assert.Empty(t, resp.Header().Get(discovery.NonceHeader))
	assert.Empty(t, resp.Header().Values(discovery.NonceHeader))
}
