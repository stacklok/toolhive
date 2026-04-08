// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

func TestAddOTelCABundleVolumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		caBundleRef    *mcpv1alpha1.CABundleSource
		wantVolumeName string
		wantConfigMap  string
		wantKey        string
		wantMountPath  string
	}{
		{
			name:        "nil caBundleRef returns nil",
			caBundleRef: nil,
		},
		{
			name:        "nil ConfigMapRef returns nil",
			caBundleRef: &mcpv1alpha1.CABundleSource{},
		},
		{
			name: "valid caBundleRef with explicit key",
			caBundleRef: &mcpv1alpha1.CABundleSource{
				ConfigMapRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "otel-ca"},
					Key:                  "custom.pem",
				},
			},
			wantVolumeName: "otel-ca-bundle-otel-ca",
			wantConfigMap:  "otel-ca",
			wantKey:        "custom.pem",
			wantMountPath:  "/config/otel-certs/otel-ca",
		},
		{
			name: "valid caBundleRef without key defaults to ca.crt",
			caBundleRef: &mcpv1alpha1.CABundleSource{
				ConfigMapRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "otel-ca"},
				},
			},
			wantVolumeName: "otel-ca-bundle-otel-ca",
			wantConfigMap:  "otel-ca",
			wantKey:        "ca.crt",
			wantMountPath:  "/config/otel-certs/otel-ca",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			volumes, mounts := controllerutil.AddOTelCABundleVolumes(tt.caBundleRef)

			if tt.wantVolumeName == "" {
				if volumes != nil {
					t.Errorf("expected nil volumes, got %v", volumes)
				}
				if mounts != nil {
					t.Errorf("expected nil mounts, got %v", mounts)
				}
				return
			}

			if len(volumes) != 1 {
				t.Fatalf("expected 1 volume, got %d", len(volumes))
			}
			if len(mounts) != 1 {
				t.Fatalf("expected 1 mount, got %d", len(mounts))
			}

			vol := volumes[0]
			if vol.Name != tt.wantVolumeName {
				t.Errorf("volume name = %q, want %q", vol.Name, tt.wantVolumeName)
			}
			if vol.ConfigMap == nil {
				t.Fatal("expected ConfigMap volume source, got nil")
			}
			if vol.ConfigMap.Name != tt.wantConfigMap {
				t.Errorf("configMap name = %q, want %q", vol.ConfigMap.Name, tt.wantConfigMap)
			}
			if len(vol.ConfigMap.Items) != 1 {
				t.Fatalf("expected 1 item, got %d", len(vol.ConfigMap.Items))
			}
			if vol.ConfigMap.Items[0].Key != tt.wantKey {
				t.Errorf("item key = %q, want %q", vol.ConfigMap.Items[0].Key, tt.wantKey)
			}

			mount := mounts[0]
			if mount.Name != tt.wantVolumeName {
				t.Errorf("mount name = %q, want %q", mount.Name, tt.wantVolumeName)
			}
			if mount.MountPath != tt.wantMountPath {
				t.Errorf("mount path = %q, want %q", mount.MountPath, tt.wantMountPath)
			}
			if !mount.ReadOnly {
				t.Error("expected mount to be ReadOnly")
			}
		})
	}
}

func TestComputeOTelCABundlePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		caBundleRef *mcpv1alpha1.CABundleSource
		want        string
	}{
		{
			name:        "nil caBundleRef returns empty",
			caBundleRef: nil,
			want:        "",
		},
		{
			name:        "nil ConfigMapRef returns empty",
			caBundleRef: &mcpv1alpha1.CABundleSource{},
			want:        "",
		},
		{
			name: "valid with explicit key",
			caBundleRef: &mcpv1alpha1.CABundleSource{
				ConfigMapRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "myca"},
					Key:                  "custom.pem",
				},
			},
			want: "/config/otel-certs/myca/custom.pem",
		},
		{
			name: "valid without key defaults to ca.crt",
			caBundleRef: &mcpv1alpha1.CABundleSource{
				ConfigMapRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "myca"},
				},
			},
			want: "/config/otel-certs/myca/ca.crt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := controllerutil.ComputeOTelCABundlePath(tt.caBundleRef)
			if got != tt.want {
				t.Errorf("ComputeOTelCABundlePath() = %q, want %q", got, tt.want)
			}
		})
	}
}
