// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package registryapi provides deployment management for the registry API component.
package registryapi

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
)

// PodTemplateSpecOption is a functional option for configuring a PodTemplateSpec.
type PodTemplateSpecOption func(*corev1.PodTemplateSpec)

// PodTemplateSpecBuilder builds a PodTemplateSpec using the functional options pattern.
// When created with NewPodTemplateSpecBuilderFrom, the builder stores the user's template
// and applies options as defaults. Build() merges them with user values taking precedence.
type PodTemplateSpecBuilder struct {
	// userTemplate is the user-provided PodTemplateSpec (if any)
	userTemplate *corev1.PodTemplateSpec
	// defaultSpec is built up via Apply() with options acting as defaults
	defaultSpec *corev1.PodTemplateSpec
}

// NewPodTemplateSpecBuilder creates a new PodTemplateSpecBuilder with an empty template.
func NewPodTemplateSpecBuilder() *PodTemplateSpecBuilder {
	return NewPodTemplateSpecBuilderFrom(nil)
}

// NewPodTemplateSpecBuilderFrom creates a new PodTemplateSpecBuilder with a user-provided template.
// The user template is deep-copied to avoid mutating the original.
// Options applied via Apply() act as defaults - Build() will merge them with user values,
// where user values take precedence over defaults.
func NewPodTemplateSpecBuilderFrom(userTemplate *corev1.PodTemplateSpec) *PodTemplateSpecBuilder {
	var userCopy *corev1.PodTemplateSpec
	if userTemplate != nil {
		userCopy = userTemplate.DeepCopy()
	}
	return &PodTemplateSpecBuilder{
		userTemplate: userCopy,
		defaultSpec:  &corev1.PodTemplateSpec{},
	}
}

// Apply applies the given options to build up the default PodTemplateSpec.
func (b *PodTemplateSpecBuilder) Apply(opts ...PodTemplateSpecOption) *PodTemplateSpecBuilder {
	for _, opt := range opts {
		opt(b.defaultSpec)
	}
	return b
}

// Build returns the final PodTemplateSpec.
// If a user template was provided, merges defaults with user values (user takes precedence).
func (b *PodTemplateSpecBuilder) Build() corev1.PodTemplateSpec {
	if b.userTemplate == nil {
		return *b.defaultSpec
	}
	return MergePodTemplateSpecs(b.defaultSpec, b.userTemplate)
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

// WithInitContainer adds an init container to the PodSpec.
// If an init container with the same name already exists, it is replaced for idempotency.
func WithInitContainer(container corev1.Container) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		// Check if init container with this name already exists for idempotency
		for i, existing := range pts.Spec.InitContainers {
			if existing.Name == container.Name {
				pts.Spec.InitContainers[i] = container
				return
			}
		}
		pts.Spec.InitContainers = append(pts.Spec.InitContainers, container)
	}
}

// WithEnvVar adds an environment variable to a specific container by name.
func WithEnvVar(containerName string, envVar corev1.EnvVar) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		container := findContainerByName(pts.Spec.Containers, containerName)
		if container != nil {
			// Check if env var with this name already exists for idempotency
			for i, existing := range container.Env {
				if existing.Name == envVar.Name {
					container.Env[i] = envVar
					return
				}
			}
			container.Env = append(container.Env, envVar)
		}
	}
}

// WithPGPassMount configures the pgpass secret mounting for PostgreSQL authentication.
// Kubernetes secret volumes don't allow changing file permissions after mounting, so this
// function uses an init container to copy the file and set proper permissions.
//
// This function adds:
// 1. A volume from the secret containing the pgpass file (mounted in init container)
// 2. An emptyDir volume for the prepared pgpass file (mounted in app container)
// 3. An init container that copies the file and sets permissions (600) and ownership (65532:65532)
// 4. A volume mount in the registry-api container for the pgpass file from the emptyDir
// 5. The PGPASSFILE environment variable pointing to the mounted file
func WithPGPassMount(containerName, secretName string) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		// Add the secret volume with the pgpass file (for init container)
		WithVolume(corev1.Volume{
			Name: pgpassSecretVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secretName,
					Items: []corev1.KeyToPath{
						{
							Key:  GetPGPassSecretKey(),
							Path: pgpassFileName,
						},
					},
				},
			},
		})(pts)

		// Add the emptyDir volume for the prepared pgpass file (for app container)
		WithVolume(corev1.Volume{
			Name: pgpassVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})(pts)

		// Add init container to copy pgpass file and set permissions.
		// Using Chainguard's busybox which runs as nonroot (65532) by default,
		// so no chown is needed - the file will be owned by the same user as the app container.
		WithInitContainer(corev1.Container{
			Name:  pgpassInitContainerName,
			Image: pgpassInitContainerImage,
			Command: []string{
				"sh",
				"-c",
				fmt.Sprintf(
					"cp %s/%s %s/%s && chmod 0600 %s/%s",
					pgpassSecretMountPath, pgpassFileName,
					pgpassEmptyDirMountPath, pgpassFileName,
					pgpassEmptyDirMountPath, pgpassFileName,
				),
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      pgpassSecretVolumeName,
					MountPath: pgpassSecretMountPath,
					ReadOnly:  true,
				},
				{
					Name:      pgpassVolumeName,
					MountPath: pgpassEmptyDirMountPath,
					ReadOnly:  false,
				},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             ptr.To(true),
				AllowPrivilegeEscalation: ptr.To(false),
				ReadOnlyRootFilesystem:   ptr.To(true),
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("16Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
			},
		})(pts)

		// Add the volume mount to the registry-api container
		// Uses subPath to mount just the .pgpass file at the expected location
		WithVolumeMount(containerName, corev1.VolumeMount{
			Name:      pgpassVolumeName,
			MountPath: pgpassAppUserMountPath,
			SubPath:   pgpassFileName,
			ReadOnly:  true,
		})(pts)

		// Add the PGPASSFILE environment variable
		WithEnvVar(containerName, corev1.EnvVar{
			Name:  "PGPASSFILE",
			Value: pgpassAppUserMountPath,
		})(pts)
	}
}

// WithGitAuthMount configures secret mounting for Git authentication.
// Unlike pgpass, Git credentials don't require special file permissions (0600),
// so no init container is needed - the secret is mounted directly.
//
// This function adds:
// 1. A volume from the secret containing the password/token
// 2. A volume mount to the specified container at /secrets/{secretName}/
//
// The mount path matches what buildGitPasswordFilePath() generates in the config,
// ensuring the registry server can find the password file at the expected location.
//
// Parameters:
//   - containerName: The name of the container to add the mount to
//   - secretRef: The secret key selector referencing the password secret
func WithGitAuthMount(containerName string, secretRef corev1.SecretKeySelector) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		// Both Name and Key are validated as required by buildGitAuthConfig()
		if secretRef.Name == "" || secretRef.Key == "" {
			return
		}

		// Create a unique volume name based on the secret name
		volumeName := fmt.Sprintf("git-auth-%s", secretRef.Name)

		// Add the secret volume
		// Mount the specific key as a file
		WithVolume(corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secretRef.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  secretRef.Key,
							Path: secretRef.Key,
						},
					},
				},
			},
		})(pts)

		// Add the volume mount at /secrets/{secretName}/
		// This matches the path generated by buildGitPasswordFilePath()
		mountPath := filepath.Join(gitAuthSecretsBasePath, secretRef.Name)
		WithVolumeMount(containerName, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: mountPath,
			ReadOnly:  true,
		})(pts)
	}
}

// WithRegistrySourceMounts creates volumes and mounts for all registry sources (ConfigMap and PVC).
// Each registry source (ConfigMap or PVC) gets its own volume and mount point
// at /config/registry/{registryName}/. Multiple registries can share the same PVC
// by mounting it at different paths.
func WithRegistrySourceMounts(containerName string, registries []mcpv1alpha1.MCPRegistryConfig) PodTemplateSpecOption {
	return func(pts *corev1.PodTemplateSpec) {
		for _, registry := range registries {
			if registry.ConfigMapRef != nil {
				// ConfigMap: Create unique volume per registry
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

				// Add the volume mount at registry-specific subdirectory
				mountPath := filepath.Join(config.RegistryJSONFilePath, registry.Name)
				WithVolumeMount(containerName, corev1.VolumeMount{
					Name:      volumeName,
					MountPath: mountPath,
					ReadOnly:  true,
				})(pts)
			}

			if registry.PVCRef != nil {
				// PVC: Create unique volume per registry (same PVC can be mounted multiple times)
				// Mount at /config/registry/{registryName}/ for consistent path structure
				volumeName := fmt.Sprintf("registry-data-source-%s", registry.Name)

				// Add the PVC volume
				WithVolume(corev1.Volume{
					Name: volumeName,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: registry.PVCRef.ClaimName,
							ReadOnly:  true,
						},
					},
				})(pts)

				// Mount at registry-specific subdirectory
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

// ParsePodTemplateSpec parses a runtime.RawExtension into a PodTemplateSpec.
// Returns nil if the raw extension is nil or empty.
// Returns an error if the raw extension contains invalid PodTemplateSpec data.
func ParsePodTemplateSpec(raw *runtime.RawExtension) (*corev1.PodTemplateSpec, error) {
	if raw == nil || raw.Raw == nil || len(raw.Raw) == 0 {
		return nil, nil
	}

	var pts corev1.PodTemplateSpec
	if err := json.Unmarshal(raw.Raw, &pts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal PodTemplateSpec: %w", err)
	}

	return &pts, nil
}

// ValidatePodTemplateSpec validates a runtime.RawExtension contains valid PodTemplateSpec data.
// Returns nil if the raw extension is nil, empty, or contains valid data.
// Returns an error if the raw extension contains invalid PodTemplateSpec data.
func ValidatePodTemplateSpec(raw *runtime.RawExtension) error {
	_, err := ParsePodTemplateSpec(raw)
	return err
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
