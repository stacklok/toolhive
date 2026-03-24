// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubProvider is a minimal Provider implementation used in factory tests.
type stubProvider struct {
	KubernetesProvider // embed no-op implementation to satisfy the interface
	label              string
}

func TestRegisterProviderFactory_NoFactoryRegistered(t *testing.T) {
	// Ensure clean state.
	registeredFactory = nil
	t.Cleanup(func() { registeredFactory = nil })

	// Ensure we are not detected as running in Kubernetes.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	provider := NewProvider()
	require.NotNil(t, provider)
	assert.IsType(t, &DefaultProvider{}, provider)
}

func TestRegisterProviderFactory_ReturnsNonNilProvider(t *testing.T) {
	registeredFactory = nil
	t.Cleanup(func() { registeredFactory = nil })

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	custom := &stubProvider{label: "custom"}
	RegisterProviderFactory(func() Provider {
		return custom
	})

	provider := NewProvider()
	require.NotNil(t, provider)
	assert.Same(t, custom, provider, "NewProvider should return the factory-provided provider")
}

func TestRegisterProviderFactory_ReturnsNil_FallsThrough(t *testing.T) {
	registeredFactory = nil
	t.Cleanup(func() { registeredFactory = nil })

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	RegisterProviderFactory(func() Provider {
		return nil
	})

	provider := NewProvider()
	require.NotNil(t, provider)
	assert.IsType(t, &DefaultProvider{}, provider, "NewProvider should fall back to DefaultProvider when factory returns nil")
}

func TestRegisterProviderFactory_SecondCallWins(t *testing.T) {
	registeredFactory = nil
	t.Cleanup(func() { registeredFactory = nil })

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	first := &stubProvider{label: "first"}
	second := &stubProvider{label: "second"}

	RegisterProviderFactory(func() Provider {
		return first
	})
	RegisterProviderFactory(func() Provider {
		return second
	})

	provider := NewProvider()
	require.NotNil(t, provider)
	assert.Same(t, second, provider, "The second registered factory should replace the first")
}

func TestRegisterProviderFactory_FactoryOverridesKubernetesDetection(t *testing.T) {
	registeredFactory = nil
	t.Cleanup(func() { registeredFactory = nil })

	// Simulate a Kubernetes environment.
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")

	custom := &stubProvider{label: "custom-in-k8s"}
	RegisterProviderFactory(func() Provider {
		return custom
	})

	provider := NewProvider()
	require.NotNil(t, provider)
	assert.Same(t, custom, provider, "Factory provider should take precedence over KubernetesProvider")
}
