// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestAddTelemetryCABundleVolumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		telemetryConfig *mcpv1beta1.MCPTelemetryConfig
		wantVolumeName  string
		wantConfigMap   string
		wantKey         string
		wantMountPath   string
	}{
		{
			name:            "nil config returns nil",
			telemetryConfig: nil,
		},
		{
			name: "nil OpenTelemetry returns nil",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{},
			},
		},
		{
			name: "nil CABundleRef returns nil",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						Endpoint: "https://collector.example.com:4317",
					},
				},
			},
		},
		{
			name: "nil ConfigMapRef returns nil",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						CABundleRef: &mcpv1beta1.CABundleSource{},
					},
				},
			},
		},
		{
			name: "ConfigMapRef with default key",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						Endpoint: "https://collector.example.com:4317",
						CABundleRef: &mcpv1beta1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "my-ca-bundle"},
							},
						},
					},
				},
			},
			wantVolumeName: "otel-ca-bundle-my-ca-bundle",
			wantConfigMap:  "my-ca-bundle",
			wantKey:        "ca.crt",
			wantMountPath:  "/config/certs/otel/my-ca-bundle",
		},
		{
			name: "ConfigMapRef with custom key",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						CABundleRef: &mcpv1beta1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "internal-ca"},
								Key:                  "tls-ca.pem",
							},
						},
					},
				},
			},
			wantVolumeName: "otel-ca-bundle-internal-ca",
			wantConfigMap:  "internal-ca",
			wantKey:        "tls-ca.pem",
			wantMountPath:  "/config/certs/otel/internal-ca",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			volumes, mounts := AddTelemetryCABundleVolumes(tt.telemetryConfig)

			if tt.wantVolumeName == "" {
				assert.Empty(t, volumes)
				assert.Empty(t, mounts)
				return
			}

			require.Len(t, volumes, 1)
			require.Len(t, mounts, 1)

			vol := volumes[0]
			assert.Equal(t, tt.wantVolumeName, vol.Name)
			require.NotNil(t, vol.ConfigMap)
			assert.Equal(t, tt.wantConfigMap, vol.ConfigMap.Name)
			require.Len(t, vol.ConfigMap.Items, 1)
			assert.Equal(t, tt.wantKey, vol.ConfigMap.Items[0].Key)
			assert.Equal(t, tt.wantKey, vol.ConfigMap.Items[0].Path)

			mount := mounts[0]
			assert.Equal(t, tt.wantVolumeName, mount.Name)
			assert.Equal(t, tt.wantMountPath, mount.MountPath)
			assert.True(t, mount.ReadOnly)
		})
	}
}

func TestTelemetryCABundleFilePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		telemetryConfig *mcpv1beta1.MCPTelemetryConfig
		wantPath        string
	}{
		{
			name:            "nil config returns empty",
			telemetryConfig: nil,
			wantPath:        "",
		},
		{
			name: "nil OpenTelemetry returns empty",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{},
			},
			wantPath: "",
		},
		{
			name: "nil CABundleRef returns empty",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{},
				},
			},
			wantPath: "",
		},
		{
			name: "nil ConfigMapRef returns empty",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						CABundleRef: &mcpv1beta1.CABundleSource{},
					},
				},
			},
			wantPath: "",
		},
		{
			name: "default key produces correct path",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						CABundleRef: &mcpv1beta1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "my-ca-bundle"},
							},
						},
					},
				},
			},
			wantPath: "/config/certs/otel/my-ca-bundle/ca.crt",
		},
		{
			name: "custom key produces correct path",
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						CABundleRef: &mcpv1beta1.CABundleSource{
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "internal-ca"},
								Key:                  "tls-ca.pem",
							},
						},
					},
				},
			},
			wantPath: "/config/certs/otel/internal-ca/tls-ca.pem",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := TelemetryCABundleFilePath(tt.telemetryConfig)
			assert.Equal(t, tt.wantPath, got)
		})
	}
}
