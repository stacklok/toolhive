// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistryTimeout_InvalidJSON tests that invalid JSON returns 400 (not 504)
func TestRegistryTimeout_InvalidJSON(t *testing.T) {
	t.Parallel()

	// Create test server that returns valid HTTP but invalid registry JSON
	invalidServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"not": "a valid registry"}`))
	}))
	defer invalidServer.Close()

	// Create test config provider
	configProvider, cleanup := CreateTestConfigProvider(t, nil)
	defer cleanup()

	// Create registry routes
	routes := NewRegistryRoutesWithProvider(configProvider)

	allowPrivate := true
	updateReq := UpdateRegistryRequest{
		URL:            &invalidServer.URL,
		AllowPrivateIP: &allowPrivate,
	}
	reqBody, err := json.Marshal(updateReq)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/default", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	recorder := httptest.NewRecorder()

	// Execute request
	routes.updateRegistry(recorder, req)

	// Verify response - should be 400 for invalid format (not 504)
	assert.Equal(t, http.StatusBadRequest, recorder.Code,
		"Expected 400 Bad Request for invalid registry format")
	assert.NotContains(t, recorder.Body.String(), "timeout",
		"Error message should not mention timeout for format errors")
}
