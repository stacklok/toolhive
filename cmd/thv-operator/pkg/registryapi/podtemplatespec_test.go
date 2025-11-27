package registryapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
)

func TestNewPodTemplateSpecBuilder(t *testing.T) {
	t.Parallel()

	builder := NewPodTemplateSpecBuilder()

	assert.NotNil(t, builder)
	assert.NotNil(t, builder.podTemplateSpec)
}

func TestPodTemplateSpecBuilder_Apply_And_Build(t *testing.T) {
	t.Parallel()

	builder := NewPodTemplateSpecBuilder()
	pts := builder.Apply(
		WithLabels(map[string]string{"app": "test"}),
		WithServiceAccountName("test-sa"),
	).Build()

	assert.Equal(t, "test", pts.Labels["app"])
	assert.Equal(t, "test-sa", pts.Spec.ServiceAccountName)
}

func TestWithLabels(t *testing.T) {
	t.Parallel()

	builder := NewPodTemplateSpecBuilder()
	pts := builder.Apply(
		WithLabels(map[string]string{"key1": "value1", "key2": "value2"}),
	).Build()

	assert.Equal(t, "value1", pts.Labels["key1"])
	assert.Equal(t, "value2", pts.Labels["key2"])
}

func TestWithAnnotations(t *testing.T) {
	t.Parallel()

	builder := NewPodTemplateSpecBuilder()
	pts := builder.Apply(
		WithAnnotations(map[string]string{"anno1": "val1"}),
	).Build()

	assert.Equal(t, "val1", pts.Annotations["anno1"])
}

func TestWithServiceAccountName(t *testing.T) {
	t.Parallel()

	builder := NewPodTemplateSpecBuilder()
	pts := builder.Apply(
		WithServiceAccountName("my-service-account"),
	).Build()

	assert.Equal(t, "my-service-account", pts.Spec.ServiceAccountName)
}

func TestWithContainer(t *testing.T) {
	t.Parallel()

	container := corev1.Container{
		Name:  "test-container",
		Image: "test-image:latest",
	}

	builder := NewPodTemplateSpecBuilder()
	pts := builder.Apply(
		WithContainer(container),
	).Build()

	require.Len(t, pts.Spec.Containers, 1)
	assert.Equal(t, "test-container", pts.Spec.Containers[0].Name)
	assert.Equal(t, "test-image:latest", pts.Spec.Containers[0].Image)
}

func TestWithVolume(t *testing.T) {
	t.Parallel()

	t.Run("adds volume", func(t *testing.T) {
		t.Parallel()

		volume := corev1.Volume{
			Name: "test-volume",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithVolume(volume),
		).Build()

		require.Len(t, pts.Spec.Volumes, 1)
		assert.Equal(t, "test-volume", pts.Spec.Volumes[0].Name)
	})

	t.Run("idempotent - does not add duplicate volume", func(t *testing.T) {
		t.Parallel()

		volume := corev1.Volume{
			Name: "test-volume",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithVolume(volume),
			WithVolume(volume),
		).Build()

		assert.Len(t, pts.Spec.Volumes, 1)
	})
}

func TestWithVolumeMount(t *testing.T) {
	t.Parallel()

	t.Run("adds volume mount to existing container", func(t *testing.T) {
		t.Parallel()

		container := corev1.Container{Name: "my-container"}
		mount := corev1.VolumeMount{Name: "my-mount", MountPath: "/data"}

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithContainer(container),
			WithVolumeMount("my-container", mount),
		).Build()

		require.Len(t, pts.Spec.Containers, 1)
		require.Len(t, pts.Spec.Containers[0].VolumeMounts, 1)
		assert.Equal(t, "my-mount", pts.Spec.Containers[0].VolumeMounts[0].Name)
	})

	t.Run("does nothing if container not found", func(t *testing.T) {
		t.Parallel()

		mount := corev1.VolumeMount{Name: "my-mount", MountPath: "/data"}

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithVolumeMount("nonexistent", mount),
		).Build()

		assert.Empty(t, pts.Spec.Containers)
	})

	t.Run("idempotent - does not add duplicate mount", func(t *testing.T) {
		t.Parallel()

		container := corev1.Container{Name: "my-container"}
		mount := corev1.VolumeMount{Name: "my-mount", MountPath: "/data"}

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithContainer(container),
			WithVolumeMount("my-container", mount),
			WithVolumeMount("my-container", mount),
		).Build()

		assert.Len(t, pts.Spec.Containers[0].VolumeMounts, 1)
	})
}

func TestWithContainerArgs(t *testing.T) {
	t.Parallel()

	t.Run("sets args on existing container", func(t *testing.T) {
		t.Parallel()

		container := corev1.Container{Name: "my-container"}

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithContainer(container),
			WithContainerArgs("my-container", []string{"--flag", "value"}),
		).Build()

		assert.Equal(t, []string{"--flag", "value"}, pts.Spec.Containers[0].Args)
	})

	t.Run("does nothing if container not found", func(t *testing.T) {
		t.Parallel()

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithContainerArgs("nonexistent", []string{"--flag"}),
		).Build()

		assert.Empty(t, pts.Spec.Containers)
	})
}

func TestWithRegistryServerConfigMount(t *testing.T) {
	t.Parallel()

	container := corev1.Container{Name: "registry-api"}

	builder := NewPodTemplateSpecBuilder()
	pts := builder.Apply(
		WithContainer(container),
		WithRegistryServerConfigMount("registry-api", "my-configmap"),
	).Build()

	// Check args were set
	require.Len(t, pts.Spec.Containers, 1)
	assert.Contains(t, pts.Spec.Containers[0].Args[0], ServeCommand)
	assert.Contains(t, pts.Spec.Containers[0].Args[1], "--config=")

	// Check volume was added
	require.Len(t, pts.Spec.Volumes, 1)
	assert.Equal(t, RegistryServerConfigVolumeName, pts.Spec.Volumes[0].Name)
	assert.Equal(t, "my-configmap", pts.Spec.Volumes[0].ConfigMap.Name)

	// Check volume mount was added
	require.Len(t, pts.Spec.Containers[0].VolumeMounts, 1)
	assert.Equal(t, RegistryServerConfigVolumeName, pts.Spec.Containers[0].VolumeMounts[0].Name)
	assert.Equal(t, config.RegistryServerConfigFilePath, pts.Spec.Containers[0].VolumeMounts[0].MountPath)
}

func TestWithRegistryStorageMount(t *testing.T) {
	t.Parallel()

	container := corev1.Container{Name: "registry-api"}

	builder := NewPodTemplateSpecBuilder()
	pts := builder.Apply(
		WithContainer(container),
		WithRegistryStorageMount("registry-api"),
	).Build()

	// Check volume was added
	require.Len(t, pts.Spec.Volumes, 1)
	assert.Equal(t, "storage-data", pts.Spec.Volumes[0].Name)
	assert.NotNil(t, pts.Spec.Volumes[0].EmptyDir)

	// Check volume mount was added
	require.Len(t, pts.Spec.Containers[0].VolumeMounts, 1)
	assert.Equal(t, "storage-data", pts.Spec.Containers[0].VolumeMounts[0].Name)
	assert.Equal(t, "/data", pts.Spec.Containers[0].VolumeMounts[0].MountPath)
}

func TestWithRegistrySourceMounts(t *testing.T) {
	t.Parallel()

	t.Run("adds mounts for registries with ConfigMapRef", func(t *testing.T) {
		t.Parallel()

		container := corev1.Container{Name: "registry-api"}
		registries := []mcpv1alpha1.MCPRegistryConfig{
			{
				Name: "reg1",
				ConfigMapRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "configmap1",
					},
					Key: "data.json",
				},
			},
			{
				Name: "reg2",
				ConfigMapRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "configmap2",
					},
					Key: "registry.json",
				},
			},
		}

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithContainer(container),
			WithRegistrySourceMounts("registry-api", registries),
		).Build()

		// Check volumes were added
		assert.Len(t, pts.Spec.Volumes, 2)

		// Check volume mounts were added
		assert.Len(t, pts.Spec.Containers[0].VolumeMounts, 2)
	})

	t.Run("skips registries without ConfigMapRef", func(t *testing.T) {
		t.Parallel()

		container := corev1.Container{Name: "registry-api"}
		registries := []mcpv1alpha1.MCPRegistryConfig{
			{
				Name:         "reg1",
				ConfigMapRef: nil, // No ConfigMapRef
			},
		}

		builder := NewPodTemplateSpecBuilder()
		pts := builder.Apply(
			WithContainer(container),
			WithRegistrySourceMounts("registry-api", registries),
		).Build()

		assert.Empty(t, pts.Spec.Volumes)
		assert.Empty(t, pts.Spec.Containers[0].VolumeMounts)
	})
}

func TestBuildRegistryAPIContainer(t *testing.T) {
	t.Parallel()

	container := BuildRegistryAPIContainer("my-image:v1.0")

	assert.Equal(t, registryAPIContainerName, container.Name)
	assert.Equal(t, "my-image:v1.0", container.Image)
	assert.Equal(t, []string{ServeCommand}, container.Args)

	// Check ports
	require.Len(t, container.Ports, 1)
	assert.Equal(t, int32(RegistryAPIPort), container.Ports[0].ContainerPort)
	assert.Equal(t, RegistryAPIPortName, container.Ports[0].Name)

	// Check resources
	assert.NotNil(t, container.Resources.Requests)
	assert.NotNil(t, container.Resources.Limits)

	// Check probes
	assert.NotNil(t, container.LivenessProbe)
	assert.NotNil(t, container.ReadinessProbe)
	assert.Equal(t, HealthCheckPath, container.LivenessProbe.HTTPGet.Path)
	assert.Equal(t, ReadinessCheckPath, container.ReadinessProbe.HTTPGet.Path)
}

func TestDefaultRegistryAPIPodTemplateSpec(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"app": "registry"}
	configHash := "abc123"

	pts := DefaultRegistryAPIPodTemplateSpec(labels, configHash)

	// Check labels
	assert.Equal(t, "registry", pts.Labels["app"])

	// Check annotations
	assert.Equal(t, configHash, pts.Annotations["toolhive.stacklok.dev/config-hash"])

	// Check service account
	assert.Equal(t, DefaultServiceAccountName, pts.Spec.ServiceAccountName)

	// Check container
	require.Len(t, pts.Spec.Containers, 1)
	assert.Equal(t, registryAPIContainerName, pts.Spec.Containers[0].Name)
}
