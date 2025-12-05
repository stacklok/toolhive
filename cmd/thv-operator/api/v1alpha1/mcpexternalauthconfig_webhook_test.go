package v1alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPExternalAuthConfig_ValidateCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *MCPExternalAuthConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid unauthenticated",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-unauthenticated",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
				},
			},
			expectError: false,
		},
		{
			name: "unauthenticated with tokenExchange should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
					},
				},
			},
			expectError: true,
			errorMsg:    "tokenExchange must not be set when type is 'unauthenticated'",
		},
		{
			name: "unauthenticated with headerInjection should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "Authorization",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "headerInjection must not be set when type is 'unauthenticated'",
		},
		{
			name: "valid tokenExchange",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tokenexchange",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
				},
			},
			expectError: false,
		},
		{
			name: "tokenExchange without config should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeTokenExchange,
				},
			},
			expectError: true,
			errorMsg:    "tokenExchange configuration is required when type is 'tokenExchange'",
		},
		{
			name: "tokenExchange with headerInjection should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "Authorization",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "headerInjection must not be set when type is 'tokenExchange'",
		},
		{
			name: "valid headerInjection",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-headerinjection",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeHeaderInjection,
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &SecretKeyRef{
							Name: "api-key-secret",
							Key:  "api-key",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "headerInjection without config should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeHeaderInjection,
				},
			},
			expectError: true,
			errorMsg:    "headerInjection configuration is required when type is 'headerInjection'",
		},
		{
			name: "headerInjection with tokenExchange should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeHeaderInjection,
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
					},
				},
			},
			expectError: true,
			errorMsg:    "tokenExchange must not be set when type is 'headerInjection'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			warnings, err := tt.config.ValidateCreate(context.Background(), tt.config)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}

			// Warnings should always be nil for now
			assert.Nil(t, warnings)
		})
	}
}

func TestMCPExternalAuthConfig_ValidateUpdate(t *testing.T) {
	t.Parallel()

	config := &MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unauthenticated",
			Namespace: "default",
		},
		Spec: MCPExternalAuthConfigSpec{
			Type: ExternalAuthTypeUnauthenticated,
		},
	}

	// ValidateUpdate should use the same logic as ValidateCreate
	warnings, err := config.ValidateUpdate(context.Background(), nil, config)
	require.NoError(t, err)
	assert.Nil(t, warnings)

	// Test invalid update
	invalidConfig := &MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-invalid",
			Namespace: "default",
		},
		Spec: MCPExternalAuthConfigSpec{
			Type: ExternalAuthTypeUnauthenticated,
			TokenExchange: &TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
			},
		},
	}

	warnings, err = invalidConfig.ValidateUpdate(context.Background(), nil, invalidConfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tokenExchange must not be set when type is 'unauthenticated'")
	assert.Nil(t, warnings)
}

func TestMCPExternalAuthConfig_ValidateDelete(t *testing.T) {
	t.Parallel()

	config := &MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unauthenticated",
			Namespace: "default",
		},
		Spec: MCPExternalAuthConfigSpec{
			Type: ExternalAuthTypeUnauthenticated,
		},
	}

	// ValidateDelete should always succeed
	warnings, err := config.ValidateDelete(context.Background(), config)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}
