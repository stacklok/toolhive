package resolver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

const testNamespace = "test-namespace"

// setupTestClient creates a fake Kubernetes client with the CRD schemes registered
func setupTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

func TestNewK8SAuthResolver(t *testing.T) {
	t.Parallel()

	k8sClient := setupTestClient(t)
	resolver := NewK8SAuthResolver(k8sClient, "my-namespace")

	require.NotNil(t, resolver)
	assert.Equal(t, "my-namespace", resolver.namespace)
	assert.Equal(t, k8sClient, resolver.k8sClient)
}

func TestK8SAuthResolver_ResolveExternalAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		refName        string
		objects        func() []client.Object
		wantErr        bool
		errContains    string
		validateResult func(t *testing.T, strategy *authtypes.BackendAuthStrategy)
	}{
		{
			name:        "empty ref name returns error",
			refName:     "",
			objects:     func() []client.Object { return nil },
			wantErr:     true,
			errContains: "external auth config ref name is empty",
		},
		{
			name:        "auth config not found returns error",
			refName:     "non-existent",
			objects:     func() []client.Object { return nil },
			wantErr:     true,
			errContains: "failed to resolve external auth config test-namespace/non-existent",
		},
		{
			name:    "token exchange success",
			refName: "token-exchange-config",
			objects: func() []client.Object {
				return []client.Object{
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{Name: "client-secret", Namespace: testNamespace},
						Data:       map[string][]byte{"secret": []byte("secret-value")},
					},
					&mcpv1alpha1.MCPExternalAuthConfig{
						ObjectMeta: metav1.ObjectMeta{Name: "token-exchange-config", Namespace: testNamespace},
						Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
							Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
							TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
								TokenURL: "https://auth.example.com/token",
								ClientID: "test-client",
								ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "client-secret",
									Key:  "secret",
								},
								Audience: "https://api.example.com",
								Scopes:   []string{"read", "write"},
							},
						},
					},
				}
			},
			wantErr: false,
			validateResult: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, authtypes.StrategyTypeTokenExchange, strategy.Type)
				require.NotNil(t, strategy.TokenExchange)
				assert.Equal(t, "https://auth.example.com/token", strategy.TokenExchange.TokenURL)
				assert.Equal(t, "test-client", strategy.TokenExchange.ClientID)
				assert.Equal(t, "secret-value", strategy.TokenExchange.ClientSecret)
			},
		},
		{
			name:    "header injection success",
			refName: "header-injection-config",
			objects: func() []client.Object {
				return []client.Object{
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{Name: "api-key", Namespace: testNamespace},
						Data:       map[string][]byte{"key": []byte("api-key-value")},
					},
					&mcpv1alpha1.MCPExternalAuthConfig{
						ObjectMeta: metav1.ObjectMeta{Name: "header-injection-config", Namespace: testNamespace},
						Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
							Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
							HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
								HeaderName: "X-API-Key",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "api-key",
									Key:  "key",
								},
							},
						},
					},
				}
			},
			wantErr: false,
			validateResult: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, authtypes.StrategyTypeHeaderInjection, strategy.Type)
				require.NotNil(t, strategy.HeaderInjection)
				assert.Equal(t, "X-API-Key", strategy.HeaderInjection.HeaderName)
				assert.Equal(t, "api-key-value", strategy.HeaderInjection.HeaderValue)
			},
		},
		{
			name:    "secret not found returns error",
			refName: "missing-secret-config",
			objects: func() []client.Object {
				return []client.Object{
					&mcpv1alpha1.MCPExternalAuthConfig{
						ObjectMeta: metav1.ObjectMeta{Name: "missing-secret-config", Namespace: testNamespace},
						Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
							Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
							TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
								TokenURL: "https://auth.example.com/token",
								ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "non-existent-secret",
									Key:  "secret",
								},
							},
						},
					},
				}
			},
			wantErr:     true,
			errContains: "failed to resolve external auth config test-namespace/missing-secret-config",
		},
		{
			name:    "wrong namespace returns error",
			refName: "other-ns-config",
			objects: func() []client.Object {
				return []client.Object{
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{Name: "other-ns-secret", Namespace: "other-namespace"},
						Data:       map[string][]byte{"key": []byte("value")},
					},
					&mcpv1alpha1.MCPExternalAuthConfig{
						ObjectMeta: metav1.ObjectMeta{Name: "other-ns-config", Namespace: "other-namespace"},
						Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
							Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
							HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
								HeaderName: "X-API-Key",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "other-ns-secret",
									Key:  "key",
								},
							},
						},
					},
				}
			},
			wantErr:     true,
			errContains: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var objects []client.Object
			if tc.objects != nil {
				objects = tc.objects()
			}

			k8sClient := setupTestClient(t, objects...)
			resolver := NewK8SAuthResolver(k8sClient, testNamespace)

			strategy, err := resolver.ResolveExternalAuthConfig(context.Background(), tc.refName)

			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, strategy)
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, strategy)
				if tc.validateResult != nil {
					tc.validateResult(t, strategy)
				}
			}
		})
	}
}
