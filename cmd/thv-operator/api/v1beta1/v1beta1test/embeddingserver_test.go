// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1test_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
)

func TestNewEmbeddingServer_Defaults(t *testing.T) {
	t.Parallel()

	e := v1beta1test.NewEmbeddingServer("embed", "default")

	assert.Equal(t, "embed", e.Name)
	assert.Equal(t, "default", e.Namespace)
	assert.Equal(t, "sentence-transformers/all-MiniLM-L6-v2", e.Spec.Model)
	assert.Equal(t, "ghcr.io/huggingface/text-embeddings-inference:latest", e.Spec.Image)
	assert.Equal(t, int32(8080), e.Spec.Port)
}

func TestNewEmbeddingServer_Options(t *testing.T) {
	t.Parallel()

	e := v1beta1test.NewEmbeddingServer("embed", "toolhive",
		v1beta1test.WithEmbeddingModel("test-model"),
		v1beta1test.WithEmbeddingImage("image:v1"),
		v1beta1test.WithEmbeddingPort(9000),
		v1beta1test.WithEmbeddingReplicas(2),
		v1beta1test.WithEmbeddingImagePullPolicy(corev1.PullAlways),
		v1beta1test.WithEmbeddingArgs("--flag"),
		v1beta1test.WithEmbeddingEnv(mcpv1beta1.EnvVar{Name: "FOO", Value: "bar"}),
	)

	assert.Equal(t, "test-model", e.Spec.Model)
	assert.Equal(t, "image:v1", e.Spec.Image)
	assert.Equal(t, int32(9000), e.Spec.Port)
	require.NotNil(t, e.Spec.Replicas)
	assert.Equal(t, int32(2), *e.Spec.Replicas)
	assert.Equal(t, corev1.PullAlways, e.Spec.ImagePullPolicy)
	assert.Equal(t, []string{"--flag"}, e.Spec.Args)
	assert.Len(t, e.Spec.Env, 1)
}

func TestNewEmbeddingServer_Mutate(t *testing.T) {
	t.Parallel()

	e := v1beta1test.NewEmbeddingServer("embed", "ns",
		v1beta1test.WithEmbeddingModelCache(&mcpv1beta1.ModelCacheConfig{Enabled: true, Size: "5Gi"}),
		v1beta1test.MutateEmbedding(func(e *mcpv1beta1.EmbeddingServer) {
			e.Spec.Model = "from-mutate"
		}),
	)

	require.NotNil(t, e.Spec.ModelCache)
	assert.Equal(t, "5Gi", e.Spec.ModelCache.Size)
	assert.Equal(t, "from-mutate", e.Spec.Model)
}
