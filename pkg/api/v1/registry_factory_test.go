// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
)

// makeListServersRequest builds an httptest request for GET /{name}/servers
// with the chi URL param "name" set to registryName.
func makeListServersRequest(registryName string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/"+registryName+"/servers", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", registryName)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestNewRegistryRoutes_NoFactory_ReturnsValidRoutes verifies that NewRegistryRoutes
// returns a fully-initialised struct when no ProviderFactory is registered.
//
//nolint:paralleltest // Mutates global state: config.registeredFactory
func TestNewRegistryRoutes_NoFactory_ReturnsValidRoutes(t *testing.T) {
	config.RegisterProviderFactory(nil)
	t.Cleanup(func() { config.RegisterProviderFactory(nil) })

	routes := NewRegistryRoutes()

	require.NotNil(t, routes, "NewRegistryRoutes must return a non-nil value")
	assert.NotNil(t, routes.configProvider, "configProvider must be initialised")
	assert.NotNil(t, routes.configService, "configService must be initialised")
	assert.False(t, routes.serveMode, "serveMode must be false for NewRegistryRoutes")
}

// TestNewRegistryRoutesForServe_NoFactory_ReturnsValidRoutes verifies that
// NewRegistryRoutesForServe returns a fully-initialised struct with serveMode
// set to true when no ProviderFactory is registered.
//
//nolint:paralleltest // Mutates global state: config.registeredFactory
func TestNewRegistryRoutesForServe_NoFactory_ReturnsValidRoutes(t *testing.T) {
	config.RegisterProviderFactory(nil)
	t.Cleanup(func() { config.RegisterProviderFactory(nil) })

	routes := NewRegistryRoutesForServe()

	require.NotNil(t, routes, "NewRegistryRoutesForServe must return a non-nil value")
	assert.NotNil(t, routes.configProvider, "configProvider must be initialised")
	assert.NotNil(t, routes.configService, "configService must be initialised")
	assert.True(t, routes.serveMode, "serveMode must be true for NewRegistryRoutesForServe")
}

// TestListServers_DefaultStore_ReturnsServers verifies that the listServers
// handler returns a 200 response with servers from the default Store (which
// always includes the embedded catalog).
//
//nolint:paralleltest // Mutates global state: registry.currentStore
func TestListServers_DefaultStore_ReturnsServers(t *testing.T) {
	config.RegisterProviderFactory(nil)
	registry.ResetDefaultStore()
	t.Cleanup(func() {
		config.RegisterProviderFactory(nil)
		registry.ResetDefaultStore()
	})

	// Ensure no K8s environment
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	routes := NewRegistryRoutes()

	w := httptest.NewRecorder()
	routes.listServers(w, makeListServersRequest("default"))

	assert.Equal(t, http.StatusOK, w.Code,
		"listServers should return 200 from default Store")

	var body listServersResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body),
		"response body should be valid JSON")

	totalServers := len(body.Servers) + len(body.RemoteServers)
	assert.Greater(t, totalServers, 0,
		"default Store (embedded catalog) must contain at least one server")
}
