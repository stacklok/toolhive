// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestGenerateAuthServerVolumesTLSListener(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		listener *mcpv1beta1.TLSListenerConfig
		want     []corev1.VolumeProjection
	}{
		{
			name: "same secret",
			listener: &mcpv1beta1.TLSListenerConfig{
				CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "certificate"},
				PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "private-key"},
			},
			want: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{
				Name: "listener-tls",
				Items: []corev1.KeyToPath{
					{Key: "certificate", Path: AuthServerTLSCertFileName},
					{Key: "private-key", Path: AuthServerTLSKeyFileName},
				},
			}}},
		},
		{
			name: "different secrets",
			listener: &mcpv1beta1.TLSListenerConfig{
				CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "certificate", Key: "tls.crt"},
				PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "private-key", Key: "tls.key"},
			},
			want: []corev1.VolumeProjection{
				{Secret: &corev1.SecretProjection{Name: "certificate", Items: []corev1.KeyToPath{{Key: "tls.crt", Path: AuthServerTLSCertFileName}}}},
				{Secret: &corev1.SecretProjection{Name: "private-key", Items: []corev1.KeyToPath{{Key: "tls.key", Path: AuthServerTLSKeyFileName}}}},
			},
		},
		{name: "nil listener"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			volumes, mounts, err := GenerateAuthServerVolumes(&mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: test.listener})
			require.NoError(t, err)
			if test.listener == nil {
				assert.Empty(t, volumes)
				assert.Empty(t, mounts)
				return
			}

			require.Len(t, volumes, 1)
			assert.Equal(t, AuthServerTLSVolumeName, volumes[0].Name)
			require.NotNil(t, volumes[0].Projected)
			assert.Equal(t, test.want, volumes[0].Projected.Sources)
			require.NotNil(t, volumes[0].Projected.DefaultMode)
			assert.Equal(t, int32(0400), *volumes[0].Projected.DefaultMode)
			require.Equal(t, []corev1.VolumeMount{{
				Name: AuthServerTLSVolumeName, MountPath: AuthServerTLSMountPath, ReadOnly: true,
			}}, mounts)
		})
	}
}

func TestBuildAuthServerRunConfigSetsTLSListener(t *testing.T) {
	t.Parallel()

	config, err := BuildAuthServerRunConfig("default", "server", &mcpv1beta1.EmbeddedAuthServerConfig{
		TLSListener: &mcpv1beta1.TLSListenerConfig{
			CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "certificate"},
			PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "private-key"},
		},
	}, nil, nil, "")
	require.NoError(t, err)
	require.NotNil(t, config.TLSListener)
	assert.Equal(t, "/etc/toolhive/authserver/tls/tls.crt", config.TLSListener.CertFile)
	assert.Equal(t, "/etc/toolhive/authserver/tls/tls.key", config.TLSListener.KeyFile)

	config, err = BuildAuthServerRunConfig("default", "server", &mcpv1beta1.EmbeddedAuthServerConfig{}, nil, nil, "")
	require.NoError(t, err)
	assert.Nil(t, config.TLSListener)
}

func TestBuildAuthServerRunConfigRejectsX509WithoutTLSListener(t *testing.T) {
	t.Parallel()

	config, err := BuildAuthServerRunConfig("default", "server", &mcpv1beta1.EmbeddedAuthServerConfig{
		InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientConfig{{
			Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
		}}},
	}, nil, nil, "")

	require.Nil(t, config)
	require.EqualError(t, err, "tlsListener is required when SPIFFE X.509 client authentication is configured")
}
