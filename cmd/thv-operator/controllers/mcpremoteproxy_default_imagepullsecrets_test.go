// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
)

// TestMCPRemoteProxy_DefaultImagePullSecrets verifies that the merge of
// cluster-wide chart defaults with spec.resourceOverrides.proxyDeployment.imagePullSecrets
// reaches both the proxy-runner ServiceAccount and the Deployment PodSpec.
//
// The Merge precedence rule is exhaustively covered in
// imagepullsecrets/defaults_test.go::TestDefaultsMerge; the cases here exist
// only to prove the wiring is correct end-to-end.
func TestMCPRemoteProxy_DefaultImagePullSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		defaults    []string
		crSecrets   []corev1.LocalObjectReference
		wantSecrets []corev1.LocalObjectReference
	}{
		{
			name:     "merged defaults+CR with name collision reach SA and Deployment",
			defaults: []string{"shared", "chart-only"},
			crSecrets: []corev1.LocalObjectReference{
				{Name: "shared"},
			},
			wantSecrets: []corev1.LocalObjectReference{
				{Name: "shared"},
				{Name: "chart-only"},
			},
		},
		{
			name:        "no defaults and no CR yields empty fields",
			defaults:    nil,
			crSecrets:   nil,
			wantSecrets: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy := &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-pullsecrets-proxy",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ProxyPort: 8080,
				},
			}
			if tt.crSecrets != nil {
				proxy.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						ImagePullSecrets: tt.crSecrets,
					},
				}
			}

			scheme := createRunConfigTestScheme()
			_ = rbacv1.AddToScheme(scheme)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(proxy).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client:                   fakeClient,
				Scheme:                   scheme,
				PlatformDetector:         ctrlutil.NewSharedPlatformDetector(),
				ImagePullSecretsDefaults: imagepullsecrets.NewDefaults(tt.defaults),
			}

			require.NoError(t, reconciler.ensureRBACResources(t.Context(), proxy))

			sa := &corev1.ServiceAccount{}
			require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{
				Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
				Namespace: proxy.Namespace,
			}, sa))
			assert.Equal(t, tt.wantSecrets, sa.ImagePullSecrets,
				"proxy runner SA ImagePullSecrets must reflect merged defaults+CR")

			dep := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
			require.NotNil(t, dep)
			assert.Equal(t, tt.wantSecrets, dep.Spec.Template.Spec.ImagePullSecrets,
				"proxy runner Deployment ImagePullSecrets must reflect merged defaults+CR")
		})
	}
}
