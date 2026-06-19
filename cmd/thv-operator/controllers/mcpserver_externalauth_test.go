// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerReconciler_handleExternalAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		mcpServer          *mcpv1beta1.MCPServer
		externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig
		expectError        bool
		expectHash         string
		expectHashCleared  bool
	}{
		{
			name: "no external auth config reference",
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
			),
			expectError:       false,
			expectHash:        "",
			expectHashCleared: false,
		},
		{
			name: "external auth config reference exists",
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithExternalAuthConfigRef("test-config"),
			),
			externalAuthConfig: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
					},
				},
				Status: mcpv1beta1.MCPExternalAuthConfigStatus{
					ConfigHash: "test-hash-123",
				},
			},
			expectError: false,
			expectHash:  "test-hash-123",
		},
		{
			name: "external auth config not found",
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithExternalAuthConfigRef("non-existent-config"),
			),
			expectError: true,
		},
		{
			name: "external auth config hash changed",
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithExternalAuthConfigRef("test-config"),
				v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{ExternalAuthConfigHash: "old-hash"}),
			),
			externalAuthConfig: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "new-audience", // Changed config
					},
				},
				Status: mcpv1beta1.MCPExternalAuthConfigStatus{
					ConfigHash: "new-hash-456",
				},
			},
			expectError: false,
			expectHash:  "new-hash-456",
		},
		{
			name: "clear hash when reference is removed",
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				// No ExternalAuthConfigRef (was removed)
				v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{ExternalAuthConfigHash: "old-hash-to-clear"}),
			),
			expectError:       false,
			expectHash:        "",
			expectHashCleared: true,
		},
		{
			name: "embedded auth server with multiple upstreams rejected",
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithExternalAuthConfigRef("multi-upstream-config"),
			),
			externalAuthConfig: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-upstream-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
							{Name: "github", Type: mcpv1beta1.UpstreamProviderTypeOIDC, OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "id1"}},
							{Name: "google", Type: mcpv1beta1.UpstreamProviderTypeOIDC, OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://accounts.google.com", ClientID: "id2"}},
						},
					},
				},
				Status: mcpv1beta1.MCPExternalAuthConfigStatus{ConfigHash: "multi-hash"},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			scheme := testutil.NewScheme(t)

			// Build objects for fake client
			objs := []runtime.Object{tt.mcpServer}
			if tt.externalAuthConfig != nil {
				objs = append(objs, tt.externalAuthConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&mcpv1beta1.MCPServer{}).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			// Execute
			err := reconciler.handleExternalAuthConfig(ctx, tt.mcpServer)

			// Assert
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				if tt.expectHash != "" {
					assert.Equal(t, tt.expectHash, tt.mcpServer.Status.ExternalAuthConfigHash,
						"Hash should be updated in status")
				}

				if tt.expectHashCleared {
					assert.Empty(t, tt.mcpServer.Status.ExternalAuthConfigHash,
						"Hash should be cleared from status")
				}
			}
		})
	}
}

func TestMCPServerReconciler_handleExternalAuthConfig_SameNamespace(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scheme := testutil.NewScheme(t)

	// External auth config in a different namespace
	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "other-namespace",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
		Status: mcpv1beta1.MCPExternalAuthConfigStatus{
			ConfigHash: "test-hash-123",
		},
	}

	// MCPServer in different namespace
	mcpServer := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.WithImage("test-image"),
		// References config in same namespace (default)
		v1beta1test.WithExternalAuthConfigRef("test-config"),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	// Execute - should fail because config is in different namespace
	err := reconciler.handleExternalAuthConfig(ctx, mcpServer)

	// Assert - should get an error because config is not in same namespace
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMCPServerReconciler_handleExternalAuthConfig_HashUpdateTrigger(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scheme := testutil.NewScheme(t)

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
		Status: mcpv1beta1.MCPExternalAuthConfigStatus{
			ConfigHash: "initial-hash",
		},
	}

	mcpServer := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("test-config"),
		v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{ExternalAuthConfigHash: "initial-hash"}),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}, &mcpv1beta1.MCPExternalAuthConfig{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	// First call - hash is the same, no update needed
	err := reconciler.handleExternalAuthConfig(ctx, mcpServer)
	assert.NoError(t, err)
	assert.Equal(t, "initial-hash", mcpServer.Status.ExternalAuthConfigHash)

	// Simulate external auth config change - need to get the object first
	var updatedConfig mcpv1beta1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, client.ObjectKey{Name: "test-config", Namespace: "default"}, &updatedConfig)
	require.NoError(t, err)

	updatedConfig.Status.ConfigHash = "updated-hash"
	err = fakeClient.Status().Update(ctx, &updatedConfig)
	require.NoError(t, err)

	// Second call - hash changed, should update
	err = reconciler.handleExternalAuthConfig(ctx, mcpServer)
	assert.NoError(t, err)
	assert.Equal(t, "updated-hash", mcpServer.Status.ExternalAuthConfigHash,
		"Hash should be updated to new value")
}

func TestMCPServerReconciler_handleExternalAuthConfig_NoHashInConfig(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scheme := testutil.NewScheme(t)

	// External auth config without hash in status
	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
		Status: mcpv1beta1.MCPExternalAuthConfigStatus{
			// ConfigHash is empty
		},
	}

	mcpServer := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("test-config"),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	// Execute
	err := reconciler.handleExternalAuthConfig(ctx, mcpServer)

	// Assert - should succeed, but hash will be empty
	assert.NoError(t, err)
	assert.Empty(t, mcpServer.Status.ExternalAuthConfigHash,
		"Hash should be empty when external auth config has no hash")
}

// TestMCPServerReconciler_handleExternalAuthConfig_MirrorsInvalidCondition verifies
// the Status Condition Parity mirror added for #5347: when the referenced
// MCPExternalAuthConfig has Status.Conditions[Valid]=False (e.g. an obo-typed
// config that the default OBO handler rejected with Reason=EnterpriseRequired
// in upstream-only builds), the MCPServer reconciler must surface a parallel
// ExternalAuthConfigValidated=False condition that carries the same reason and
// message — instead of silently propagating the dispatch error through the
// generic phase-Failed path.
func TestMCPServerReconciler_handleExternalAuthConfig_MirrorsInvalidCondition(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	const (
		authName  = "obo-config"
		namespace = "default"
		reason    = mcpv1beta1.ConditionReasonEnterpriseRequired
		message   = "on-behalf-of (OBO) external auth type requires an enterprise build"
	)

	tests := []struct {
		name                   string
		sourceValid            *metav1.Condition
		preexisting            []metav1.Condition
		wantMirrored           bool
		wantReason             string
		wantMessage            string
		wantPreexistingCleared bool
	}{
		{
			name: "source Valid=False/EnterpriseRequired is mirrored",
			sourceValid: &metav1.Condition{
				Type:    mcpv1beta1.ConditionTypeValid,
				Status:  metav1.ConditionFalse,
				Reason:  reason,
				Message: message,
			},
			wantMirrored: true,
			wantReason:   reason,
			wantMessage:  message,
		},
		{
			name: "source Valid=False/InvalidConfig is mirrored verbatim",
			sourceValid: &metav1.Condition{
				Type:    mcpv1beta1.ConditionTypeValid,
				Status:  metav1.ConditionFalse,
				Reason:  mcpv1beta1.ConditionReasonInvalidConfig,
				Message: "custom OBO handler rejected the spec",
			},
			wantMirrored: true,
			wantReason:   mcpv1beta1.ConditionReasonInvalidConfig,
			wantMessage:  "custom OBO handler rejected the spec",
		},
		{
			name: "source Valid=True does not produce a mirror",
			sourceValid: &metav1.Condition{
				Type:   mcpv1beta1.ConditionTypeValid,
				Status: metav1.ConditionTrue,
				Reason: "ValidationSucceeded",
			},
			wantMirrored: false,
		},
		{
			name:         "source with no Valid condition does not produce a mirror",
			sourceValid:  nil,
			wantMirrored: false,
		},
		{
			// Regression guard: once the source heals, a stale mirror left from
			// a previous reconcile must be cleared so the condition does not
			// outlive its cause. Without the heal path, the False sticks
			// forever even after the user fixes the spec.
			name: "stale mirror is cleared when source has healed",
			sourceValid: &metav1.Condition{
				Type:   mcpv1beta1.ConditionTypeValid,
				Status: metav1.ConditionTrue,
				Reason: "ValidationSucceeded",
			},
			preexisting: []metav1.Condition{{
				Type:    mcpv1beta1.ConditionTypeExternalAuthConfigValidated,
				Status:  metav1.ConditionFalse,
				Reason:  mcpv1beta1.ConditionReasonEnterpriseRequired,
				Message: "stale mirror from a previous reconcile",
			}},
			wantMirrored:           false,
			wantPreexistingCleared: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)

			externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: authName, Namespace: namespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeOBO,
					OBO:  &mcpv1beta1.OBOConfig{},
				},
			}
			if tt.sourceValid != nil {
				externalAuthConfig.Status.Conditions = []metav1.Condition{*tt.sourceValid}
			}

			const serverGeneration int64 = 11
			mcpServer := v1beta1test.NewMCPServer("test-server", namespace,
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithExternalAuthConfigRef(authName),
				v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{Conditions: tt.preexisting}),
				v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
					m.Generation = serverGeneration
				}),
			)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(externalAuthConfig, mcpServer).
				WithStatusSubresource(&mcpv1beta1.MCPServer{}).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			err := reconciler.handleExternalAuthConfig(ctx, mcpServer)

			cond := meta.FindStatusCondition(
				mcpServer.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated)

			if !tt.wantMirrored {
				assert.NoError(t, err, "no error expected when source is valid")
				if tt.wantPreexistingCleared {
					assert.Nil(t, cond, "stale mirror condition must be cleared once source has healed")
				} else {
					assert.Nil(t, cond, "no mirror condition expected when source is valid")
				}
				return
			}

			require.Error(t, err, "handler must surface the mirrored failure so callers mark Phase=Failed")
			require.NotNil(t, cond, "mirror condition must be set on MCPServer.Status.Conditions")
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
			assert.Equal(t, tt.wantMessage, cond.Message)
			// F9: the mirror must stamp ObservedGeneration with the consumer's
			// Generation (not the source's), so the condition reflects the
			// generation of the spec it was computed for.
			assert.Equal(t, serverGeneration, cond.ObservedGeneration,
				"Condition.ObservedGeneration must match MCPServer.Generation")
		})
	}
}

// TestMCPServerReconciler_handleExternalAuthConfig_ClearsMirrorOnRefRemoved is
// a regression guard for the no-ref heal path: handleExternalAuthConfig's
// early-return branch (m.Spec.ExternalAuthConfigRef == nil) never reaches the
// mirror helper, so if a stale ExternalAuthConfigValidated=False sits on the
// status from a previous reconcile, the user removing the ref must still
// clear it — otherwise the condition outlives its cause.
func TestMCPServerReconciler_handleExternalAuthConfig_ClearsMirrorOnRefRemoved(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	scheme := testutil.NewScheme(t)

	mcpServer := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.WithImage("test-image"), // no ExternalAuthConfigRef
		v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{
			Conditions: []metav1.Condition{{
				Type:    mcpv1beta1.ConditionTypeExternalAuthConfigValidated,
				Status:  metav1.ConditionFalse,
				Reason:  mcpv1beta1.ConditionReasonEnterpriseRequired,
				Message: "stale mirror from when the ref pointed at an obo-typed config",
			}},
		}),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	require.NoError(t, reconciler.handleExternalAuthConfig(ctx, mcpServer))
	assert.Nil(t,
		meta.FindStatusCondition(mcpServer.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated),
		"stale mirror must be cleared in the no-ref early-return branch")
}

// TestMCPServerReconciler_handleExternalAuthConfig_ClearsMirrorOnSourceNotFound
// is the second F10 regression guard: when the referenced MCPExternalAuthConfig
// is deleted between reconciles, handleExternalAuthConfig returns the lookup
// error and previously left any stale mirror in place. The fix clears the
// mirror in both NotFound branches so the condition does not outlive the
// source.
func TestMCPServerReconciler_handleExternalAuthConfig_ClearsMirrorOnSourceNotFound(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	scheme := testutil.NewScheme(t)

	mcpServer := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("gone"),
		v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{
			Conditions: []metav1.Condition{{
				Type:    mcpv1beta1.ConditionTypeExternalAuthConfigValidated,
				Status:  metav1.ConditionFalse,
				Reason:  mcpv1beta1.ConditionReasonEnterpriseRequired,
				Message: "stale mirror — source has since been deleted",
			}},
		}),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	err := reconciler.handleExternalAuthConfig(ctx, mcpServer)
	require.Error(t, err, "NotFound lookup must still surface an error")
	assert.Contains(t, err.Error(), "not found",
		"the pre-existing NotFound error contract is unchanged by the mirror-clear")
	assert.Nil(t,
		meta.FindStatusCondition(mcpServer.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated),
		"stale mirror must be cleared when the referenced source is NotFound")
}

// TestMCPServerDeployment_OBOSecretEnvVars verifies that an obo-typed
// MCPExternalAuthConfig referenced from an MCPServer injects the registered
// OBOHandler.SecretEnvVars output into the proxy container, and that the
// deployment builder (deploymentForMCPServer) and the drift check
// (deploymentNeedsUpdate) agree on it. MCPServer assembles env in two separate
// functions, so this guards the builder/drift symmetry that prevents a reconcile
// hot-loop. A stub OBO handler stands in for the out-of-tree enterprise handler.
//
//nolint:paralleltest // Mutates package-level oboHandler via RegisterOBOHandler.
func TestMCPServerDeployment_OBOSecretEnvVars(t *testing.T) {
	t.Cleanup(func() { ctrlutil.RegisterOBOHandler(defaultOBOHandlerStub()) })

	oboEnvVar := corev1.EnvVar{
		Name: "TOOLHIVE_OBO_CLIENT_SECRET",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "obo-secret"},
				Key:                  "client-secret",
			},
		},
	}
	stub := defaultOBOHandlerStub()
	stub.SecretEnvVars = func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
		return []corev1.EnvVar{oboEnvVar}, nil
	}
	ctrlutil.RegisterOBOHandler(stub)

	scheme := testutil.NewScheme(t)

	authConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "obo-config", Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  &mcpv1beta1.OBOConfig{},
		},
	}
	mcpServer := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.WithExternalAuthConfigRef(authConfig.Name),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authConfig).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	deployment, err := reconciler.deploymentForMCPServer(t.Context(), mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment)

	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Contains(t, container.Env, oboEnvVar,
		"OBO handler SecretEnvVars output must be injected into the proxy container")

	assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, mcpServer, "test-checksum"),
		"builder and drift check must agree on the OBO env var (no reconcile hot-loop)")
}

// TestMCPServerDeployment_OBOSecretEnvVars_GenuineErrorDivergence locks in the
// documented MCPServer builder/drift behavior when the registered OBO handler
// returns a genuine (non-ErrEnterpriseRequired) error: the builder logs and
// continues, producing an OBO-env-less Deployment without failing, while
// deploymentNeedsUpdate reports the Deployment needs an update. This divergence
// is described at the drift site and exercised nowhere else at the controller
// level. (The inert ErrEnterpriseRequired path stays symmetric — see the test
// above — because AddOBOSecretEnvVars swallows it.)
//
//nolint:paralleltest // Mutates package-level oboHandler via RegisterOBOHandler.
func TestMCPServerDeployment_OBOSecretEnvVars_GenuineErrorDivergence(t *testing.T) {
	t.Cleanup(func() { ctrlutil.RegisterOBOHandler(defaultOBOHandlerStub()) })

	stub := defaultOBOHandlerStub()
	stub.SecretEnvVars = func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
		return nil, errors.New("secret not yet available")
	}
	ctrlutil.RegisterOBOHandler(stub)

	scheme := testutil.NewScheme(t)

	authConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "obo-config", Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  &mcpv1beta1.OBOConfig{},
		},
	}
	mcpServer := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.WithExternalAuthConfigRef(authConfig.Name),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authConfig).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	// Builder logs-and-continues: a Deployment is still produced (no error, no
	// panic), with no OBO env var injected.
	deployment, err := reconciler.deploymentForMCPServer(t.Context(), mcpServer, "test-checksum")
	require.NoError(t, err, "builder must log-and-continue on a genuine OBO handler error, not fail")
	require.NotNil(t, deployment)

	// Drift check returns true on the same error — the documented divergence.
	assert.True(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, mcpServer, "test-checksum"),
		"deploymentNeedsUpdate reports drift while the OBO handler errors")
}
