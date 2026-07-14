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
