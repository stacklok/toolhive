// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestDeploymentForMCPServer_TelemetryCABundleVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		telemetryConfig   *mcpv1beta1.MCPTelemetryConfig
		expectVolumeName  string
		expectMountPath   string
		expectConfigMap   string
		expectKey         string
		expectNoCAVolumes bool
	}{
		{
			name: "CA bundle volume and mount are present with default key",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-telemetry",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel-collector:4318",
						Tracing:  &mcpv1beta1.OpenTelemetryTracingConfig{Enabled: true},
						CABundleRef: &mcpv1beta1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "otel-ca-bundle",
								},
							},
						},
					},
				},
			},
			expectVolumeName: "otel-ca-bundle-otel-ca-bundle",
			expectMountPath:  "/config/certs/otel/otel-ca-bundle",
			expectConfigMap:  "otel-ca-bundle",
			expectKey:        "ca.crt",
		},
		{
			name: "CA bundle volume and mount use custom key",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-telemetry",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel-collector:4318",
						Tracing:  &mcpv1beta1.OpenTelemetryTracingConfig{Enabled: true},
						CABundleRef: &mcpv1beta1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "internal-ca",
								},
								Key: "tls-ca.pem",
							},
						},
					},
				},
			},
			expectVolumeName: "otel-ca-bundle-internal-ca",
			expectMountPath:  "/config/certs/otel/internal-ca",
			expectConfigMap:  "internal-ca",
			expectKey:        "tls-ca.pem",
		},
		{
			name: "no CA bundle when telemetry config has no caBundleRef",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-telemetry",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel-collector:4318",
						Tracing:  &mcpv1beta1.OpenTelemetryTracingConfig{Enabled: true},
					},
				},
			},
			expectNoCAVolumes: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.telemetryConfig).
				Build()

			mcpServer := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image:latest",
					Transport: "stdio",
					ProxyPort: 8080,
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{
						Name: "my-telemetry",
					},
				},
			}

			r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)
			deployment := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
			require.NotNil(t, deployment, "deployment should not be nil")

			podSpec := deployment.Spec.Template.Spec
			container := podSpec.Containers[0]

			if tt.expectNoCAVolumes {
				for _, v := range podSpec.Volumes {
					assert.NotContains(t, v.Name, "otel-ca-bundle",
						"should not have any otel CA bundle volumes")
				}
				return
			}

			// Find the expected volume
			var foundVolume *corev1.Volume
			for i := range podSpec.Volumes {
				if podSpec.Volumes[i].Name == tt.expectVolumeName {
					foundVolume = &podSpec.Volumes[i]
					break
				}
			}
			require.NotNil(t, foundVolume, "expected volume %q not found", tt.expectVolumeName)
			require.NotNil(t, foundVolume.ConfigMap, "volume should be a ConfigMap volume")
			assert.Equal(t, tt.expectConfigMap, foundVolume.ConfigMap.Name)
			require.Len(t, foundVolume.ConfigMap.Items, 1)
			assert.Equal(t, tt.expectKey, foundVolume.ConfigMap.Items[0].Key)

			// Find the expected volume mount
			var foundMount *corev1.VolumeMount
			for i := range container.VolumeMounts {
				if container.VolumeMounts[i].Name == tt.expectVolumeName {
					foundMount = &container.VolumeMounts[i]
					break
				}
			}
			require.NotNil(t, foundMount, "expected volume mount %q not found", tt.expectVolumeName)
			assert.Equal(t, tt.expectMountPath, foundMount.MountPath)
			assert.True(t, foundMount.ReadOnly, "CA bundle mount should be read-only")
		})
	}
}

func TestDeploymentForMCPServer_TelemetryCABundleVolume_FetchError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Build a client that does NOT have the MCPTelemetryConfig object.
	// The MCPServer references it, so getTelemetryConfigForMCPServer returns nil (not found).
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			ProxyPort: 8080,
			TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{
				Name: "missing-telemetry-config",
			},
		},
	}

	r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)
	deployment := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")

	// When the referenced MCPTelemetryConfig is not found, getTelemetryConfigForMCPServer
	// returns nil without error (NotFound is swallowed). The deployment should still be created
	// but without any otel CA bundle volumes.
	require.NotNil(t, deployment, "deployment should still be created when telemetry config is not found")

	for _, v := range deployment.Spec.Template.Spec.Volumes {
		assert.NotContains(t, v.Name, "otel-ca-bundle",
			"should not have otel CA bundle volumes when telemetry config is not found")
	}
}
