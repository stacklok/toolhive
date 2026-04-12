// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
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

// TestRegistryUpdate_URL tests that a URL-based registry update succeeds.
// Validation of the URL content is deferred to load time, not set time.
func TestRegistryUpdate_URL(t *testing.T) {
	t.Parallel()

	// Create test server that returns valid HTTP
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"not": "a valid registry"}`))
	}))
	defer testServer.Close()

	// Create test config provider
	configProvider, cleanup := CreateTestConfigProvider(t, nil)
	defer cleanup()

	routes := NewRegistryRoutesWithProvider(configProvider)

	allowPrivate := true
	updateReq := UpdateRegistryRequest{
		URL:            &testServer.URL,
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
	routes.updateRegistry(recorder, req)

	// In the new architecture, registry configuration is stored without
	// content validation. Validation happens at load time.
	assert.Equal(t, http.StatusOK, recorder.Code,
		"URL registry update should succeed (validation deferred to load time)")
}
