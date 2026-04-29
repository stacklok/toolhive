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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// TestVirtualMCPServer_DefaultImagePullSecrets verifies that the merge of
// cluster-wide chart defaults with vmcp.Spec.ImagePullSecrets reaches the
// vMCP Deployment PodSpec, the ServiceAccount, and the
// imagePullRefsHashAnnotation that drives drift detection.
//
// The Merge precedence rule itself is exhaustively covered in
// imagepullsecrets/defaults_test.go::TestDefaultsMerge.
func TestVirtualMCPServer_DefaultImagePullSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		defaults    []string
		crSecrets   []corev1.LocalObjectReference
		wantSecrets []corev1.LocalObjectReference
	}{
		{
			name:     "merged defaults+CR with name collision reach Deployment, SA, and hash",
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
			name:        "no defaults and no CR yields empty fields and no annotation",
			defaults:    nil,
			crSecrets:   nil,
			wantSecrets: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-pullsecrets-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef:         &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					ImagePullSecrets: tt.crSecrets,
				},
			}

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, rbacv1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(vmcp).
				Build()

			r := &VirtualMCPServerReconciler{
				Client:                   fakeClient,
				Scheme:                   scheme,
				PlatformDetector:         ctrlutil.NewSharedPlatformDetector(),
				ImagePullSecretsDefaults: imagepullsecrets.NewDefaults(tt.defaults),
			}

			// Verify Deployment PodSpec carries the merged list.
			dep := r.deploymentForVirtualMCPServer(t.Context(), vmcp, "test-checksum", nil, []workloads.TypedWorkload{})
			require.NotNil(t, dep)
			assert.Equal(t, tt.wantSecrets, dep.Spec.Template.Spec.ImagePullSecrets,
				"vMCP Deployment ImagePullSecrets must reflect merged defaults+CR")

			// Verify the drift-detection annotation is present iff the
			// merged list is non-empty, and matches the hash of the merged list.
			expectedHash, err := imagePullSecretsHash(tt.wantSecrets)
			require.NoError(t, err)
			gotHash, present := dep.Annotations[imagePullRefsHashAnnotation]
			if expectedHash == "" {
				assert.False(t, present,
					"imagePullRefsHashAnnotation must be absent when merged list is empty")
			} else {
				assert.True(t, present, "imagePullRefsHashAnnotation must be set")
				assert.Equal(t, expectedHash, gotHash,
					"hash annotation must match hash of the merged list")
			}

			// Confirm drift detection treats this freshly-built Deployment as
			// up-to-date — i.e. the annotation matches the desired-state hash
			// computed from the same merge. Without this, every reconcile
			// would loop.
			assert.False(t, r.imagePullSecretsNeedsUpdate(t.Context(), dep, vmcp),
				"freshly built Deployment must not be flagged as needing update")

			// Verify the ServiceAccount also carries the merged list.
			require.NoError(t, r.ensureRBACResources(t.Context(), vmcp))
			sa := &corev1.ServiceAccount{}
			require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{
				Name:      r.serviceAccountNameForVmcp(vmcp),
				Namespace: vmcp.Namespace,
			}, sa))
			assert.Equal(t, tt.wantSecrets, sa.ImagePullSecrets,
				"vMCP SA ImagePullSecrets must reflect merged defaults+CR")
		})
	}
}
