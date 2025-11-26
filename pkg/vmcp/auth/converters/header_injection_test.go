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

func TestHeaderInjectionConverter_StrategyType(t *testing.T) {
	t.Parallel()

	converter := &HeaderInjectionConverter{}
	assert.Equal(t, "header_injection", converter.StrategyType())
}

func TestHeaderInjectionConverter_ConvertToMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		wantMetadata map[string]any
		wantErr      bool
		errContains  string
	}{
		{
			name: "secret reference",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
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
			},
			wantMetadata: map[string]any{
				"header_name": "X-API-Key",
			},
			wantErr: false,
		},
		{
			name: "nil header injection config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:            mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: nil,
				},
			},
			wantErr:     true,
			errContains: "header injection config is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := &HeaderInjectionConverter{}
			metadata, err := converter.ConvertToMetadata(tt.externalAuth)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantMetadata, metadata)
		})
	}
}

func TestHeaderInjectionConverter_ResolveSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		secret       *corev1.Secret
		inputMeta    map[string]any
		wantMetadata map[string]any
		wantErr      bool
		errContains  string
	}{
		{
			name: "successful secret resolution",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
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
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "api-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"key": []byte("secret-value-123"),
				},
			},
			inputMeta: map[string]any{
				"header_name": "X-API-Key",
			},
			wantMetadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "secret-value-123",
			},
			wantErr: false,
		},
		{
			name: "missing secret",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
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
			},
			inputMeta: map[string]any{
				"header_name": "X-API-Key",
			},
			wantErr:     true,
			errContains: "failed to get secret",
		},
		{
			name: "missing key in secret",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
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
							Key:  "missing-key",
						},
					},
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "api-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"key": []byte("secret-value"),
				},
			},
			inputMeta: map[string]any{
				"header_name": "X-API-Key",
			},
			wantErr:     true,
			errContains: "does not contain key",
		},
		{
			name: "nil header injection config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:            mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: nil,
				},
			},
			inputMeta:   map[string]any{},
			wantErr:     true,
			errContains: "header injection config is nil",
		},
		{
			name: "nil valueSecretRef",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName:     "X-API-Key",
						ValueSecretRef: nil,
					},
				},
			},
			inputMeta:   map[string]any{},
			wantErr:     true,
			errContains: "valueSecretRef is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client with scheme
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = mcpv1alpha1.AddToScheme(scheme)

			// Add secret if provided
			var objects []runtime.Object
			if tt.secret != nil {
				objects = append(objects, tt.secret)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			converter := &HeaderInjectionConverter{}
			metadata, err := converter.ResolveSecrets(
				context.Background(),
				tt.externalAuth,
				fakeClient,
				tt.externalAuth.Namespace,
				tt.inputMeta,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantMetadata, metadata)
		})
	}
}
