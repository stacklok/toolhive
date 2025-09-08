package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComputeConfigMapChecksum(t *testing.T) {
	t.Parallel()

	// Create a test ConfigMap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
			Labels: map[string]string{
				"app": "test",
			},
			Annotations: map[string]string{
				"test.io/annotation": "value",
			},
		},
		Data: map[string]string{
			"runconfig.json": `{"name":"test","image":"test:latest"}`,
			"other.json":     `{"key":"value"}`,
		},
	}

	checksum1 := ComputeConfigMapChecksum(configMap)
	assert.NotEmpty(t, checksum1)
	assert.Len(t, checksum1, 64) // SHA256 hex string length

	// Same ConfigMap should produce same checksum
	checksum2 := ComputeConfigMapChecksum(configMap)
	assert.Equal(t, checksum1, checksum2)

	// Different data should produce different checksum
	configMap.Data["runconfig.json"] = `{"name":"test","image":"test:v2"}`
	checksum3 := ComputeConfigMapChecksum(configMap)
	assert.NotEqual(t, checksum1, checksum3)

	// Adding existing checksum annotation should not affect checksum
	configMap.Annotations["toolhive.stacklok.dev/content-checksum"] = "existing-checksum"
	checksum4 := ComputeConfigMapChecksum(configMap)
	assert.Equal(t, checksum3, checksum4)
}
