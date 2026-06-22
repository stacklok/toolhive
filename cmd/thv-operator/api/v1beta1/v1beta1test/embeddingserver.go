// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1test

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// EmbeddingServerOption mutates an EmbeddingServer under construction.
type EmbeddingServerOption func(*mcpv1beta1.EmbeddingServer)

// NewEmbeddingServer returns an EmbeddingServer with test defaults (the model,
// image, and port the suite most commonly uses), customized by the supplied
// options.
//
// Its options are prefixed Embedding to coexist with the other workload builders
// in this package, which share field names (Image, Args, Env, Replicas, …) that
// Go will not let us overload.
func NewEmbeddingServer(name, namespace string, opts ...EmbeddingServerOption) *mcpv1beta1.EmbeddingServer {
	e := &mcpv1beta1.EmbeddingServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1beta1.EmbeddingServerSpec{
			Model: "sentence-transformers/all-MiniLM-L6-v2",
			Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
			Port:  8080,
		},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// WithEmbeddingModel overrides the HuggingFace embedding model.
func WithEmbeddingModel(model string) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.Model = model }
}

// WithEmbeddingImage overrides the container image.
func WithEmbeddingImage(image string) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.Image = image }
}

// WithEmbeddingPort overrides the service port.
func WithEmbeddingPort(port int32) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.Port = port }
}

// WithEmbeddingReplicas sets the desired replica count.
func WithEmbeddingReplicas(replicas int32) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.Replicas = &replicas }
}

// WithEmbeddingArgs sets the inference server args.
func WithEmbeddingArgs(args ...string) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.Args = args }
}

// WithEmbeddingEnv replaces the environment variables.
func WithEmbeddingEnv(env ...mcpv1beta1.EnvVar) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.Env = env }
}

// WithEmbeddingModelCache sets the model cache configuration.
func WithEmbeddingModelCache(cfg *mcpv1beta1.ModelCacheConfig) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.ModelCache = cfg }
}

// WithEmbeddingImagePullPolicy sets the image pull policy.
func WithEmbeddingImagePullPolicy(policy corev1.PullPolicy) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.ImagePullPolicy = policy }
}

// WithEmbeddingHFTokenSecretRef sets the HuggingFace token secret reference.
func WithEmbeddingHFTokenSecretRef(ref *mcpv1beta1.SecretKeyRef) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.HFTokenSecretRef = ref }
}

// WithEmbeddingPodTemplateSpec sets the raw pod template spec override.
func WithEmbeddingPodTemplateSpec(pts *runtime.RawExtension) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Spec.PodTemplateSpec = pts }
}

// WithEmbeddingStatus replaces the EmbeddingServer status.
func WithEmbeddingStatus(status mcpv1beta1.EmbeddingServerStatus) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) { e.Status = status }
}

// WithEmbeddingDeletionTimestamp marks the server as being deleted (with the
// given finalizers so the fake client accepts the non-zero timestamp).
func WithEmbeddingDeletionTimestamp(ts metav1.Time, finalizers ...string) EmbeddingServerOption {
	return func(e *mcpv1beta1.EmbeddingServer) {
		e.DeletionTimestamp = &ts
		e.Finalizers = finalizers
	}
}

// MutateEmbedding is the escape hatch for spec or metadata fields that have no
// dedicated option (Resources, ResourceOverrides, metadata.UID/Labels, …). It
// runs in option order. Prefer a dedicated option when one exists; keep
// genuinely complex fixtures as inline literals.
func MutateEmbedding(fn func(*mcpv1beta1.EmbeddingServer)) EmbeddingServerOption {
	return fn
}
