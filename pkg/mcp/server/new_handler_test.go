// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package server contains unit tests for the two-step sequence that NewHandler
// performs when obtaining a registry provider:
//
//  1. config.NewProvider()                          — obtains the config provider
//  2. registry.GetDefaultProviderWithConfig(...)    — seeds the registry singleton
//
// Why NewHandler cannot be tested directly:
// NewHandler calls workloads.NewManager(ctx) as its very first step. That call
// probes the local container runtime (Docker / containerd) and fails in CI
// environments and developer machines that do not have a live runtime. Rather
// than requiring a running Docker daemon in every test environment, these tests
// isolate and verify the specific invariant the fix protects:
//
// Invariant: when a custom ProviderFactory is registered via
// config.RegisterProviderFactory, config.NewProvider() must return the
// factory-produced provider, and that provider must be the one passed into
// registry.GetDefaultProviderWithConfig so the registry singleton is seeded
// with the correct (possibly enterprise) configuration.
//
// The bug (pre-fix) was that NewHandler called config.NewDefaultProvider()
// directly, which bypasses any registered ProviderFactory.
//
// Each test must NOT run in parallel because both config.registeredFactory and
// registry.currentProviderState are package-level singletons. Parallel
// execution would cause tests to observe each other's state changes, producing
// non-deterministic failures.
package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	configmocks "github.com/stacklok/toolhive/pkg/config/mocks"
	"github.com/stacklok/toolhive/pkg/registry"
)

// resetProviderGlobals clears both the config factory singleton and the
// registry provider singleton, and registers t.Cleanup hooks to do so again
// after the test. This ensures that each test starts and ends with a clean
// global state regardless of test execution order.
func resetProviderGlobals(t *testing.T) {
	t.Helper()
	config.RegisterProviderFactory(nil)
	registry.ResetDefaultProvider()
	t.Cleanup(func() {
		config.RegisterProviderFactory(nil)
		registry.ResetDefaultProvider()
	})
}

// TestNewProvider_FactoryIsInvoked verifies that registering a ProviderFactory
// causes config.NewProvider() to call it. This is the first step in NewHandler
// and proves that the factory hook is reachable.
//
//nolint:paralleltest // Mutates global config factory and registry provider singletons
func TestNewProvider_FactoryIsInvoked(t *testing.T) {
	resetProviderGlobals(t)

	factoryCalled := false
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	mockCfgProvider := configmocks.NewMockProvider(ctrl)
	// LoadOrCreateConfig is called by GetDefaultProviderWithConfig; allow any
	// number of calls so that mock verification does not interfere with the
	// factoryCalled assertion.
	mockCfgProvider.EXPECT().LoadOrCreateConfig().Return(&config.Config{}, nil).AnyTimes()

	config.RegisterProviderFactory(func() config.Provider {
		factoryCalled = true
		return mockCfgProvider
	})

	// Replicate the two-step sequence from NewHandler.
	cfgProvider := config.NewProvider()
	_, err := registry.GetDefaultProviderWithConfig(cfgProvider, registry.WithInteractive(false))
	require.NoError(t, err)

	assert.True(t, factoryCalled,
		"config.NewProvider() must invoke a registered ProviderFactory; "+
			"the old code called config.NewDefaultProvider() which bypasses the factory")
}

// TestNewProvider_FactoryConfigIsConsumedByRegistry verifies that the provider
// returned by the registered factory is the one ultimately used by the registry
// singleton. The gomock controller enforces that LoadOrCreateConfig is called
// exactly once — proving the mock (factory) provider, not the default provider,
// was consumed.
//
//nolint:paralleltest // Mutates global config factory and registry provider singletons
func TestNewProvider_FactoryConfigIsConsumedByRegistry(t *testing.T) {
	resetProviderGlobals(t)

	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	mockCfgProvider := configmocks.NewMockProvider(ctrl)
	// Expect exactly one call: GetDefaultProviderWithConfig calls
	// configProvider.LoadOrCreateConfig() inside its sync.Once closure.
	mockCfgProvider.EXPECT().LoadOrCreateConfig().Return(&config.Config{}, nil).Times(1)

	config.RegisterProviderFactory(func() config.Provider {
		return mockCfgProvider
	})

	// Replicate the two-step sequence from NewHandler.
	cfgProvider := config.NewProvider()
	registryProvider, err := registry.GetDefaultProviderWithConfig(cfgProvider, registry.WithInteractive(false))
	require.NoError(t, err)
	assert.NotNil(t, registryProvider,
		"GetDefaultProviderWithConfig must return a non-nil registry provider when "+
			"the mock config provider returns a valid (empty) config")
	// The gomock controller Finish() call (registered in t.Cleanup above) will
	// fail the test if LoadOrCreateConfig was not called exactly once, proving
	// the mock provider — not the default provider — was consumed.
}

// TestNewProvider_NoFactory_FallsBackToPlatformDefault verifies that when no
// ProviderFactory is registered, config.NewProvider() falls back to the
// platform-appropriate built-in provider. On a non-Kubernetes host this must be
// *config.DefaultProvider; on a Kubernetes host it must be
// *config.KubernetesProvider. Either satisfies the invariant that no mock
// provider leaks through when no factory is installed.
//
//nolint:paralleltest // Mutates global config factory singleton
func TestNewProvider_NoFactory_FallsBackToPlatformDefault(t *testing.T) {
	resetProviderGlobals(t)

	// Unset Kubernetes env vars so detection falls back to DefaultProvider on
	// developer/CI machines that are not running inside a cluster.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	provider := config.NewProvider()
	require.NotNil(t, provider, "NewProvider must never return nil")

	_, isDefault := provider.(*config.DefaultProvider)
	_, isKubernetes := provider.(*config.KubernetesProvider)
	assert.True(t, isDefault || isKubernetes,
		"without a registered factory, NewProvider must return *DefaultProvider or *KubernetesProvider, got %T", provider)
}

// TestGetDefaultProviderWithConfig_NonInteractiveModeSucceeds verifies that
// passing registry.WithInteractive(false) — exactly as NewHandler does — does
// not cause an error and returns a non-nil provider. This exercises the option
// plumbing through GetDefaultProviderWithConfig and guards against regressions
// where the non-interactive flag is mis-wired.
//
//nolint:paralleltest // Mutates global registry provider singleton
func TestGetDefaultProviderWithConfig_NonInteractiveModeSucceeds(t *testing.T) {
	resetProviderGlobals(t)

	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	mockCfgProvider := configmocks.NewMockProvider(ctrl)
	mockCfgProvider.EXPECT().LoadOrCreateConfig().Return(&config.Config{}, nil).Times(1)

	registryProvider, err := registry.GetDefaultProviderWithConfig(
		mockCfgProvider,
		registry.WithInteractive(false),
	)

	require.NoError(t, err,
		"GetDefaultProviderWithConfig with WithInteractive(false) must not return an error")
	assert.NotNil(t, registryProvider,
		"GetDefaultProviderWithConfig must return a non-nil provider in non-interactive mode")
}
