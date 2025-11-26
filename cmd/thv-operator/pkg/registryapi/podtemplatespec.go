// Package registryapi provides deployment management for the registry API component.
package registryapi

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
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
		pts.Spec.Volumes = append(pts.Spec.Volumes, volume)
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
