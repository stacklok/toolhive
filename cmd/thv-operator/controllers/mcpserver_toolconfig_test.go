// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerReconciler_mapToolConfigToServers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	servers := []runtime.Object{
		v1beta1test.NewMCPServer("s1", "default",
			v1beta1test.WithToolConfigRef("test-config"),
		),
		v1beta1test.NewMCPServer("s2", "default",
			v1beta1test.WithToolConfigRef("other-config"),
		),
		v1beta1test.NewMCPServer("s3", "other-ns",
			v1beta1test.WithToolConfigRef("test-config"),
		),
		v1beta1test.NewMCPServer("s4", "default"), // No reference
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(servers...).
		Build()

	r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	t.Run("valid ToolConfig", func(t *testing.T) {
		t.Parallel()
		config := &mcpv1beta1.MCPToolConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default"},
		}

		reqs := r.mapToolConfigToServers(ctx, config)
		require.Len(t, reqs, 1)
		assert.Equal(t, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "s1", Namespace: "default"},
		}, reqs[0])
	})

	t.Run("wrong object type", func(t *testing.T) {
		t.Parallel()
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
		}
		reqs := r.mapToolConfigToServers(ctx, pod)
		assert.Nil(t, reqs)
	})
}
