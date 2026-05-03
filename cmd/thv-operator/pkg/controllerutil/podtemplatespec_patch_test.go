// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestApplyPodTemplateSpecPatch(t *testing.T) {
	t.Parallel()

	base := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "main",
					Image: "main:v1",
				},
			},
		},
	}

	tests := []struct {
		name      string
		patch     []byte
		assertOut func(t *testing.T, out corev1.PodTemplateSpec)
		expectErr bool
	}{
		{
			name:  "nil patch is a no-op",
			patch: nil,
			assertOut: func(t *testing.T, out corev1.PodTemplateSpec) {
				t.Helper()
				assert.Equal(t, base, out)
			},
		},
		{
			name:  "empty patch is a no-op",
			patch: []byte{},
			assertOut: func(t *testing.T, out corev1.PodTemplateSpec) {
				t.Helper()
				assert.Equal(t, base, out)
			},
		},
		{
			name:  "empty object patch preserves base",
			patch: []byte(`{}`),
			assertOut: func(t *testing.T, out corev1.PodTemplateSpec) {
				t.Helper()
				require.Len(t, out.Spec.Containers, 1)
				assert.Equal(t, "main", out.Spec.Containers[0].Name)
				assert.Equal(t, "main:v1", out.Spec.Containers[0].Image)
				assert.Equal(t, "test", out.Labels["app"])
			},
		},
		{
			name:  "user fields outside the base are merged in",
			patch: []byte(`{"spec":{"imagePullSecrets":[{"name":"creds"}],"priorityClassName":"high"}}`),
			assertOut: func(t *testing.T, out corev1.PodTemplateSpec) {
				t.Helper()
				assert.Equal(t, "high", out.Spec.PriorityClassName)
				require.Len(t, out.Spec.ImagePullSecrets, 1)
				assert.Equal(t, "creds", out.Spec.ImagePullSecrets[0].Name)
				// Base container survives the merge.
				require.Len(t, out.Spec.Containers, 1)
				assert.Equal(t, "main", out.Spec.Containers[0].Name)
			},
		},
		{
			name:      "type-mismatched patch returns an error",
			patch:     []byte(`{"spec":{"containers":"not-an-array"}}`),
			expectErr: true,
		},
		{
			name:      "malformed JSON returns an error",
			patch:     []byte(`{not-json`),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := ApplyPodTemplateSpecPatch(base, tt.patch)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.assertOut(t, out)
		})
	}
}
