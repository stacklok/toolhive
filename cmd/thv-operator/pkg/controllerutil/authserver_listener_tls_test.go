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

	volumes, mounts := GenerateAuthServerVolumes(&mcpv1beta1.EmbeddedAuthServerConfig{
		ListenerTLS: &mcpv1beta1.ListenerTLSConfig{
			CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "certificate"},
			PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "private-key"},
		},
	})

	require.Len(t, volumes, 2)
	require.Equal(t, AuthServerTLSVolumeName+"-cert", volumes[0].Name)
	require.Equal(t, "listener-tls", volumes[0].Secret.SecretName)
	require.Equal(t, []corev1.KeyToPath{{Key: "certificate", Path: AuthServerTLSCertFileName}}, volumes[0].Secret.Items)
	require.Equal(t, int32(0400), *volumes[0].Secret.DefaultMode)
	require.Equal(t, AuthServerTLSVolumeName+"-key", volumes[1].Name)
	require.Equal(t, []corev1.KeyToPath{{Key: "private-key", Path: AuthServerTLSKeyFileName}}, volumes[1].Secret.Items)
	require.Equal(t, int32(0400), *volumes[1].Secret.DefaultMode)
	require.Len(t, mounts, 2)
	for _, mount := range mounts {
		require.True(t, mount.ReadOnly)
	}
	require.True(t, HasAuthServerListenerTLS(mounts))
}

func TestBuildAuthServerRunConfigRejectsX509WithoutListenerTLS(t *testing.T) {
	t.Parallel()

	config, err := BuildAuthServerRunConfig("default", "server", &mcpv1beta1.EmbeddedAuthServerConfig{
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientAuthConfig{{
			Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
		}}},
	}, nil, nil, "")

	require.Nil(t, config)
	require.EqualError(t, err, "listenerTLS is required when SPIFFE X.509 client authentication is configured")
}
