package converters

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestDefaultRegistry(t *testing.T) {
	t.Parallel()

	t.Run("returns singleton instance", func(t *testing.T) {
		t.Parallel()

		// Get registry multiple times
		registry1 := DefaultRegistry()
		registry2 := DefaultRegistry()
		registry3 := DefaultRegistry()

		// All should be the same instance
		assert.Same(t, registry1, registry2, "DefaultRegistry should return same instance")
		assert.Same(t, registry2, registry3, "DefaultRegistry should return same instance")
	})

	t.Run("singleton is initialized once", func(t *testing.T) {
		t.Parallel()

		// Access registry from multiple goroutines concurrently
		const numGoroutines = 100
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		registries := make(chan *Registry, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer wg.Done()
				registries <- DefaultRegistry()
			}()
		}

		wg.Wait()
		close(registries)

		// Verify all goroutines got the same instance
		var firstRegistry *Registry
		for registry := range registries {
			if firstRegistry == nil {
				firstRegistry = registry
			} else {
				assert.Same(t, firstRegistry, registry, "all goroutines should get same registry instance")
			}
		}
	})

	t.Run("has all built-in converters registered", func(t *testing.T) {
		t.Parallel()

		registry := DefaultRegistry()

		// Test token exchange converter
		tokenExchangeConverter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeTokenExchange)
		require.NoError(t, err)
		require.NotNil(t, tokenExchangeConverter)
		assert.Equal(t, "token_exchange", tokenExchangeConverter.StrategyType())

		// Test header injection converter
		headerInjectionConverter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeHeaderInjection)
		require.NoError(t, err)
		require.NotNil(t, headerInjectionConverter)
		assert.Equal(t, "header_injection", headerInjectionConverter.StrategyType())
	})
}

func TestNewRegistry(t *testing.T) {
	t.Parallel()

	t.Run("creates new registry with built-in converters", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		require.NotNil(t, registry)
		require.NotNil(t, registry.converters)

		// Verify built-in converters are registered
		tokenExchangeConverter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeTokenExchange)
		require.NoError(t, err)
		assert.NotNil(t, tokenExchangeConverter)

		headerInjectionConverter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeHeaderInjection)
		require.NoError(t, err)
		assert.NotNil(t, headerInjectionConverter)
	})

	t.Run("creates independent instances", func(t *testing.T) {
		t.Parallel()

		registry1 := NewRegistry()
		registry2 := NewRegistry()

		// Should be different instances (unlike DefaultRegistry)
		assert.NotSame(t, registry1, registry2, "NewRegistry should create new instances")
	})

	t.Run("registers correct converter types", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		// Verify correct types are registered
		testCases := []struct {
			authType     mcpv1alpha1.ExternalAuthType
			expectedType string
		}{
			{mcpv1alpha1.ExternalAuthTypeTokenExchange, "token_exchange"},
			{mcpv1alpha1.ExternalAuthTypeHeaderInjection, "header_injection"},
		}

		for _, tc := range testCases {
			converter, err := registry.GetConverter(tc.authType)
			require.NoError(t, err, "should get converter for %s", tc.authType)
			assert.Equal(t, tc.expectedType, converter.StrategyType(),
				"auth type %s should map to strategy type %s", tc.authType, tc.expectedType)
		}
	})
}

func TestRegistry_Register(t *testing.T) {
	t.Parallel()

	t.Run("registers new converter", func(t *testing.T) {
		t.Parallel()

		registry := &Registry{
			converters: make(map[mcpv1alpha1.ExternalAuthType]StrategyConverter),
		}

		converter := &HeaderInjectionConverter{}
		registry.Register(mcpv1alpha1.ExternalAuthTypeHeaderInjection, converter)

		retrieved, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeHeaderInjection)
		require.NoError(t, err)
		assert.Same(t, converter, retrieved)
	})

	t.Run("overwrites existing converter", func(t *testing.T) {
		t.Parallel()

		registry := &Registry{
			converters: make(map[mcpv1alpha1.ExternalAuthType]StrategyConverter),
		}

		// Register a HeaderInjectionConverter first
		converter1 := &HeaderInjectionConverter{}
		registry.Register(mcpv1alpha1.ExternalAuthTypeHeaderInjection, converter1)

		// Verify first converter is registered
		retrieved, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeHeaderInjection)
		require.NoError(t, err)
		assert.Equal(t, "header_injection", retrieved.StrategyType())

		// Register a TokenExchangeConverter with same auth type (should overwrite)
		converter2 := &TokenExchangeConverter{}
		registry.Register(mcpv1alpha1.ExternalAuthTypeHeaderInjection, converter2)

		// Verify second converter overwrote the first
		retrieved, err = registry.GetConverter(mcpv1alpha1.ExternalAuthTypeHeaderInjection)
		require.NoError(t, err)
		assert.Equal(t, "token_exchange", retrieved.StrategyType(), "should return second converter with different strategy type")
	})

	t.Run("is thread-safe", func(t *testing.T) {
		t.Parallel()

		registry := &Registry{
			converters: make(map[mcpv1alpha1.ExternalAuthType]StrategyConverter),
		}

		const numGoroutines = 50
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		// Register converters concurrently
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()
				// Use different auth types to avoid overwrites
				authType := mcpv1alpha1.ExternalAuthType("test-type-" + string(rune('A'+id%26)))
				converter := &HeaderInjectionConverter{}
				registry.Register(authType, converter)
			}(i)
		}

		wg.Wait()

		// Should have registered all converters without races
		assert.GreaterOrEqual(t, len(registry.converters), 1)
	})
}

func TestRegistry_GetConverter(t *testing.T) {
	t.Parallel()

	t.Run("returns registered converter", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		converter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeHeaderInjection)
		require.NoError(t, err)
		require.NotNil(t, converter)
		assert.IsType(t, &HeaderInjectionConverter{}, converter)
	})

	t.Run("returns error for unsupported auth type", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		converter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthType("unsupported"))
		assert.Error(t, err)
		assert.Nil(t, converter)
		assert.Contains(t, err.Error(), "unsupported auth type")
		assert.Contains(t, err.Error(), "unsupported")
	})

	t.Run("is thread-safe for concurrent reads", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		const numGoroutines = 100
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		errs := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer wg.Done()
				converter, err := registry.GetConverter(mcpv1alpha1.ExternalAuthTypeHeaderInjection)
				if err != nil {
					errs <- err
					return
				}
				if converter.StrategyType() != "header_injection" {
					errs <- assert.AnError
				}
			}()
		}

		wg.Wait()
		close(errs)

		// Should have no errors
		for err := range errs {
			t.Errorf("concurrent GetConverter failed: %v", err)
		}
	})
}

func TestConvertToStrategyMetadata(t *testing.T) {
	t.Parallel()

	t.Run("converts header injection config", func(t *testing.T) {
		t.Parallel()

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
						Name: "api-secret",
						Key:  "key",
					},
				},
			},
		}

		strategyType, metadata, err := ConvertToStrategyMetadata(authConfig)
		require.NoError(t, err)
		assert.Equal(t, "header_injection", strategyType)
		assert.NotNil(t, metadata)
		assert.Equal(t, "X-API-Key", metadata["header_name"])
		assert.Equal(t, "TOOLHIVE_HEADER_INJECTION_VALUE", metadata["header_value_env"])
	})

	t.Run("converts token exchange config", func(t *testing.T) {
		t.Parallel()

		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
					ClientID: "test-client",
					ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: "oauth-secret",
						Key:  "client-secret",
					},
					Audience: "api.example.com",
					Scopes:   []string{"openid", "profile"},
				},
			},
		}

		strategyType, metadata, err := ConvertToStrategyMetadata(authConfig)
		require.NoError(t, err)
		assert.Equal(t, "token_exchange", strategyType)
		assert.NotNil(t, metadata)
		// Token exchange metadata contains URLs and config
		assert.NotEmpty(t, metadata)
	})

	t.Run("returns error for nil config", func(t *testing.T) {
		t.Parallel()

		strategyType, metadata, err := ConvertToStrategyMetadata(nil)
		assert.Error(t, err)
		assert.Empty(t, strategyType)
		assert.Nil(t, metadata)
		assert.Contains(t, err.Error(), "external auth config is nil")
	})

	t.Run("returns error for unsupported auth type", func(t *testing.T) {
		t.Parallel()

		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthType("unsupported"),
			},
		}

		strategyType, metadata, err := ConvertToStrategyMetadata(authConfig)
		assert.Error(t, err)
		assert.Empty(t, strategyType)
		assert.Nil(t, metadata)
		assert.Contains(t, err.Error(), "unsupported auth type")
	})

	t.Run("returns error for invalid config", func(t *testing.T) {
		t.Parallel()

		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type:            mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: nil, // Invalid: missing required config
			},
		}

		strategyType, metadata, err := ConvertToStrategyMetadata(authConfig)
		assert.Error(t, err)
		assert.Empty(t, strategyType)
		assert.Nil(t, metadata)
		assert.Contains(t, err.Error(), "nil")
	})
}

func TestResolveSecretsForStrategy(t *testing.T) {
	t.Parallel()

	t.Run("resolves header injection secrets", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		// Create fake client with secret
		scheme := runtime.NewScheme()
		_ = corev1.AddToScheme(scheme)
		_ = mcpv1alpha1.AddToScheme(scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "api-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("secret-value-123"),
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(secret).
			Build()

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
						Name: "api-secret",
						Key:  "key",
					},
				},
			},
		}

		initialMetadata := map[string]any{
			"header_name":      "X-API-Key",
			"header_value_env": "TOOLHIVE_HEADER_INJECTION_VALUE",
		}

		resolvedMetadata, err := ResolveSecretsForStrategy(ctx, authConfig, k8sClient, "default", initialMetadata)
		require.NoError(t, err)
		assert.NotNil(t, resolvedMetadata)
		assert.Equal(t, "X-API-Key", resolvedMetadata["header_name"])
		assert.Equal(t, "secret-value-123", resolvedMetadata["header_value"])
	})

	t.Run("returns error for nil config", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		scheme := runtime.NewScheme()
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		inputMetadata := map[string]any{}
		metadata, err := ResolveSecretsForStrategy(ctx, nil, k8sClient, "default", inputMetadata)
		assert.Error(t, err)
		assert.Nil(t, metadata, "should return nil on error")
		assert.Contains(t, err.Error(), "external auth config is nil")
	})

	t.Run("returns error for unsupported auth type", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		scheme := runtime.NewScheme()
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthType("unsupported"),
			},
		}

		inputMetadata := map[string]any{}
		metadata, err := ResolveSecretsForStrategy(ctx, authConfig, k8sClient, "default", inputMetadata)
		assert.Error(t, err)
		assert.Nil(t, metadata, "should return nil on error")
		assert.Contains(t, err.Error(), "unsupported auth type")
	})

	t.Run("returns error when secret resolution fails", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		// Create empty fake client (no secrets)
		scheme := runtime.NewScheme()
		_ = corev1.AddToScheme(scheme)
		_ = mcpv1alpha1.AddToScheme(scheme)

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

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
						Name: "missing-secret",
						Key:  "key",
					},
				},
			},
		}

		initialMetadata := map[string]any{
			"header_name":      "X-API-Key",
			"header_value_env": "TOOLHIVE_HEADER_INJECTION_VALUE",
		}

		metadata, err := ResolveSecretsForStrategy(ctx, authConfig, k8sClient, "default", initialMetadata)
		assert.Error(t, err)
		assert.Nil(t, metadata, "should return nil on error")
		assert.Contains(t, err.Error(), "failed to get secret")
	})
}
