// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestAddOIDCCABundleVolumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		oidcConfig     *mcpv1alpha1.OIDCConfigRef
		wantVolumeName string
		wantConfigMap  string
		wantKey        string
		wantMountPath  string
	}{
		{
			name:       "nil OIDCConfig returns nil",
			oidcConfig: nil,
		},
		{
			name: "missing CABundleRef returns nil",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{
				Type:      mcpv1alpha1.OIDCConfigTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{Name: "oidc-config"},
			},
		},
		{
			name: "configMap type with caBundleRef uses default key",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
					Name: "oidc-config",
					CABundleRef: &mcpv1alpha1.CABundleSource{
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "my-ca-bundle"},
						},
					},
				},
			},
			wantVolumeName: "oidc-ca-bundle-my-ca-bundle",
			wantConfigMap:  "my-ca-bundle",
			wantKey:        "ca.crt",
			wantMountPath:  "/config/certs/my-ca-bundle",
		},
		{
			name: "inline type with custom key",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer: "https://issuer.example.com",
					CABundleRef: &mcpv1alpha1.CABundleSource{
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "custom-ca"},
							Key:                  "custom.pem",
						},
					},
				},
			},
			wantVolumeName: "oidc-ca-bundle-custom-ca",
			wantConfigMap:  "custom-ca",
			wantKey:        "custom.pem",
			wantMountPath:  "/config/certs/custom-ca",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			volumes, mounts := AddOIDCCABundleVolumes(tt.oidcConfig)

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

			mount := mounts[0]
			assert.Equal(t, tt.wantVolumeName, mount.Name)
			assert.Equal(t, tt.wantMountPath, mount.MountPath)
			assert.True(t, mount.ReadOnly)
		})
	}
}
