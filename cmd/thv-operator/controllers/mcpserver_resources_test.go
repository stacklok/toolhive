// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// TestDeploymentForMCPServerSpecResourcesReachMCPContainer is the regression
// test for https://github.com/stacklok/toolhive/issues/7101:
// MCPServer.spec.resources is documented as "the resource requirements for the
// MCP server container", but the operator applied it to the proxy-runner
// (toolhive) container instead, leaving the backend StatefulSet's mcp
// container with resources: {} (BestEffort QoS).
//
// Expected after the fix: the proxy container carries no resources from
// spec.resources, and the pod-template patch passed to the proxy runner sets
// the resources on the mcp container, so the proxy runner applies them to
// the backend StatefulSet.
func TestDeploymentForMCPServerSpecResourcesReachMCPContainer(t *testing.T) {
	t.Parallel()

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server-resources",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			ProxyPort: 8080,
			Resources: mcpv1beta1.ResourceRequirements{
				Limits: mcpv1beta1.ResourceList{
					CPU:    "1",
					Memory: "512Mi",
				},
				Requests: mcpv1beta1.ResourceList{
					CPU:    "100m",
					Memory: "128Mi",
				},
			},
		},
	}

	s := testutil.NewScheme(t)
	s.AddKnownTypes(mcpv1beta1.GroupVersion, &mcpv1beta1.MCPServer{})
	s.AddKnownTypes(mcpv1beta1.GroupVersion, &mcpv1beta1.MCPServerList{})

	r := newTestMCPServerReconciler(nil, s, kubernetes.PlatformKubernetes)

	ctx := context.Background()
	deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment, "Deployment should not be nil")

	// spec.resources must NOT land on the proxy-runner (toolhive) container.
	proxyContainer := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "toolhive", proxyContainer.Name)
	assert.True(t, len(proxyContainer.Resources.Limits) == 0 && len(proxyContainer.Resources.Requests) == 0,
		"proxy container must not carry spec.resources, got %+v", proxyContainer.Resources)

	// spec.resources must reach the mcp container via the pod-template patch.
	patch := findPodTemplatePatch(t, deployment)
	require.NotNil(t, patch, "pod template patch should be present")
	require.Len(t, patch.Spec.Containers, 1, "patch should contain the mcp container")
	mcpContainer := patch.Spec.Containers[0]
	assert.Equal(t, "mcp", mcpContainer.Name)
	assert.Equal(t, "1", mcpContainer.Resources.Limits.Cpu().String())
	assert.Equal(t, "512Mi", mcpContainer.Resources.Limits.Memory().String())
	assert.Equal(t, "100m", mcpContainer.Resources.Requests.Cpu().String())
	assert.Equal(t, "128Mi", mcpContainer.Resources.Requests.Memory().String())
}

// TestDeploymentForMCPServerPodTemplateSpecResourcesWin verifies that
// resources set explicitly on the mcp container via spec.podTemplateSpec
// take precedence over spec.resources (the documented workaround keeps
// working and is not silently overwritten).
func TestDeploymentForMCPServerPodTemplateSpecResourcesWin(t *testing.T) {
	t.Parallel()

	podTemplateSpec := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "mcp",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server-resources-override",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:           "test-image:latest",
			Transport:       "stdio",
			ProxyPort:       8080,
			PodTemplateSpec: podTemplateSpecToRawExtension(t, podTemplateSpec),
			Resources: mcpv1beta1.ResourceRequirements{
				Limits: mcpv1beta1.ResourceList{
					CPU:    "1",
					Memory: "512Mi",
				},
				Requests: mcpv1beta1.ResourceList{
					CPU:    "100m",
					Memory: "128Mi",
				},
			},
		},
	}

	s := testutil.NewScheme(t)
	s.AddKnownTypes(mcpv1beta1.GroupVersion, &mcpv1beta1.MCPServer{})
	s.AddKnownTypes(mcpv1beta1.GroupVersion, &mcpv1beta1.MCPServerList{})

	r := newTestMCPServerReconciler(nil, s, kubernetes.PlatformKubernetes)

	ctx := context.Background()
	deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment, "Deployment should not be nil")

	patch := findPodTemplatePatch(t, deployment)
	require.NotNil(t, patch, "pod template patch should be present")
	require.Len(t, patch.Spec.Containers, 1, "patch should contain the mcp container")
	mcpContainer := patch.Spec.Containers[0]
	assert.Equal(t, "2", mcpContainer.Resources.Limits.Cpu().String(),
		"explicit podTemplateSpec resources must win over spec.resources")
	assert.Equal(t, "1Gi", mcpContainer.Resources.Limits.Memory().String())
}

// findPodTemplatePatch extracts and unmarshals the --k8s-pod-patch arg from
// the proxy container. Returns nil if no patch arg is present.
func findPodTemplatePatch(t *testing.T, deployment *appsv1.Deployment) *corev1.PodTemplateSpec {
	t.Helper()

	container := deployment.Spec.Template.Spec.Containers[0]
	for _, arg := range container.Args {
		if !strings.HasPrefix(arg, "--k8s-pod-patch=") {
			continue
		}
		var podTemplateSpec corev1.PodTemplateSpec
		err := json.Unmarshal([]byte(arg[len("--k8s-pod-patch="):]), &podTemplateSpec)
		require.NoError(t, err, "Should be able to unmarshal pod template patch")
		return &podTemplateSpec
	}
	return nil
}
