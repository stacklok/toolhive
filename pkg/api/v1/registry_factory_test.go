// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
)

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
