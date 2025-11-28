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

// MergePodTemplateSpecs merges a default PodTemplateSpec with a user-provided one.
// User-provided values take precedence over defaults. This allows users to customize
// infrastructure concerns while ensuring sensible defaults are applied where values
// are not specified.
//
// The merge strategy starts with the user's PodTemplateSpec and fills in defaults
// only where the user hasn't specified values. This means any field the user sets
// (affinity, tolerations, nodeSelector, etc.) is automatically preserved.
//
// Merge behavior:
//   - Labels/Annotations: Merged, with defaults added for missing keys
//   - ServiceAccountName: Default only if user hasn't specified
//   - Containers: Merged by name - defaults fill in missing container fields
//   - Volumes: Merged by name - defaults added only if not present
//   - All other PodSpec fields: User values preserved as-is
func MergePodTemplateSpecs(defaultPTS, userPTS *corev1.PodTemplateSpec) corev1.PodTemplateSpec {
	if userPTS == nil {
		if defaultPTS == nil {
			return corev1.PodTemplateSpec{}
		}
		return *defaultPTS.DeepCopy()
	}

	if defaultPTS == nil {
		return *userPTS.DeepCopy()
	}

	// Start with a deep copy of the user's spec - this preserves all user fields automatically
	result := userPTS.DeepCopy()

	// Merge labels: add default labels that user hasn't specified
	result.Labels = mergeStringMapsDefaultsFirst(defaultPTS.Labels, result.Labels)

	// Merge annotations: add default annotations that user hasn't specified
	result.Annotations = mergeStringMapsDefaultsFirst(defaultPTS.Annotations, result.Annotations)

	// Set service account only if user hasn't specified one
	if result.Spec.ServiceAccountName == "" {
		result.Spec.ServiceAccountName = defaultPTS.Spec.ServiceAccountName
	}

	// Merge containers: user containers take precedence, defaults fill gaps
	result.Spec.Containers = mergeContainersUserFirst(defaultPTS.Spec.Containers, result.Spec.Containers)

	// Merge init containers
	result.Spec.InitContainers = mergeContainersUserFirst(defaultPTS.Spec.InitContainers, result.Spec.InitContainers)

	// Merge volumes: add default volumes that user hasn't specified
	result.Spec.Volumes = mergeVolumesUserFirst(defaultPTS.Spec.Volumes, result.Spec.Volumes)

	return *result
}

// mergeContainersUserFirst merges containers where user containers take precedence.
// User containers are preserved, and default container fields fill in gaps.
func mergeContainersUserFirst(defaults, user []corev1.Container) []corev1.Container {
	if len(user) == 0 {
		return defaults
	}
	if len(defaults) == 0 {
		return user
	}

	// Create a map of default containers by name
	defaultMap := make(map[string]corev1.Container)
	for _, c := range defaults {
		defaultMap[c.Name] = c
	}

	// Start with user containers, filling in defaults where needed
	result := make([]corev1.Container, 0, len(user)+len(defaults))
	merged := make(map[string]bool)

	for _, userContainer := range user {
		if defaultContainer, exists := defaultMap[userContainer.Name]; exists {
			// Merge: user values take precedence, defaults fill gaps
			result = append(result, mergeContainer(defaultContainer, userContainer))
			merged[userContainer.Name] = true
		} else {
			// User container with no default - keep as-is
			result = append(result, userContainer)
		}
	}

	// Add default containers that user didn't specify
	for _, defaultContainer := range defaults {
		if !merged[defaultContainer.Name] {
			result = append(result, defaultContainer)
		}
	}

	return result
}

// mergeContainer merges a default container with a user container.
// User values take precedence; defaults fill in where user hasn't specified.
func mergeContainer(defaultContainer, userContainer corev1.Container) corev1.Container {
	// Start with user container - preserves all user-specified fields
	result := userContainer

	// Fill in defaults only where user hasn't specified
	if result.Image == "" {
		result.Image = defaultContainer.Image
	}
	if len(result.Command) == 0 {
		result.Command = defaultContainer.Command
	}
	if len(result.Args) == 0 {
		result.Args = defaultContainer.Args
	}
	if result.WorkingDir == "" {
		result.WorkingDir = defaultContainer.WorkingDir
	}
	if isResourcesEmpty(result.Resources) {
		result.Resources = defaultContainer.Resources
	}
	if result.LivenessProbe == nil {
		result.LivenessProbe = defaultContainer.LivenessProbe
	}
	if result.ReadinessProbe == nil {
		result.ReadinessProbe = defaultContainer.ReadinessProbe
	}
	if result.StartupProbe == nil {
		result.StartupProbe = defaultContainer.StartupProbe
	}
	if result.SecurityContext == nil {
		result.SecurityContext = defaultContainer.SecurityContext
	}
	if result.ImagePullPolicy == "" {
		result.ImagePullPolicy = defaultContainer.ImagePullPolicy
	}

	// Merge slices: add defaults that user hasn't specified
	result.Ports = mergePortsUserFirst(defaultContainer.Ports, result.Ports)
	result.Env = mergeEnvVarsUserFirst(defaultContainer.Env, result.Env)
	result.VolumeMounts = mergeVolumeMountsUserFirst(defaultContainer.VolumeMounts, result.VolumeMounts)

	return result
}

// mergeVolumesUserFirst merges volumes where user volumes take precedence.
func mergeVolumesUserFirst(defaults, user []corev1.Volume) []corev1.Volume {
	if len(user) == 0 {
		return defaults
	}
	if len(defaults) == 0 {
		return user
	}

	// Create a map of user volumes by name
	userMap := make(map[string]bool)
	for _, v := range user {
		userMap[v.Name] = true
	}

	// Start with user volumes
	result := make([]corev1.Volume, 0, len(user)+len(defaults))
	result = append(result, user...)

	// Add default volumes that user hasn't specified
	for _, defaultVolume := range defaults {
		if !userMap[defaultVolume.Name] {
			result = append(result, defaultVolume)
		}
	}

	return result
}

// mergePortsUserFirst merges ports where user ports take precedence.
func mergePortsUserFirst(defaults, user []corev1.ContainerPort) []corev1.ContainerPort {
	if len(user) == 0 {
		return defaults
	}
	if len(defaults) == 0 {
		return user
	}

	// Track user ports by name and port number
	userByName := make(map[string]bool)
	userByPort := make(map[int32]bool)
	for _, p := range user {
		if p.Name != "" {
			userByName[p.Name] = true
		}
		userByPort[p.ContainerPort] = true
	}

	// Start with user ports
	result := make([]corev1.ContainerPort, 0, len(user)+len(defaults))
	result = append(result, user...)

	// Add default ports that user hasn't specified
	for _, defaultPort := range defaults {
		nameConflict := defaultPort.Name != "" && userByName[defaultPort.Name]
		portConflict := userByPort[defaultPort.ContainerPort]
		if !nameConflict && !portConflict {
			result = append(result, defaultPort)
		}
	}

	return result
}

// mergeEnvVarsUserFirst merges env vars where user env vars take precedence.
func mergeEnvVarsUserFirst(defaults, user []corev1.EnvVar) []corev1.EnvVar {
	if len(user) == 0 {
		return defaults
	}
	if len(defaults) == 0 {
		return user
	}

	// Create a map of user env vars by name
	userMap := make(map[string]bool)
	for _, e := range user {
		userMap[e.Name] = true
	}

	// Start with user env vars
	result := make([]corev1.EnvVar, 0, len(user)+len(defaults))
	result = append(result, user...)

	// Add default env vars that user hasn't specified
	for _, defaultEnv := range defaults {
		if !userMap[defaultEnv.Name] {
			result = append(result, defaultEnv)
		}
	}

	return result
}

// mergeVolumeMountsUserFirst merges volume mounts where user mounts take precedence.
func mergeVolumeMountsUserFirst(defaults, user []corev1.VolumeMount) []corev1.VolumeMount {
	if len(user) == 0 {
		return defaults
	}
	if len(defaults) == 0 {
		return user
	}

	// Create a map of user volume mounts by name
	userMap := make(map[string]bool)
	for _, m := range user {
		userMap[m.Name] = true
	}

	// Start with user mounts
	result := make([]corev1.VolumeMount, 0, len(user)+len(defaults))
	result = append(result, user...)

	// Add default mounts that user hasn't specified
	for _, defaultMount := range defaults {
		if !userMap[defaultMount.Name] {
			result = append(result, defaultMount)
		}
	}

	return result
}

// mergeStringMapsDefaultsFirst merges string maps where user values override defaults.
// Returns a map with all default keys, plus any additional user keys, with user values taking precedence.
func mergeStringMapsDefaultsFirst(defaults, user map[string]string) map[string]string {
	if len(defaults) == 0 && len(user) == 0 {
		return nil
	}

	result := make(map[string]string)
	for k, v := range defaults {
		result[k] = v
	}
	for k, v := range user {
		result[k] = v // User values override defaults
	}
	return result
}

// isResourcesEmpty checks if ResourceRequirements are empty.
func isResourcesEmpty(resources corev1.ResourceRequirements) bool {
	return len(resources.Requests) == 0 && len(resources.Limits) == 0
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
