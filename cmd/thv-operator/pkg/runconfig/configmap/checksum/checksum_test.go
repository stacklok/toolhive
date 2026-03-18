// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package checksum

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestComputeConfigMapChecksum tests the checksum computation
func TestComputeConfigMapChecksum(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		cm1                *corev1.ConfigMap
		cm2                *corev1.ConfigMap
		sameShouldChecksum bool
	}{
		{
			name: "identical configmaps have same checksum",
			cm1: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"key": "value"},
					Annotations: map[string]string{"other": "annotation"},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			cm2: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"key": "value"},
					Annotations: map[string]string{"other": "annotation"},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			sameShouldChecksum: true,
		},
		{
			name: "different data content produces different checksum",
			cm1: &corev1.ConfigMap{
				Data: map[string]string{"runconfig.json": "content1"},
			},
			cm2: &corev1.ConfigMap{
				Data: map[string]string{"runconfig.json": "content2"},
			},
			sameShouldChecksum: false,
		},
		{
			name: "different labels produce different checksum",
			cm1: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "value1"},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			cm2: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "value2"},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			sameShouldChecksum: false,
		},
		{
			name: "checksum annotation is ignored in computation",
			cm1: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other":                                  "annotation",
						"toolhive.stacklok.dev/content-checksum": "checksum1",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			cm2: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other":                                  "annotation",
						"toolhive.stacklok.dev/content-checksum": "checksum2",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			sameShouldChecksum: true, // Should be same because checksum annotation is ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := &runConfigConfigMapChecksum{}
			checksum1 := cs.ComputeConfigMapChecksum(tt.cm1)
			checksum2 := cs.ComputeConfigMapChecksum(tt.cm2)

			assert.NotEmpty(t, checksum1)
			assert.NotEmpty(t, checksum2)

			if tt.sameShouldChecksum {
				assert.Equal(t, checksum1, checksum2)
			} else {
				assert.NotEqual(t, checksum1, checksum2)
			}
		})
	}
}

// TestConfigMapChecksumHasChanged tests the checksum change detection logic
func TestConfigMapChecksumHasChanged(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		current  *corev1.ConfigMap
		desired  *corev1.ConfigMap
		expected bool // true if changed, false if not changed
	}{
		{
			name: "identical content with same checksum - no change",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "value"},
					Annotations: map[string]string{
						"other":                                  "annotation",
						"toolhive.stacklok.dev/content-checksum": "samechecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "value"},
					Annotations: map[string]string{
						"other":                                  "annotation",
						"toolhive.stacklok.dev/content-checksum": "samechecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			expected: false, // No change - checksums are the same
		},
		{
			name: "different data content - has changed",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "oldchecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "old-content"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "newchecksum456",
					},
				},
				Data: map[string]string{"runconfig.json": "new-content"},
			},
			expected: true, // Changed - checksums are different
		},
		{
			name: "different labels - has changed",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "old-value"},
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "oldchecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "new-value"},
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "newchecksum456",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			expected: true, // Changed - checksums are different
		},
		{
			name: "different non-checksum annotations - has changed",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other":                                  "old-annotation",
						"toolhive.stacklok.dev/content-checksum": "oldchecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other":                                  "new-annotation",
						"toolhive.stacklok.dev/content-checksum": "newchecksum456",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			expected: true, // Changed - checksums are different
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := &runConfigConfigMapChecksum{}
			result := cs.ConfigMapChecksumHasChanged(tt.current, tt.desired)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHashRawJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input1      []byte
		input2      []byte
		sameHash    bool
		expectError bool
	}{
		{
			name:     "same fields different order produce same hash",
			input1:   []byte(`{"b":"2","a":"1"}`),
			input2:   []byte(`{"a":"1","b":"2"}`),
			sameHash: true,
		},
		{
			name:     "nested objects with different order produce same hash",
			input1:   []byte(`{"spec":{"nodeSelector":{"disktype":"ssd"},"priorityClassName":"high"}}`),
			input2:   []byte(`{"spec":{"priorityClassName":"high","nodeSelector":{"disktype":"ssd"}}}`),
			sameHash: true,
		},
		{
			name:     "different values produce different hash",
			input1:   []byte(`{"a":"1"}`),
			input2:   []byte(`{"a":"2"}`),
			sameHash: false,
		},
		{
			name:        "invalid JSON returns error",
			input1:      []byte(`not-json`),
			input2:      []byte(`{}`),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hash1, err1 := HashRawJSON(tt.input1)
			if tt.expectError {
				require.Error(t, err1)
				return
			}
			require.NoError(t, err1)

			hash2, err2 := HashRawJSON(tt.input2)
			require.NoError(t, err2)

			assert.NotEmpty(t, hash1)
			assert.NotEmpty(t, hash2)

			if tt.sameHash {
				assert.Equal(t, hash1, hash2)
			} else {
				assert.NotEqual(t, hash1, hash2)
			}
		})
	}
}
