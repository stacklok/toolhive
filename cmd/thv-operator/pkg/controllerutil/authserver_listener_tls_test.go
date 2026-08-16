// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestGenerateAuthServerVolumesListenerTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		certificateSecretRef *mcpv1beta1.SecretKeyRef
		privateKeySecretRef  *mcpv1beta1.SecretKeyRef
	}{
		{
			name:                 "same Secret",
			certificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "certificate"},
			privateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "private-key"},
		},
		{
			name:                 "different Secrets",
			certificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-certificate", Key: "certificate"},
			privateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-private-key", Key: "private-key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			volumes, mounts, err := GenerateAuthServerVolumes(&mcpv1beta1.EmbeddedAuthServerConfig{
				ListenerTLS: &mcpv1beta1.ListenerTLSConfig{
					CertificateSecretRef: tt.certificateSecretRef,
					PrivateKeySecretRef:  tt.privateKeySecretRef,
				},
			})
			require.NoError(t, err)

			require.Len(t, volumes, 1)
			require.Equal(t, AuthServerTLSVolumeName, volumes[0].Name)
			require.NotNil(t, volumes[0].Projected)
			require.Len(t, volumes[0].Projected.Sources, 2)
			require.Equal(t, &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: tt.certificateSecretRef.Name},
				Items:                []corev1.KeyToPath{{Key: tt.certificateSecretRef.Key, Path: "tls.crt"}},
			}, volumes[0].Projected.Sources[0].Secret)
			require.Equal(t, &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: tt.privateKeySecretRef.Name},
				Items:                []corev1.KeyToPath{{Key: tt.privateKeySecretRef.Key, Path: "tls.key"}},
			}, volumes[0].Projected.Sources[1].Secret)
			require.NotNil(t, volumes[0].Projected.DefaultMode)
			require.Equal(t, int32(0400), *volumes[0].Projected.DefaultMode)

			require.Len(t, mounts, 1)
			require.Equal(t, AuthServerTLSVolumeName, mounts[0].Name)
			require.Equal(t, AuthServerTLSMountPath, mounts[0].MountPath)
			require.Empty(t, mounts[0].SubPath)
			require.True(t, mounts[0].ReadOnly)
			require.True(t, HasAuthServerListenerTLS(mounts))
		})
	}
}

func TestHasAuthServerListenerTLSMissing(t *testing.T) {
	t.Parallel()

	mounts := []corev1.VolumeMount{{Name: "other-volume"}}

	require.False(t, HasAuthServerListenerTLS(mounts))
}

func TestBuildAuthServerRunConfigRejectsX509WithoutListenerTLS(t *testing.T) {
	t.Parallel()

	config, err := BuildAuthServerRunConfig("default", "server", &mcpv1beta1.EmbeddedAuthServerConfig{
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientConfig{{
			Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
		}}},
	}, nil, nil, "")

	require.Nil(t, config)
	require.EqualError(t, err, "listenerTLS is required when SPIFFE X.509 client authentication is configured")
}
