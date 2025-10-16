package checksum

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
