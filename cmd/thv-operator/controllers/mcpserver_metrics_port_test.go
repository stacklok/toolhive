// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/diagnostics"
)

func TestProxyContainerPorts(t *testing.T) {
	t.Parallel()

	t.Run("metrics disabled declares only the http port", func(t *testing.T) {
		t.Parallel()
		ports := proxyContainerPorts(8080, false)
		require.Len(t, ports, 1)
		assert.Equal(t, "http", ports[0].Name)
		assert.Equal(t, int32(8080), ports[0].ContainerPort)
	})

	t.Run("metrics enabled appends the named metrics port", func(t *testing.T) {
		t.Parallel()
		ports := proxyContainerPorts(8080, true)
		require.Len(t, ports, 2)
		assert.Equal(t, "http", ports[0].Name)
		assert.Equal(t, int32(8080), ports[0].ContainerPort)
		assert.Equal(t, metricsContainerPortName, ports[1].Name)
		assert.Equal(t, int32(diagnostics.DefaultPort), ports[1].ContainerPort)
	})
}

func TestHasMetricsPort(t *testing.T) {
	t.Parallel()

	assert.False(t, hasMetricsPort(nil))
	assert.False(t, hasMetricsPort([]corev1.ContainerPort{{Name: "http"}}))
	assert.True(t, hasMetricsPort([]corev1.ContainerPort{{Name: "http"}, {Name: metricsContainerPortName}}))
}

func TestPrometheusMetricsEnabled(t *testing.T) {
	t.Parallel()

	assert.False(t, prometheusMetricsEnabled(nil))
	assert.False(t, prometheusMetricsEnabled(&mcpv1beta1.MCPTelemetryConfig{}))
	assert.False(t, prometheusMetricsEnabled(&mcpv1beta1.MCPTelemetryConfig{
		Spec: mcpv1beta1.MCPTelemetryConfigSpec{Prometheus: &mcpv1beta1.PrometheusConfig{Enabled: false}},
	}))
	assert.True(t, prometheusMetricsEnabled(&mcpv1beta1.MCPTelemetryConfig{
		Spec: mcpv1beta1.MCPTelemetryConfigSpec{Prometheus: &mcpv1beta1.PrometheusConfig{Enabled: true}},
	}))
}

// TestDeploymentForMCPServer_MetricsPort asserts the pod declares the named
// "metrics" container port exactly when the referenced MCPTelemetryConfig
// enables Prometheus metrics, so a PodMonitor can target the diagnostics
// listener by name (#6434).
func TestDeploymentForMCPServer_MetricsPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		telemetryConfig *mcpv1beta1.MCPTelemetryConfig
		expectMetrics   bool
	}{
		{
			name: "prometheus disabled",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-telemetry", Namespace: "default"},
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					Prometheus: &mcpv1beta1.PrometheusConfig{Enabled: false},
				},
			},
			expectMetrics: false,
		},
		{
			name: "prometheus enabled",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-telemetry", Namespace: "default"},
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					Prometheus: &mcpv1beta1.PrometheusConfig{Enabled: true},
				},
			},
			expectMetrics: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			scheme := testutil.NewScheme(t)
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.telemetryConfig).
				Build()

			mcpServer := v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithTelemetryConfigRef("my-telemetry"),
			)

			r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)
			deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
			require.NoError(t, err)
			require.NotNil(t, deployment)

			ports := deployment.Spec.Template.Spec.Containers[0].Ports
			assert.Equal(t, tt.expectMetrics, hasMetricsPort(ports))
			assert.Equal(t, "http", ports[0].Name, "the http port must remain first")
		})
	}
}
