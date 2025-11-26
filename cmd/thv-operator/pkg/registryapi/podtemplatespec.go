// Package registryapi provides deployment management for the registry API component.
package registryapi

import (
	"fmt"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
)

// PodTemplateSpecOption is a functional option for configuring a PodTemplateSpec.
type PodTemplateSpecOption func(*corev1.PodTemplateSpec)

// PodTemplateSpecBuilder builds a PodTemplateSpec using the functional options pattern.
type PodTemplateSpecBuilder struct {
	podTemplateSpec *corev1.PodTemplateSpec
}

// NewPodTemplateSpecBuilder creates a new PodTemplateSpecBuilder with default values.
func NewPodTemplateSpecBuilder() *PodTemplateSpecBuilder {
	return &PodTemplateSpecBuilder{
		podTemplateSpec: &corev1.PodTemplateSpec{},
	}
}

// WithLabels sets the labels on the PodTemplateSpec.
func WithLabels(labels map[string]string) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		if pts.Labels == nil {
			pts.Labels = make(map[string]string)
		}
		for k, v := range labels {
			pts.Labels[k] = v
		}
	}
}

// WithAnnotations sets the annotations on the PodTemplateSpec.
func WithAnnotations(annotations map[string]string) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		if pts.Annotations == nil {
			pts.Annotations = make(map[string]string)
		}
		for k, v := range annotations {
			pts.Annotations[k] = v
		}
	}
}

// WithServiceAccountName sets the service account name for the pod.
func WithServiceAccountName(name string) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		pts.Spec.ServiceAccountName = name
	}
}

// WithContainer adds a container to the PodSpec.
func WithContainer(container corev1.Container) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		pts.Spec.Containers = append(pts.Spec.Containers, container)
	}
}

// WithVolume adds a volume to the PodSpec.
func WithVolume(volume corev1.Volume) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		// Check if volume with this name already exists for idempotency
		if !hasVolume(pts.Spec.Volumes, volume.Name) {
			pts.Spec.Volumes = append(pts.Spec.Volumes, volume)
		}
	}
}

// WithVolumeMount adds a volume mount to a specific container by name.
func WithVolumeMount(containerName string, mount corev1.VolumeMount) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		container := findContainerByName(pts.Spec.Containers, containerName)
		if container != nil {
			// Check if volume mount with this name already exists for idempotency
			if !hasVolumeMount(container.VolumeMounts, mount.Name) {
				container.VolumeMounts = append(container.VolumeMounts, mount)
			}
		}
	}
}

// WithContainerArgs sets the args for a specific container by name.
// This replaces any existing args for the container.
func WithContainerArgs(containerName string, args []string) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		container := findContainerByName(pts.Spec.Containers, containerName)
		if container != nil {
			container.Args = args
		}
	}
}

// WithRegistryServerConfigMount creates a volume and mount for the registry server config.
// This adds both the ConfigMap volume and the corresponding volume mount to the specified container.
func WithRegistryServerConfigMount(containerName, configMapName string) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		// Add the config args to the container
		configPath := filepath.Join(config.RegistryServerConfigFilePath, config.RegistryServerConfigFileName)
		WithContainerArgs(containerName, []string{
			ServeCommand,
			fmt.Sprintf("--config=%s", configPath),
		})(pts)

		// Add the ConfigMap volume
		WithVolume(corev1.Volume{
			Name: RegistryServerConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMapName,
					},
				},
			},
		})(pts)

		// Add the volume mount
		WithVolumeMount(containerName, corev1.VolumeMount{
			Name:      RegistryServerConfigVolumeName,
			MountPath: config.RegistryServerConfigFilePath,
			ReadOnly:  true,
		})(pts)
	}
}

// WithRegistryStorageMount creates an emptyDir volume and mount for registry storage.
// This adds both the emptyDir volume and the corresponding volume mount to the specified container.
func WithRegistryStorageMount(containerName string) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		storageVolumeName := "storage-data"

		// Add the emptyDir volume
		WithVolume(corev1.Volume{
			Name: storageVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})(pts)

		// Add the volume mount
		WithVolumeMount(containerName, corev1.VolumeMount{
			Name:      storageVolumeName,
			MountPath: "/data",
			ReadOnly:  false,
		})(pts)
	}
}

// WithRegistrySourceMounts creates volumes and mounts for all registry source ConfigMaps.
// This iterates through the registry sources and creates a volume and mount for each ConfigMapRef.
func WithRegistrySourceMounts(containerName string, registries []mcpv1alpha1.MCPRegistryConfig) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		for _, registry := range registries {
			if registry.ConfigMapRef != nil {
				// Create unique volume name for each ConfigMap source
				volumeName := fmt.Sprintf("registry-data-source-%s", registry.Name)

				// Add the ConfigMap volume
				WithVolume(corev1.Volume{
					Name: volumeName,
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: registry.ConfigMapRef.Name,
							},
							// Mount only the specified key as registry.json
							Items: []corev1.KeyToPath{
								{
									Key:  registry.ConfigMapRef.Key,
									Path: "registry.json",
								},
							},
						},
					},
				})(pts)

				// Add the volume mount
				// Mount path follows the pattern /config/registry/{registryName}/
				mountPath := filepath.Join(config.RegistryJSONFilePath, registry.Name)
				WithVolumeMount(containerName, corev1.VolumeMount{
					Name:      volumeName,
					MountPath: mountPath,
					ReadOnly:  true,
				})(pts)
			}
		}
	}
}

// Apply applies the given options to the PodTemplateSpecBuilder.
func (b *PodTemplateSpecBuilder) Apply(opts ...PodTemplateSpecOption) *PodTemplateSpecBuilder {
	for _, opt := range opts {
		opt(b.podTemplateSpec)
	}
	return b
}

// Build returns the configured PodTemplateSpec.
func (b *PodTemplateSpecBuilder) Build() corev1.PodTemplateSpec {
	return *b.podTemplateSpec
}

// BuildRegistryAPIContainer creates the registry-api container with default configuration.
func BuildRegistryAPIContainer(image string) corev1.Container {
	return corev1.Container{
		Name:  registryAPIContainerName,
		Image: image,
		Args: []string{
			ServeCommand,
		},
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: RegistryAPIPort,
				Name:          RegistryAPIPortName,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(DefaultCPURequest),
				corev1.ResourceMemory: resource.MustParse(DefaultMemoryRequest),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(DefaultCPULimit),
				corev1.ResourceMemory: resource.MustParse(DefaultMemoryLimit),
			},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: HealthCheckPath,
					Port: intstr.FromInt32(RegistryAPIPort),
				},
			},
			InitialDelaySeconds: LivenessInitialDelay,
			PeriodSeconds:       LivenessPeriod,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: ReadinessCheckPath,
					Port: intstr.FromInt32(RegistryAPIPort),
				},
			},
			InitialDelaySeconds: ReadinessInitialDelay,
			PeriodSeconds:       ReadinessPeriod,
		},
	}
}

// DefaultRegistryAPIPodTemplateSpec creates a default PodTemplateSpec for the registry-api.
func DefaultRegistryAPIPodTemplateSpec(labels map[string]string, configHash string) corev1.PodTemplateSpec {
	builder := NewPodTemplateSpecBuilder()
	return builder.Apply(
		WithLabels(labels),
		WithAnnotations(map[string]string{
			"toolhive.stacklok.dev/config-hash": configHash,
		}),
		WithServiceAccountName(DefaultServiceAccountName),
		WithContainer(BuildRegistryAPIContainer(getRegistryAPIImage())),
	).Build()
}

// findContainerByName finds a container by name in a slice of containers.
// Returns a pointer to the container if found, nil otherwise.
func findContainerByName(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

// hasVolume checks if a volume with the given name exists in the volumes slice.
func hasVolume(volumes []corev1.Volume, name string) bool {
	for _, volume := range volumes {
		if volume.Name == name {
			return true
		}
	}
	return false
}

// hasVolumeMount checks if a volume mount with the given name exists in the volume mounts slice.
func hasVolumeMount(volumeMounts []corev1.VolumeMount, name string) bool {
	for _, mount := range volumeMounts {
		if mount.Name == name {
			return true
		}
	}
	return false
}
