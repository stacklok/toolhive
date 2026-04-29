// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
)

// TestEnsureRBACResources_DefaultImagePullSecrets verifies that cluster-wide
// chart defaults are merged with per-CR ImagePullSecrets when reconciling
// the proxy-runner ServiceAccount and the MCP server ServiceAccount.
//
// The Merge precedence rule itself is exhaustively covered in
// imagepullsecrets/defaults_test.go::TestDefaultsMerge. The cases here exist
// only to prove that the merged slice actually reaches the constructed
// ServiceAccount fields, so we keep this table to the minimum that exercises
// both ends of the wiring (overlap + empty).
func TestEnsureRBACResources_DefaultImagePullSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		defaults    []string
		crSecrets   []corev1.LocalObjectReference
		wantSecrets []corev1.LocalObjectReference
	}{
		{
			// Overlap proves merge precedence reaches the SA: shared is
			// deduplicated, chart-only is appended after the CR entry.
			name:     "merged defaults+CR with name collision reach ServiceAccount",
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
			name:        "no defaults and no CR yields empty ServiceAccount field",
			defaults:    nil,
			crSecrets:   nil,
			wantSecrets: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tc := setupTest("test-server-default-pull-secrets", "default")
			tc.reconciler.ImagePullSecretsDefaults = imagepullsecrets.NewDefaults(tt.defaults)

			if tt.crSecrets != nil {
				tc.mcpServer.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						ImagePullSecrets: tt.crSecrets,
					},
				}
			}

			require.NoError(t, tc.ensureRBACResources())

			// Proxy-runner ServiceAccount.
			sa := &corev1.ServiceAccount{}
			require.NoError(t, tc.client.Get(t.Context(), types.NamespacedName{
				Name:      tc.proxyRunnerNameForRBAC,
				Namespace: tc.mcpServer.Namespace,
			}, sa))
			assert.Equal(t, tt.wantSecrets, sa.ImagePullSecrets,
				"proxy runner SA ImagePullSecrets must reflect merged defaults+CR")

			// MCP server ServiceAccount (auto-created when CR doesn't supply one).
			mcpSA := &corev1.ServiceAccount{}
			require.NoError(t, tc.client.Get(t.Context(), types.NamespacedName{
				Name:      mcpServerServiceAccountName(tc.mcpServer.Name),
				Namespace: tc.mcpServer.Namespace,
			}, mcpSA))
			assert.Equal(t, tt.wantSecrets, mcpSA.ImagePullSecrets,
				"MCP server SA ImagePullSecrets must reflect merged defaults+CR")
		})
	}
}

// TestDeploymentNeedsUpdate_DefaultImagePullSecrets is a regression test for a
// bug where deploymentNeedsUpdate compared the live Deployment's
// ImagePullSecrets against only the per-CR slice while the construction site
// applied the chart-default-merged slice. With chart defaults configured the
// comparison was always unequal, so every reconcile returned needsUpdate=true
// and the controller looped forever. The fix routes both sites through
// imagePullSecretsForMCPServer.
func TestDeploymentNeedsUpdate_DefaultImagePullSecrets(t *testing.T) {
	t.Parallel()

	tc := setupTest("test-server-drift-pull-secrets", "default")
	tc.reconciler.ImagePullSecretsDefaults = imagepullsecrets.NewDefaults([]string{"chart-default"})

	dep := tc.reconciler.deploymentForMCPServer(t.Context(), tc.mcpServer, "test-checksum")
	require.NotNil(t, dep)

	assert.False(t, tc.reconciler.deploymentNeedsUpdate(t.Context(), dep, tc.mcpServer, "test-checksum"),
		"freshly built Deployment must not be flagged for update by drift detection")
}

// TestDeploymentForMCPServer_DefaultImagePullSecrets verifies that cluster-wide
// chart defaults are merged with per-CR ImagePullSecrets when constructing the
// proxy-runner Deployment PodSpec. See the comment on
// TestEnsureRBACResources_DefaultImagePullSecrets for why this table is small.
func TestDeploymentForMCPServer_DefaultImagePullSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		defaults    []string
		crSecrets   []corev1.LocalObjectReference
		wantSecrets []corev1.LocalObjectReference
	}{
		{
			name:     "merged defaults+CR reach Deployment PodSpec",
			defaults: []string{"chart-default"},
			crSecrets: []corev1.LocalObjectReference{
				{Name: "cr-secret"},
			},
			wantSecrets: []corev1.LocalObjectReference{
				{Name: "cr-secret"},
				{Name: "chart-default"},
			},
		},
		{
			name:        "no defaults and no CR yields nil PodSpec field",
			defaults:    nil,
			crSecrets:   nil,
			wantSecrets: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tc := setupTest("test-server-default-pull-secrets-dep", "default")
			tc.reconciler.ImagePullSecretsDefaults = imagepullsecrets.NewDefaults(tt.defaults)

			if tt.crSecrets != nil {
				tc.mcpServer.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						ImagePullSecrets: tt.crSecrets,
					},
				}
			}

			dep := tc.reconciler.deploymentForMCPServer(t.Context(), tc.mcpServer, "test-checksum")
			require.NotNil(t, dep)
			assert.Equal(t, tt.wantSecrets, dep.Spec.Template.Spec.ImagePullSecrets,
				"proxy runner Deployment ImagePullSecrets must reflect merged defaults+CR")
		})
	}
}
