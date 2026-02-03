// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/stacklok/toolhive-core/env"
	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TestHeaderInjectionIntegration validates the complete flow of:
// 1. Creating a registry with header injection strategy
// 2. Converting MCPExternalAuthConfig to metadata
// 3. Resolving secrets
// 4. Using the strategy to authenticate a request
func TestHeaderInjectionIntegration(t *testing.T) {
	t.Parallel()

	t.Run("complete header injection flow with secret resolution", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		// Step 1: Create the outgoing auth registry with all strategies
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)
		require.NoError(t, err)
		require.NotNil(t, registry)

		// Step 2: Create a fake Kubernetes client with a secret
		scheme := runtime.NewScheme()
		_ = corev1.AddToScheme(scheme)
		_ = mcpv1alpha1.AddToScheme(scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-api-key",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("secret-api-key-12345"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(secret).
			Build()

		// Step 3: Create MCPExternalAuthConfig
		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
					ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: "test-api-key",
						Key:  "key",
					},
				},
			},
		}

		// Step 4: Convert to strategy using the converter
		converter := &converters.HeaderInjectionConverter{}
		strategy, err := converter.ConvertToStrategy(authConfig)
		require.NoError(t, err)
		require.NotNil(t, strategy)

		assert.Equal(t, "X-API-Key", strategy.HeaderInjection.HeaderName)

		// Step 5: Resolve secrets
		resolvedStrategy, err := converter.ResolveSecrets(ctx, authConfig, fakeClient, "default", strategy)
		require.NoError(t, err)
		require.NotNil(t, resolvedStrategy)

		// Verify secret was resolved
		assert.Equal(t, "X-API-Key", resolvedStrategy.HeaderInjection.HeaderName)
		assert.Equal(t, "secret-api-key-12345", resolvedStrategy.HeaderInjection.HeaderValue)

		// Step 6: Get the header injection strategy from registry
		authStrat, err := registry.GetStrategy("header_injection")
		require.NoError(t, err)
		require.NotNil(t, authStrat)

		// Step 7: Validate the strategy
		err = authStrat.Validate(resolvedStrategy)
		require.NoError(t, err)

		// Step 8: Use the strategy to authenticate a request
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		err = authStrat.Authenticate(ctx, req, resolvedStrategy)
		require.NoError(t, err)

		// Step 9: Verify the header was injected
		assert.Equal(t, "secret-api-key-12345", req.Header.Get("X-API-Key"))
	})

	t.Run("header injection with custom header name", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		// Create registry
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)
		require.NoError(t, err)

		// Create fake client with secret
		scheme := runtime.NewScheme()
		_ = corev1.AddToScheme(scheme)
		_ = mcpv1alpha1.AddToScheme(scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "custom-header-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"token": []byte("custom-token-value"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(secret).
			Build()

		// Create auth config with custom header
		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "custom-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-Custom-Auth-Token",
					ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: "custom-header-secret",
						Key:  "token",
					},
				},
			},
		}

		// Convert and resolve
		converter := &converters.HeaderInjectionConverter{}
		strategy, err := converter.ConvertToStrategy(authConfig)
		require.NoError(t, err)

		resolvedStrategy, err := converter.ResolveSecrets(ctx, authConfig, fakeClient, "default", strategy)
		require.NoError(t, err)

		// Get strategy and authenticate
		authStrat, err := registry.GetStrategy("header_injection")
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		err = authStrat.Authenticate(ctx, req, resolvedStrategy)
		require.NoError(t, err)

		// Verify custom header was injected
		assert.Equal(t, "custom-token-value", req.Header.Get("X-Custom-Auth-Token"))
		assert.Empty(t, req.Header.Get("X-API-Key"), "default header should not be set")
	})

	t.Run("header injection fails with missing secret", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		// Create empty fake client (no secrets)
		scheme := runtime.NewScheme()
		_ = corev1.AddToScheme(scheme)
		_ = mcpv1alpha1.AddToScheme(scheme)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		// Create auth config referencing non-existent secret
		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "missing-secret-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
					ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: "non-existent-secret",
						Key:  "key",
					},
				},
			},
		}

		// Convert should succeed (doesn't fetch secret yet)
		converter := &converters.HeaderInjectionConverter{}
		strategy, err := converter.ConvertToStrategy(authConfig)
		require.NoError(t, err)

		// Resolve secrets should fail
		_, err = converter.ResolveSecrets(ctx, authConfig, fakeClient, "default", strategy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get secret")
	})

	t.Run("header injection validates metadata before authentication", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		// Create registry
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)
		require.NoError(t, err)

		// Get strategy
		strategy, err := registry.GetStrategy("header_injection")
		require.NoError(t, err)

		// Test with invalid strategy (missing header_value)
		invalidStrategy := &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeHeaderInjection,
			HeaderInjection: &authtypes.HeaderInjectionConfig{
				HeaderName: "X-API-Key",
				// missing header_value
			},
		}

		// Validate should fail
		err = strategy.Validate(invalidStrategy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "header_value")

		// Authenticate should also fail
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		err = strategy.Authenticate(ctx, req, invalidStrategy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "header_value")
	})
}
