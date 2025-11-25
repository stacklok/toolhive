package converters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestRegistry_GetConverter(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()

	tests := []struct {
		name             string
		authType         mcpv1alpha1.ExternalAuthType
		wantStrategyType string
		wantErr          bool
	}{
		{
			name:             "token exchange converter exists",
			authType:         mcpv1alpha1.ExternalAuthTypeTokenExchange,
			wantStrategyType: "token_exchange",
			wantErr:          false,
		},
		{
			name:             "header injection converter exists",
			authType:         mcpv1alpha1.ExternalAuthTypeHeaderInjection,
			wantStrategyType: "header_injection",
			wantErr:          false,
		},
		{
			name:     "unsupported auth type",
			authType: "unsupported",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter, err := registry.GetConverter(tt.authType)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported auth type")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, converter)
			assert.Equal(t, tt.wantStrategyType, converter.StrategyType())
		})
	}
}

func TestTokenExchangeConverter_Integration(t *testing.T) {
	t.Parallel()

	// Create a fake Kubernetes client with a secret
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1alpha1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"client-secret": []byte("super-secret-value"),
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret).
		Build()

	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
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
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "https://api.example.com",
				Scopes:   []string{"read", "write"},
			},
		},
	}

	// Test the full flow: registry -> converter -> metadata -> resolve secrets
	registry := NewRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	require.NoError(t, err)

	// Convert to metadata
	metadata, err := converter.ConvertToMetadata(externalAuth)
	require.NoError(t, err)

	// Verify metadata before secret resolution
	assert.Equal(t, "https://auth.example.com/token", metadata["token_url"])
	assert.Equal(t, "test-client", metadata["client_id"])
	assert.Equal(t, "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET", metadata["client_secret_env"])
	assert.Equal(t, "https://api.example.com", metadata["audience"])
	assert.Equal(t, []string{"read", "write"}, metadata["scopes"])

	// Resolve secrets
	ctx := context.Background()
	metadata, err = converter.ResolveSecrets(ctx, externalAuth, k8sClient, "default", metadata)
	require.NoError(t, err)

	// Verify secret was resolved
	assert.Equal(t, "super-secret-value", metadata["client_secret"])
	assert.NotContains(t, metadata, "client_secret_env", "env var reference should be removed")
}

func TestHeaderInjectionConverter_Integration(t *testing.T) {
	t.Parallel()

	// Create a fake Kubernetes client with a secret
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1alpha1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-key-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"api-key": []byte("my-secret-api-key"),
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret).
		Build()

	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "header-auth",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
			HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
				HeaderName: "X-API-Key",
				ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "api-key-secret",
					Key:  "api-key",
				},
			},
		},
	}

	// Test the full flow: registry -> converter -> metadata -> resolve secrets
	registry := NewRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	require.NoError(t, err)

	// Convert to metadata
	metadata, err := converter.ConvertToMetadata(externalAuth)
	require.NoError(t, err)

	// Verify metadata before secret resolution
	assert.Equal(t, "X-API-Key", metadata["header_name"])
	assert.Equal(t, "TOOLHIVE_HEADER_INJECTION_VALUE", metadata["header_value_env"])

	// Resolve secrets
	ctx := context.Background()
	metadata, err = converter.ResolveSecrets(ctx, externalAuth, k8sClient, "default", metadata)
	require.NoError(t, err)

	// Verify secret was resolved
	assert.Equal(t, "my-secret-api-key", metadata["header_value"])
	assert.NotContains(t, metadata, "header_value_env", "env var reference should be removed")
}

func TestHeaderInjectionConverter_DirectValue(t *testing.T) {
	t.Parallel()

	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "header-auth-direct",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
			HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
				HeaderName: "X-Custom-Header",
				Value:      "direct-value",
			},
		},
	}

	registry := NewRegistry()
	converter, err := registry.GetConverter(externalAuth.Spec.Type)
	require.NoError(t, err)

	// Convert to metadata
	metadata, err := converter.ConvertToMetadata(externalAuth)
	require.NoError(t, err)

	// Verify metadata with direct value (no secret resolution needed)
	assert.Equal(t, "X-Custom-Header", metadata["header_name"])
	assert.Equal(t, "direct-value", metadata["header_value"])
	assert.NotContains(t, metadata, "header_value_env")

	// Resolve secrets should be no-op for direct values
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	metadata, err = converter.ResolveSecrets(ctx, externalAuth, k8sClient, "default", metadata)
	require.NoError(t, err)
	assert.Equal(t, "direct-value", metadata["header_value"])
}

func TestDiscoverAndResolveAuth(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1alpha1.AddToScheme(scheme)

	t.Run("no auth config ref returns empty", func(t *testing.T) {
		t.Parallel()
		k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		strategyType, metadata, err := DiscoverAndResolveAuth(
			context.Background(),
			nil, // No external auth config ref
			"default",
			k8sClient,
		)

		require.NoError(t, err)
		assert.Empty(t, strategyType)
		assert.Nil(t, metadata)
	})

	t.Run("discovers token exchange auth", func(t *testing.T) {
		t.Parallel()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"client-secret": []byte("super-secret-value"),
			},
		}

		externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
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
						Name: "test-secret",
						Key:  "client-secret",
					},
					Audience: "https://api.example.com",
				},
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret, externalAuth).
			Build()

		strategyType, metadata, err := DiscoverAndResolveAuth(
			context.Background(),
			&mcpv1alpha1.ExternalAuthConfigRef{Name: "test-auth"},
			"default",
			k8sClient,
		)

		require.NoError(t, err)
		assert.Equal(t, "token_exchange", strategyType)
		assert.Equal(t, "https://auth.example.com/token", metadata["token_url"])
		assert.Equal(t, "test-client", metadata["client_id"])
		assert.Equal(t, "super-secret-value", metadata["client_secret"])
		assert.Equal(t, "https://api.example.com", metadata["audience"])
	})

	t.Run("discovers header injection auth", func(t *testing.T) {
		t.Parallel()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "api-key-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"api-key": []byte("my-secret-api-key"),
			},
		}

		externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "header-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
					HeaderName: "X-API-Key",
					ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: "api-key-secret",
						Key:  "api-key",
					},
				},
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret, externalAuth).
			Build()

		strategyType, metadata, err := DiscoverAndResolveAuth(
			context.Background(),
			&mcpv1alpha1.ExternalAuthConfigRef{Name: "header-auth"},
			"default",
			k8sClient,
		)

		require.NoError(t, err)
		assert.Equal(t, "header_injection", strategyType)
		assert.Equal(t, "X-API-Key", metadata["header_name"])
		assert.Equal(t, "my-secret-api-key", metadata["header_value"])
	})

	t.Run("returns error when auth config not found", func(t *testing.T) {
		t.Parallel()
		k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		strategyType, metadata, err := DiscoverAndResolveAuth(
			context.Background(),
			&mcpv1alpha1.ExternalAuthConfigRef{Name: "nonexistent"},
			"default",
			k8sClient,
		)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get MCPExternalAuthConfig")
		assert.Empty(t, strategyType)
		assert.Nil(t, metadata)
	})
}

func TestConverters_ErrorCases(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1alpha1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()

	tests := []struct {
		name         string
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		errContains  string
	}{
		{
			name: "token exchange - secret not found",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "nonexistent-secret",
							Key:  "key",
						},
					},
				},
			},
			errContains: "failed to get client secret",
		},
		{
			name: "header injection - secret not found",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "nonexistent-secret",
							Key:  "key",
						},
					},
				},
			},
			errContains: "failed to get header value secret",
		},
		{
			name: "header injection - missing value and secret ref",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						// Neither Value nor ValueSecretRef set
					},
				},
			},
			errContains: "must specify either value or valueSecretRef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := NewRegistry()
			converter, err := registry.GetConverter(tt.externalAuth.Spec.Type)
			require.NoError(t, err)

			metadata, err := converter.ConvertToMetadata(tt.externalAuth)
			if err != nil {
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			// If metadata conversion succeeded, secret resolution should fail
			_, err = converter.ResolveSecrets(ctx, tt.externalAuth, k8sClient, "default", metadata)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}
