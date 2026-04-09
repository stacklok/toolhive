// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// marshalToRawJSON marshals a value to apiextensionsv1.JSON for test input construction.
func marshalToRawJSON(t *testing.T, v any) apiextensionsv1.JSON {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return apiextensionsv1.JSON{Raw: data}
}

func TestParseVolumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		volumes []apiextensionsv1.JSON
		assert  func(t *testing.T, got []corev1.Volume)
		wantErr string
	}{
		{
			name:    "empty volumes returns empty result",
			volumes: nil,
			assert: func(t *testing.T, got []corev1.Volume) {
				t.Helper()
				assert.Empty(t, got)
			},
		},
		{
			name: "valid volume with configMap source",
			volumes: []apiextensionsv1.JSON{
				marshalToRawJSON(t, corev1.Volume{
					Name: "my-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "my-cm"},
						},
					},
				}),
			},
			assert: func(t *testing.T, got []corev1.Volume) {
				t.Helper()
				require.Len(t, got, 1)
				assert.Equal(t, "my-config", got[0].Name)
				require.NotNil(t, got[0].ConfigMap)
				assert.Equal(t, "my-cm", got[0].ConfigMap.Name)
			},
		},
		{
			name: "invalid JSON returns error",
			volumes: []apiextensionsv1.JSON{
				{Raw: []byte(`{not valid json}`)},
			},
			wantErr: "failed to unmarshal volumes[0]",
		},
		{
			name: "multiple volumes all deserialize correctly",
			volumes: []apiextensionsv1.JSON{
				marshalToRawJSON(t, corev1.Volume{
					Name: "vol-a",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				}),
				marshalToRawJSON(t, corev1.Volume{
					Name: "vol-b",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: "my-secret"},
					},
				}),
			},
			assert: func(t *testing.T, got []corev1.Volume) {
				t.Helper()
				require.Len(t, got, 2)
				assert.Equal(t, "vol-a", got[0].Name)
				require.NotNil(t, got[0].EmptyDir)
				assert.Equal(t, "vol-b", got[1].Name)
				require.NotNil(t, got[1].Secret)
				assert.Equal(t, "my-secret", got[1].Secret.SecretName)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := &MCPRegistrySpec{Volumes: tt.volumes}
			got, err := spec.ParseVolumes()

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			tt.assert(t, got)
		})
	}
}

func TestParseVolumeMounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mounts  []apiextensionsv1.JSON
		assert  func(t *testing.T, got []corev1.VolumeMount)
		wantErr string
	}{
		{
			name:   "empty volume mounts returns empty result",
			mounts: nil,
			assert: func(t *testing.T, got []corev1.VolumeMount) {
				t.Helper()
				assert.Empty(t, got)
			},
		},
		{
			name: "valid volume mount deserializes correctly",
			mounts: []apiextensionsv1.JSON{
				marshalToRawJSON(t, corev1.VolumeMount{
					Name:      "my-mount",
					MountPath: "/data",
					ReadOnly:  true,
				}),
			},
			assert: func(t *testing.T, got []corev1.VolumeMount) {
				t.Helper()
				require.Len(t, got, 1)
				assert.Equal(t, "my-mount", got[0].Name)
				assert.Equal(t, "/data", got[0].MountPath)
				assert.True(t, got[0].ReadOnly)
			},
		},
		{
			name: "invalid JSON returns error",
			mounts: []apiextensionsv1.JSON{
				{Raw: []byte(`[broken`)},
			},
			wantErr: "failed to unmarshal volumeMounts[0]",
		},
		{
			name: "multiple volume mounts all deserialize correctly",
			mounts: []apiextensionsv1.JSON{
				marshalToRawJSON(t, corev1.VolumeMount{
					Name:      "mount-a",
					MountPath: "/a",
				}),
				marshalToRawJSON(t, corev1.VolumeMount{
					Name:      "mount-b",
					MountPath: "/b",
					ReadOnly:  true,
				}),
			},
			assert: func(t *testing.T, got []corev1.VolumeMount) {
				t.Helper()
				require.Len(t, got, 2)
				assert.Equal(t, "mount-a", got[0].Name)
				assert.Equal(t, "/a", got[0].MountPath)
				assert.False(t, got[0].ReadOnly)
				assert.Equal(t, "mount-b", got[1].Name)
				assert.Equal(t, "/b", got[1].MountPath)
				assert.True(t, got[1].ReadOnly)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := &MCPRegistrySpec{VolumeMounts: tt.mounts}
			got, err := spec.ParseVolumeMounts()

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			tt.assert(t, got)
		})
	}
}
