// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestGenerateAuthServerVolumesSPIFFEFileBundles(t *testing.T) {
	t.Parallel()

	authConfig := &mcpv1beta1.EmbeddedAuthServerConfig{
		SPIFFETrustDomains: []mcpv1beta1.SPIFFETrustDomainConfig{
			{
				BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
					Type: mcpv1beta1.SPIFFEBundleSourceTypeFile,
					File: &mcpv1beta1.SPIFFEFileBundleSourceConfig{
						ConfigMapName: "bundle-one",
						ConfigMapKey:  "trust-bundle.json",
					},
				},
			},
			{
				BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
					Type: mcpv1beta1.SPIFFEBundleSourceTypeFile,
					File: &mcpv1beta1.SPIFFEFileBundleSourceConfig{
						ConfigMapName: "bundle-two",
						ConfigMapKey:  "trust-bundle.json",
					},
				},
			},
		},
	}

	volumes, mounts := GenerateAuthServerVolumes(authConfig)
	require.Len(t, volumes, 2)
	require.Len(t, mounts, 2)
	for i := range volumes {
		require.NotNil(t, volumes[i].ConfigMap)
		require.Equal(t, AuthServerSPIFFEBundleFileName, volumes[i].ConfigMap.Items[0].Path)
		require.Equal(t, "trust-bundle.json", volumes[i].ConfigMap.Items[0].Key)
		index := strconv.Itoa(i)
		require.Equal(t, AuthServerSPIFFEBundleVolumePrefix+index, volumes[i].Name)
		require.True(t, mounts[i].ReadOnly)
		require.Empty(t, mounts[i].SubPath)
		require.Equal(t, AuthServerSPIFFEBundleMountPath+"/"+index, mounts[i].MountPath)
	}
}
