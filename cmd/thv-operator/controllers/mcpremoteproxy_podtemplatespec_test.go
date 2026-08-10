// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

func rawPodTemplateSpecJSON(t *testing.T, raw string) *runtime.RawExtension {
	t.Helper()
	return &runtime.RawExtension{Raw: []byte(raw)}
}

func TestMCPRemoteProxyPodTemplateSpecValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		podTemplateSpec       *runtime.RawExtension
		expectError           bool
		expectedPhase         mcpv1beta1.MCPRemoteProxyPhase
		expectedCondition     metav1.ConditionStatus
		expectedReason        string
		expectedStatusMessage string
	}{
		{
			name:              "valid PodTemplateSpec",
			podTemplateSpec:   rawPodTemplateSpecJSON(t, `{"spec":{"nodeSelector":{"disk":"ssd"}}}`),
			expectedCondition: metav1.ConditionTrue,
			expectedReason:    mcpv1beta1.ConditionReasonMCPRemoteProxyPodTemplateValid,
		},
		{
			name: "invalid PodTemplateSpec",
			podTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{"spec":{"containers":"not-a-container-list"}}`),
			},
			expectError:           true,
			expectedPhase:         mcpv1beta1.MCPRemoteProxyPhaseFailed,
			expectedCondition:     metav1.ConditionFalse,
			expectedReason:        mcpv1beta1.ConditionReasonMCPRemoteProxyPodTemplateInvalid,
			expectedStatusMessage: "Invalid PodTemplateSpec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy := &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "template-proxy",
					Namespace:  "default",
					Generation: 3,
				},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					RemoteURL:       "https://mcp.example.com",
					PodTemplateSpec: tt.podTemplateSpec,
				},
			}
			scheme := testutil.NewScheme(t)
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(proxy).
				WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}).
				Build()
			reconciler := &MCPRemoteProxyReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Recorder: events.NewFakeRecorder(10),
			}

			err := reconciler.validateAndHandleConfigs(t.Context(), proxy)
			if tt.expectError {
				require.Error(t, err)
				assert.True(t, stderrors.Is(err, errInvalidMCPRemoteProxyPodTemplateSpec))
			} else {
				require.NoError(t, err)
			}

			updated := &mcpv1beta1.MCPRemoteProxy{}
			require.NoError(t, fakeClient.Get(t.Context(), types.NamespacedName{
				Name:      proxy.Name,
				Namespace: proxy.Namespace,
			}, updated))

			condition := meta.FindStatusCondition(
				updated.Status.Conditions,
				mcpv1beta1.ConditionTypeMCPRemoteProxyPodTemplateValid,
			)
			require.NotNil(t, condition)
			assert.Equal(t, tt.expectedCondition, condition.Status)
			assert.Equal(t, tt.expectedReason, condition.Reason)
			assert.Equal(t, proxy.Generation, condition.ObservedGeneration)

			if tt.expectedPhase != "" {
				assert.Equal(t, tt.expectedPhase, updated.Status.Phase)
			}
			if tt.expectedStatusMessage != "" {
				assert.Contains(t, updated.Status.Message, tt.expectedStatusMessage)
			}
		})
	}
}

func TestMCPRemoteProxyPodTemplateSpecMerge(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template-proxy",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			PodTemplateSpec: rawPodTemplateSpecJSON(t, `{
				"metadata": {
					"labels": {"custom-template-label": "enabled"},
					"annotations": {"custom-template-annotation": "enabled"}
				},
				"spec": {
					"nodeSelector": {"disk": "ssd"},
					"tolerations": [{
						"key": "dedicated",
						"operator": "Equal",
						"value": "toolhive",
						"effect": "NoSchedule"
					}],
					"containers": [{
						"name": "toolhive",
						"env": [{"name": "EXTRA_PROXY_ENV", "value": "enabled"}]
					}, {
						"name": "sidecar",
						"image": "busybox:latest",
						"args": ["sleep", "3600"]
					}]
				}
			}`),
		},
	}

	scheme := testutil.NewScheme(t)
	reconciler := &MCPRemoteProxyReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
	require.NotNil(t, deployment)

	assert.Equal(t, "ssd", deployment.Spec.Template.Spec.NodeSelector["disk"])
	require.Len(t, deployment.Spec.Template.Spec.Tolerations, 1)
	assert.Equal(t, "enabled", deployment.Spec.Template.Labels["custom-template-label"])
	assert.Equal(t, "enabled", deployment.Spec.Template.Annotations["custom-template-annotation"])
	assert.Contains(t, deployment.Annotations, podTemplateSpecHashAnnotation)

	toolhiveContainer, ok := findContainerByName(deployment.Spec.Template.Spec.Containers, mcpRemoteProxyContainerName)
	require.True(t, ok)
	assert.Equal(t, getToolhiveRunnerImage(), toolhiveContainer.Image)
	assert.Contains(t, toolhiveContainer.Args, "run")
	assert.Contains(t, toolhiveContainer.Env, corev1.EnvVar{Name: "EXTRA_PROXY_ENV", Value: "enabled"})

	sidecar, ok := findContainerByName(deployment.Spec.Template.Spec.Containers, "sidecar")
	require.True(t, ok)
	assert.Equal(t, "busybox:latest", sidecar.Image)
}

func TestMCPRemoteProxyPodTemplateSpecMergeAppliesFieldsOutsideBuilderEmptinessCheck(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template-proxy",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			RemoteURL:       "https://mcp.example.com",
			PodTemplateSpec: rawPodTemplateSpecJSON(t, `{"spec":{"runtimeClassName":"gvisor"}}`),
		},
	}

	scheme := testutil.NewScheme(t)
	reconciler := &MCPRemoteProxyReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
	require.NotNil(t, deployment)
	require.NotNil(t, deployment.Spec.Template.Spec.RuntimeClassName)
	assert.Equal(t, "gvisor", *deployment.Spec.Template.Spec.RuntimeClassName)
}

func TestMCPRemoteProxyPodTemplateSpecDriftDetection(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template-proxy",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			RemoteURL:       "https://mcp.example.com",
			PodTemplateSpec: rawPodTemplateSpecJSON(t, `{"spec":{"nodeSelector":{"disk":"ssd"}}}`),
		},
	}

	scheme := testutil.NewScheme(t)
	reconciler := &MCPRemoteProxyReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
	require.NotNil(t, deployment)
	assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"))

	proxy.Spec.PodTemplateSpec = rawPodTemplateSpecJSON(t, `{"spec":{"nodeSelector":{"disk":"nvme"}}}`)
	assert.True(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"))

	proxy.Spec.PodTemplateSpec = nil
	assert.True(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"))
}

// Scheduling can arrive from either resourceOverrides.proxyDeployment or
// podTemplateSpec. Drift detection must account for both, so an override-only
// comparison would report false drift on a podTemplateSpec-supplied value.
func TestMCPRemoteProxySchedulingOverridesAndPodTemplateSpec(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)
	reconciler := &MCPRemoteProxyReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	t.Run("overrides applied and stable", func(t *testing.T) {
		t.Parallel()

		proxy := &mcpv1beta1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{Name: "sched-proxy", Namespace: "default"},
			Spec: mcpv1beta1.MCPRemoteProxySpec{
				RemoteURL: "https://mcp.example.com",
				ResourceOverrides: &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						NodeSelector: map[string]string{"workload-class": "mcp-warm"},
						Tolerations: []corev1.Toleration{{
							Key:      "workload-class",
							Operator: corev1.TolerationOpEqual,
							Value:    "mcp-warm",
							Effect:   corev1.TaintEffectNoSchedule,
						}},
					},
				},
			},
		}

		deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
		require.NotNil(t, deployment)
		assert.Equal(t, map[string]string{"workload-class": "mcp-warm"},
			deployment.Spec.Template.Spec.NodeSelector)
		require.Len(t, deployment.Spec.Template.Spec.Tolerations, 1)
		assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"),
			"freshly built deployment must not report drift")

		// Clearing the overrides must be detected.
		proxy.Spec.ResourceOverrides = nil
		assert.True(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"),
			"clearing the overrides must be detected as drift")
	})

	t.Run("podTemplateSpec-supplied scheduling is not false drift", func(t *testing.T) {
		t.Parallel()

		proxy := &mcpv1beta1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{Name: "tmpl-sched-proxy", Namespace: "default"},
			Spec: mcpv1beta1.MCPRemoteProxySpec{
				RemoteURL:       "https://mcp.example.com",
				PodTemplateSpec: rawPodTemplateSpecJSON(t, `{"spec":{"nodeSelector":{"disk":"ssd"}}}`),
			},
		}

		deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
		require.NotNil(t, deployment)
		assert.Equal(t, map[string]string{"disk": "ssd"}, deployment.Spec.Template.Spec.NodeSelector)
		assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"),
			"scheduling from podTemplateSpec must not be mistaken for drift against empty overrides")
	})

	// Locks the precedence the API godoc promises when both routes are used, which
	// is not uniform across the three fields: the nodeSelector maps merge, while
	// tolerations and affinity are replaced wholesale.
	t.Run("podTemplateSpec precedence over the overrides", func(t *testing.T) {
		t.Parallel()

		overrideToleration := corev1.Toleration{
			Key:      "workload-class",
			Operator: corev1.TolerationOpEqual,
			Value:    "mcp-warm",
			Effect:   corev1.TaintEffectNoSchedule,
		}
		proxy := &mcpv1beta1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{Name: "both-sched-proxy", Namespace: "default"},
			Spec: mcpv1beta1.MCPRemoteProxySpec{
				RemoteURL: "https://mcp.example.com",
				PodTemplateSpec: rawPodTemplateSpecJSON(t, `{"spec":{
					"nodeSelector":{"zone":"from-template","disk":"ssd"},
					"tolerations":[{"key":"from-template","operator":"Exists","effect":"NoSchedule"}]
				}}`),
				ResourceOverrides: &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						NodeSelector: map[string]string{"zone": "from-override", "workload-class": "mcp-warm"},
						Tolerations:  []corev1.Toleration{overrideToleration},
					},
				},
			},
		}

		deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
		require.NotNil(t, deployment)

		// nodeSelector merges: podTemplateSpec wins on "zone", the override's
		// unique "workload-class" key survives.
		assert.Equal(t,
			map[string]string{"zone": "from-template", "disk": "ssd", "workload-class": "mcp-warm"},
			deployment.Spec.Template.Spec.NodeSelector)

		// tolerations are replaced, not appended — the override's is dropped.
		require.Len(t, deployment.Spec.Template.Spec.Tolerations, 1)
		assert.Equal(t, "from-template", deployment.Spec.Template.Spec.Tolerations[0].Key)
		assert.NotContains(t, deployment.Spec.Template.Spec.Tolerations, overrideToleration)

		assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"),
			"the overridden fields must not be reported as perpetual drift")
	})
}
