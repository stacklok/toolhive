// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// patchRecordingClient wraps a client.Client and records the marshaled body
// of every Patch call. Tests use it to assert the wire-level flavor of a
// patch — in particular, an optimistic-lock merge patch stamps the
// resourceVersion into the body, so its presence in the recorded body is a
// deterministic signal that MergeFromWithOptimisticLock was in effect.
//
// Patches issued via .Status().Patch do not pass through this wrapper:
// controller-runtime's subresource client is obtained from the embedded
// client.Client and has its own Patch implementation, so the recorder only
// observes spec/metadata patches on the root client.
type patchRecordingClient struct {
	client.Client
	mu      sync.Mutex
	patches []recordedPatch
}

type recordedPatch struct {
	obj  client.Object
	body string
}

func (c *patchRecordingClient) Patch(
	ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption,
) error {
	// err ignored: patch.Data is json.Marshal of a typed MCPServer, which
	// has no channels/funcs/cyclic pointers and cannot fail in practice.
	// A failure here would also break the production controller's own
	// Patch call and fire other assertions before this one.
	if data, err := patch.Data(obj); err == nil {
		c.mu.Lock()
		c.patches = append(c.patches, recordedPatch{
			obj:  obj.DeepCopyObject().(client.Object),
			body: string(data),
		})
		c.mu.Unlock()
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

// lastMCPServerPatchBody returns the body of the most recent recorded
// Patch call whose target was an *mcpv1beta1.MCPServer. Returns empty
// string if none was recorded.
func (c *patchRecordingClient) lastMCPServerPatchBody() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.patches) - 1; i >= 0; i-- {
		if _, ok := c.patches[i].obj.(*mcpv1beta1.MCPServer); ok {
			return c.patches[i].body
		}
	}
	return ""
}

// TestMCPServerSpecPatchesAreOptimisticLock asserts that each of the three
// MCPServer spec Patch call sites introduced in #4767 emits a merge-patch
// whose body carries the resourceVersion precondition. A regression from
// client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})
// to plain client.MergeFrom(orig) would drop the precondition and fail
// these assertions, independent of whether the higher-level field-
// clobber survival test still passes.
func TestMCPServerSpecPatchesAreOptimisticLock(t *testing.T) {
	t.Parallel()

	const namespace = "default"

	tests := []struct {
		name string
		// seed returns the MCPServer fixture placed in the fake client
		// before the action runs. Returning a distinct name per case
		// keeps parallel subtests from colliding on the shared fake.
		seed func() *mcpv1beta1.MCPServer
		// action triggers the reconcile path that should emit the
		// optimistic-lock Patch under test. It is invoked with a
		// recorder-backed reconciler.
		action func(t *testing.T, r *MCPServerReconciler, key types.NamespacedName)
	}{
		{
			name: "AddFinalizer",
			seed: func() *mcpv1beta1.MCPServer {
				s := createTestMCPServer("optlock-add", namespace)
				// No finalizer yet — Reconcile should add it.
				return s
			},
			action: func(t *testing.T, r *MCPServerReconciler, key types.NamespacedName) {
				t.Helper()
				_, _ = r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key})
			},
		},
		{
			name: "RemoveFinalizer",
			seed: func() *mcpv1beta1.MCPServer {
				s := createTestMCPServer("optlock-remove", namespace)
				s.Finalizers = []string{MCPServerFinalizerName}
				// DeletionTimestamp forces Reconcile into the
				// finalize branch. The fake client accepts an
				// already-set timestamp on created objects.
				now := metav1.Now()
				s.DeletionTimestamp = &now
				return s
			},
			action: func(t *testing.T, r *MCPServerReconciler, key types.NamespacedName) {
				t.Helper()
				_, _ = r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: key})
			},
		},
		{
			name: "RestartAnnotation",
			seed: func() *mcpv1beta1.MCPServer {
				s := createTestMCPServer("optlock-restart", namespace)
				s.Finalizers = []string{MCPServerFinalizerName}
				if s.Annotations == nil {
					s.Annotations = map[string]string{}
				}
				s.Annotations[RestartedAtAnnotationKey] = "2026-01-01T00:00:00Z"
				s.Annotations[RestartStrategyAnnotationKey] = "immediate"
				return s
			},
			action: func(t *testing.T, r *MCPServerReconciler, key types.NamespacedName) {
				t.Helper()
				got := &mcpv1beta1.MCPServer{}
				require.NoError(t, r.Get(context.TODO(), key, got))
				// handleRestartAnnotation is the innermost
				// function that issues the Patch under test;
				// calling it directly avoids exercising the
				// rest of Reconcile, which would issue many
				// unrelated writes.
				_, _ = r.handleRestartAnnotation(context.TODO(), got)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seeded := tc.seed()
			testScheme := createTestScheme()
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithObjects(seeded).
				WithStatusSubresource(&mcpv1beta1.MCPServer{}).
				Build()
			recorder := &patchRecordingClient{Client: fakeClient}
			reconciler := newTestMCPServerReconciler(
				recorder, testScheme, kubernetes.PlatformKubernetes)

			tc.action(t, reconciler, types.NamespacedName{
				Name:      seeded.Name,
				Namespace: namespace,
			})

			body := recorder.lastMCPServerPatchBody()
			require.NotEmpty(t, body,
				"no MCPServer Patch was recorded; the reconcile path did not emit the expected write")
			assert.True(t,
				strings.Contains(body, `"resourceVersion"`),
				"MCPServer spec patch body did not include a resourceVersion precondition; "+
					"MergeFromWithOptimisticLock regression? body=%s", body)
		})
	}
}
