// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TestMCPRemoteProxyValidateCABundleRefStatusUpdateError tests that
// validateCABundleRef handles Status().Update errors gracefully.
func TestMCPRemoteProxyValidateCABundleRefStatusUpdateError(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ca-status-error",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			OIDCConfig: mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer:   "https://auth.example.com",
					Audience: "mcp-proxy",
					CABundleRef: &mcpv1alpha1.CABundleSource{
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "ca-bundle"},
							Key:                  "ca.crt",
						},
					},
				},
			},
		},
	}

	caBundleCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ca-bundle",
			Namespace: "default",
		},
		Data: map[string]string{
			"ca.crt": "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		},
	}

	scheme := createRunConfigTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy, caBundleCM).
		WithStatusSubresource(proxy).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				// Fail status updates to exercise the error path in updateCABundleStatusForProxy
				return fmt.Errorf("simulated status update failure")
			},
		}).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Should not panic even when status update fails
	reconciler.validateCABundleRef(context.TODO(), proxy)

	// The condition should still be set in-memory despite the status update failure
	cond := findCondition(proxy.Status.Conditions, mcpv1alpha1.ConditionTypeMCPRemoteProxyCABundleRefValidated)
	assert.NotNil(t, cond, "CABundleRefValidated condition should be set in-memory")
}

// TestMCPRemoteProxyDeploymentMetadataMapIsSubset tests that deploymentMetadataNeedsUpdate
// uses subset checking for annotations (not exact equality).
func TestMCPRemoteProxyDeploymentMetadataMapIsSubset(t *testing.T) {
	t.Parallel()

	r := &MCPRemoteProxyReconciler{}

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "subset-test",
			Namespace: "default",
		},
	}

	expectedLabels := labelsForMCPRemoteProxy(proxy.Name)

	tests := []struct {
		name        string
		annotations map[string]string
		needsUpdate bool
	}{
		{
			name:        "extra annotations should not trigger update",
			annotations: map[string]string{"extra-key": "extra-value"},
			needsUpdate: false,
		},
		{
			name:        "no annotations should not trigger update",
			annotations: map[string]string{},
			needsUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      expectedLabels,
					Annotations: tt.annotations,
				},
			}
			result := r.deploymentMetadataNeedsUpdate(deployment, proxy)
			assert.Equal(t, tt.needsUpdate, result)
		})
	}
}

// findCondition is a helper to find a condition by type
func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// TestMCPRemoteProxyValidateSpecExtended covers additional validateSpec branches
// not exercised by TestMCPRemoteProxyValidateSpec in mcpremoteproxy_controller_test.go.
func TestMCPRemoteProxyValidateSpecExtended(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxy       *mcpv1alpha1.MCPRemoteProxy
		expectError bool
		errContains string
	}{
		{
			name: "inline OIDC with unsupported scheme issuer rejected",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "inline-ftp-issuer",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ProxyPort: 8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "ftp://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
				},
			},
			expectError: true,
			errContains: "unsupported scheme",
		},
		{
			name: "inline OIDC with HTTP issuer rejected",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "inline-http-issuer",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ProxyPort: 8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "http://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
				},
			},
			expectError: true,
			errContains: "HTTP scheme",
		},
		{
			name: "inline OIDC with invalid JWKS URL rejected",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "inline-bad-jwks",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ProxyPort: 8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
							JWKSURL:  "not-a-url",
						},
					},
				},
			},
			expectError: true,
			errContains: "JWKS URL",
		},
		{
			name: "authz ConfigMap reference not found",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "authz-cm-missing",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ProxyPort: 8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
							Name: "non-existent-cm",
						},
					},
				},
			},
			expectError: true,
			errContains: "not found",
		},
		{
			name: "header secret reference not found",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "header-secret-missing",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ProxyPort: 8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					HeaderForward: &mcpv1alpha1.HeaderForwardConfig{
						AddHeadersFromSecret: []mcpv1alpha1.HeaderFromSecret{
							{
								HeaderName: "X-API-Key",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "non-existent-secret",
									Key:  "api-key",
								},
							},
						},
					},
				},
			},
			expectError: true,
			errContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.proxy).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.validateSpec(context.TODO(), tt.proxy)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestMCPRemoteProxyValidateOIDCIssuerURL tests validateOIDCIssuerURL edge cases
// where the OIDC config struct fields are nil, exercising fallthrough to return nil.
func TestMCPRemoteProxyValidateOIDCIssuerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxy       *mcpv1alpha1.MCPRemoteProxy
		expectError bool
	}{
		{
			name: "inline type with nil Inline struct",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type:   mcpv1alpha1.OIDCConfigTypeInline,
						Inline: nil,
					},
				},
			},
			expectError: false,
		},
		{
			name: "kubernetes type with nil Kubernetes struct",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type:       mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: nil,
					},
				},
			},
			expectError: false,
		},
		{
			name: "kubernetes type with empty issuer",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: &mcpv1alpha1.KubernetesOIDCConfig{
							Issuer: "",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "unknown OIDC config type",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: "unknown",
					},
				},
			},
			expectError: false,
		},
	}

	r := &MCPRemoteProxyReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := r.validateOIDCIssuerURL(tt.proxy)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestMCPRemoteProxyValidateJWKSURL tests validateJWKSURL edge cases
// where the OIDC config struct fields are nil, exercising fallthrough to return nil.
func TestMCPRemoteProxyValidateJWKSURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxy       *mcpv1alpha1.MCPRemoteProxy
		expectError bool
	}{
		{
			name: "inline type with nil Inline struct",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type:   mcpv1alpha1.OIDCConfigTypeInline,
						Inline: nil,
					},
				},
			},
			expectError: false,
		},
		{
			name: "kubernetes type with nil Kubernetes struct",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type:       mcpv1alpha1.OIDCConfigTypeKubernetes,
						Kubernetes: nil,
					},
				},
			},
			expectError: false,
		},
		{
			name: "unknown OIDC config type",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: "unknown",
					},
				},
			},
			expectError: false,
		},
	}

	r := &MCPRemoteProxyReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := r.validateJWKSURL(tt.proxy)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestMCPRemoteProxyValidateK8sRefs tests K8s reference validation in detail.
func TestMCPRemoteProxyValidateK8sRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		proxy            *mcpv1alpha1.MCPRemoteProxy
		objects          []runtime.Object
		interceptorFuncs *interceptor.Funcs
		expectError      bool
		errContains      string
	}{
		{
			name: "no authz config or header forward",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-refs",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
			},
			expectError: false,
		},
		{
			name: "authz ConfigMap exists",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "authz-cm-ok",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
							Name: "my-authz-cm",
						},
					},
				},
			},
			objects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-authz-cm",
						Namespace: "default",
					},
				},
			},
			expectError: false,
		},
		{
			name: "authz ConfigMap not found",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "authz-cm-missing",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
							Name: "missing-cm",
						},
					},
				},
			},
			expectError: true,
			errContains: "authorization ConfigMap",
		},
		{
			name: "authz ConfigMap fetch error",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "authz-cm-error",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
							Name: "error-cm",
						},
					},
				},
			},
			interceptorFuncs: &interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.ConfigMap); ok {
						return fmt.Errorf("simulated API server error")
					}
					return c.Get(ctx, key, obj, opts...)
				},
			},
			expectError: true,
			errContains: "failed to fetch authorization ConfigMap",
		},
		{
			name: "header secret exists",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "header-secret-ok",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					HeaderForward: &mcpv1alpha1.HeaderForwardConfig{
						AddHeadersFromSecret: []mcpv1alpha1.HeaderFromSecret{
							{
								HeaderName: "X-API-Key",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "my-secret",
									Key:  "api-key",
								},
							},
						},
					},
				},
			},
			objects: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-secret",
						Namespace: "default",
					},
				},
			},
			expectError: false,
		},
		{
			name: "header secret not found",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "header-secret-missing",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					HeaderForward: &mcpv1alpha1.HeaderForwardConfig{
						AddHeadersFromSecret: []mcpv1alpha1.HeaderFromSecret{
							{
								HeaderName: "X-API-Key",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "missing-secret",
									Key:  "api-key",
								},
							},
						},
					},
				},
			},
			expectError: true,
			errContains: "not found",
		},
		{
			name: "header secret fetch error",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "header-secret-error",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					HeaderForward: &mcpv1alpha1.HeaderForwardConfig{
						AddHeadersFromSecret: []mcpv1alpha1.HeaderFromSecret{
							{
								HeaderName: "X-API-Key",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "error-secret",
									Key:  "api-key",
								},
							},
						},
					},
				},
			},
			interceptorFuncs: &interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.Secret); ok {
						return fmt.Errorf("simulated API server error")
					}
					return c.Get(ctx, key, obj, opts...)
				},
			},
			expectError: true,
			errContains: "failed to fetch secret",
		},
		{
			name: "header with nil ValueSecretRef is skipped",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "header-nil-ref",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					HeaderForward: &mcpv1alpha1.HeaderForwardConfig{
						AddHeadersFromSecret: []mcpv1alpha1.HeaderFromSecret{
							{
								HeaderName:     "X-Static",
								ValueSecretRef: nil,
							},
						},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := append([]runtime.Object{tt.proxy}, tt.objects...)

			builder := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...)
			if tt.interceptorFuncs != nil {
				builder = builder.WithInterceptorFuncs(*tt.interceptorFuncs)
			}
			fakeClient := builder.Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.validateK8sRefs(context.TODO(), tt.proxy)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
