// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
)

// TestEmbeddingServer_DefaultImagePullSecrets verifies that cluster-wide
// chart defaults reach the StatefulSet's PodSpec.ImagePullSecrets.
//
// EmbeddingServer has no per-CR imagePullSecrets field; users add their own
// entries via spec.podTemplateSpec.spec.imagePullSecrets, which is
// strategic-merged on top of this base list. The strategic-merge behavior
// (additive union keyed by Name) is exercised by integration tests against a
// real K8s API; here we only assert the chart defaults reach the base PodSpec.
func TestEmbeddingServer_DefaultImagePullSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		defaults    []string
		wantSecrets []corev1.LocalObjectReference
	}{
		{
			name:     "chart defaults reach base PodSpec",
			defaults: []string{"chart-default", "second-default"},
			wantSecrets: []corev1.LocalObjectReference{
				{Name: "chart-default"},
				{Name: "second-default"},
			},
		},
		{
			name:        "no defaults yields nil ImagePullSecrets",
			defaults:    nil,
			wantSecrets: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedding := createTestEmbeddingServer(
				"default-pullsecrets-embed",
				testNamespaceDefault,
				"image:latest",
				"model",
			)

			scheme := createEmbeddingServerTestScheme()
			reconciler := &EmbeddingServerReconciler{
				Scheme:                   scheme,
				PlatformDetector:         ctrlutil.NewSharedPlatformDetector(),
				ImagePullSecretsDefaults: imagepullsecrets.NewDefaults(tt.defaults),
			}

			sts := reconciler.statefulSetForEmbedding(t.Context(), embedding)
			require.NotNil(t, sts)
			assert.Equal(t, tt.wantSecrets, sts.Spec.Template.Spec.ImagePullSecrets,
				"StatefulSet PodSpec ImagePullSecrets must reflect chart defaults")
		})
	}
}
