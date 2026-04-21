// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// specPatchRecordingClient wraps a client.Client and intercepts top-level
// Patch calls so tests can assert the wire-level patch body (including the
// MergeFromWithOptimisticLock resourceVersion precondition).
type specPatchRecordingClient struct {
	client.Client
	mu       sync.Mutex
	bodies   []string
	forceErr error
}

func (c *specPatchRecordingClient) Patch(
	ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption,
) error {
	if data, err := patch.Data(obj); err == nil {
		c.mu.Lock()
		c.bodies = append(c.bodies, string(data))
		c.mu.Unlock()
	}
	if c.forceErr != nil {
		return c.forceErr
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *specPatchRecordingClient) lastBody() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		return ""
	}
	return c.bodies[len(c.bodies)-1]
}

func buildSpecTestClient(t *testing.T, seed *mcpv1beta1.MCPServer) (*specPatchRecordingClient, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	inner := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(seed).
		Build()
	recorder := &specPatchRecordingClient{Client: inner}
	return recorder, inner
}

// TestMutateAndPatchSpec_AppliesMutationWithOptimisticLock asserts the
// happy path: the mutation lands on the in-memory object AND the wire
// body carries both (a) the mutated fields and (b) a resourceVersion
// precondition — the deterministic signal that MergeFromWithOptimisticLock
// was in effect. A regression that dropped the OL option would produce a
// body without the precondition and silently lose the 409-on-collision
// semantics.
func TestMutateAndPatchSpec_AppliesMutationWithOptimisticLock(t *testing.T) {
	t.Parallel()

	const finalizerName = "toolhive.stacklok.dev/test-finalizer"

	tests := []struct {
		name   string
		mutate func(*mcpv1beta1.MCPServer)
		// substrings the patch body must contain
		bodyMustContain []string
	}{
		{
			name: "add finalizer",
			mutate: func(m *mcpv1beta1.MCPServer) {
				m.Finalizers = append(m.Finalizers, finalizerName)
			},
			bodyMustContain: []string{`"finalizers"`, finalizerName},
		},
		{
			name: "stamp annotation",
			mutate: func(m *mcpv1beta1.MCPServer) {
				if m.Annotations == nil {
					m.Annotations = map[string]string{}
				}
				m.Annotations["toolhive.stacklok.dev/restart-processed"] = "rev-42"
			},
			bodyMustContain: []string{`"annotations"`, "restart-processed", "rev-42"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seed := newSeedMCPServer("mutate-spec-happy-" + tc.name)
			recorder, inner := buildSpecTestClient(t, seed)

			got := &mcpv1beta1.MCPServer{}
			require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), got))

			err := MutateAndPatchSpec(context.TODO(), recorder, got, tc.mutate)
			require.NoError(t, err)

			body := recorder.lastBody()
			require.NotEmpty(t, body)
			for _, want := range tc.bodyMustContain {
				assert.Contains(t, body, want,
					"patch body must carry the mutated field %q; body=%s", want, body)
			}
			// Optimistic-lock wire signal: MergeFromWithOptimisticLock
			// always embeds metadata.resourceVersion into the patch body
			// as a precondition. A regression to plain MergeFrom would
			// drop this field.
			assert.Contains(t, body, `"resourceVersion"`,
				"MergeFromWithOptimisticLock regression? body=%s", body)
		})
	}
}

// TestMutateAndPatchSpec_DeepCopyIsolatesOriginal asserts that the
// snapshot captured before mutate is truly independent of obj. A naive
// implementation that aliased the original would produce an empty diff
// (both pointers see the mutation), so the patch body would not include
// the mutated annotation.
func TestMutateAndPatchSpec_DeepCopyIsolatesOriginal(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("mutate-spec-deepcopy")
	seed.Annotations = map[string]string{"existing": "before"}
	recorder, inner := buildSpecTestClient(t, seed)

	got := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), got))

	err := MutateAndPatchSpec(context.TODO(), recorder, got, func(m *mcpv1beta1.MCPServer) {
		m.Annotations["mutated"] = "after"
	})
	require.NoError(t, err)

	body := recorder.lastBody()
	require.NotEmpty(t, body)
	// If DeepCopy had aliased obj, original and current would both carry
	// "mutated":"after" by the time MergeFrom computes the diff, and the
	// body would lack the new annotation. Its presence proves the snapshot
	// captured the pre-mutation state.
	assert.Contains(t, body, "mutated",
		"patch body should reflect the mutated annotation; DeepCopy may "+
			"have aliased the original. body=%s", body)
	assert.Contains(t, body, "after",
		"patch body should carry the new annotation value; body=%s", body)
}

// TestMutateAndPatchSpec_Propagates409Conflict asserts that a 409
// Conflict from the apiserver (the normal outcome of a stale
// resourceVersion under optimistic locking) propagates to the caller
// unchanged. Controllers rely on IsConflict to decide between requeue
// and error-path logging; wrapping or swallowing the error would break
// that contract.
func TestMutateAndPatchSpec_Propagates409Conflict(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("mutate-spec-conflict")
	recorder, _ := buildSpecTestClient(t, seed)
	recorder.forceErr = apierrors.NewConflict(
		schema.GroupResource{Group: mcpv1beta1.GroupVersion.Group, Resource: "mcpservers"},
		seed.Name,
		assert.AnError,
	)

	got := seed.DeepCopy()
	err := MutateAndPatchSpec(context.TODO(), recorder, got, func(m *mcpv1beta1.MCPServer) {
		if m.Annotations == nil {
			m.Annotations = map[string]string{}
		}
		m.Annotations["x"] = "y"
	})
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err),
		"helper must propagate 409 Conflict so callers can requeue; got %v", err)
}

// TestMutateAndPatchSpec_RejectsNilObj asserts that a typed-nil obj
// returns a descriptive error rather than panicking inside the .(T)
// type assertion. Mirrors TestMutateAndPatchStatus_RejectsNilObj: a
// nil obj is always a programmer error, but returning an error keeps
// the reconciler's requeue and logging machinery clean instead of
// crashing the worker goroutine.
func TestMutateAndPatchSpec_RejectsNilObj(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("mutate-spec-nil")
	recorder, _ := buildSpecTestClient(t, seed)

	var nilObj *mcpv1beta1.MCPServer
	err := MutateAndPatchSpec(context.TODO(), recorder, nilObj, func(_ *mcpv1beta1.MCPServer) {
		t.Fatal("mutate must not be called when obj is nil")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MutateAndPatchSpec: obj must be non-nil",
		"error message should name the offending parameter for debugging; got %v", err)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	assert.Empty(t, recorder.bodies,
		"no PATCH should be issued when the input is invalid")
}

// TestMutateAndPatchSpec_PreservesDisjointSpecFields is the regression
// test that justifies the helper's existence (see #4767). Merge-patch
// bodies only carry fields the caller actually changed, so disjoint spec
// fields — specifically spec.authzConfig, which the authorization
// controller owns — survive a spec mutation performed by this operator.
// A swap back to r.Update (full PUT) would clobber spec.authzConfig and
// fail this test.
//
// Shape: seed an MCPServer carrying spec.authzConfig, use the helper to
// stamp a finalizer, then fresh-Get and assert both the finalizer landed
// AND spec.authzConfig survived unchanged. Also assert the recorded
// patch body does NOT carry spec.authzConfig — that is the wire-level
// proof that merge-patch is doing its job.
func TestMutateAndPatchSpec_PreservesDisjointSpecFields(t *testing.T) {
	t.Parallel()

	const finalizerName = "toolhive.stacklok.dev/test-finalizer"

	seed := newSeedMCPServer("preserve-disjoint-spec")
	// Populate a spec field that an external controller owns. If the
	// helper regresses to r.Update, this field will be zeroed on Patch.
	seed.Spec.AuthzConfig = &mcpv1beta1.AuthzConfigRef{
		Type: mcpv1beta1.AuthzConfigTypeConfigMap,
		ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
			Name: "external-authz-policy",
			Key:  "policy.cedar",
		},
	}
	recorder, inner := buildSpecTestClient(t, seed)

	got := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), got))

	err := MutateAndPatchSpec(context.TODO(), recorder, got, func(m *mcpv1beta1.MCPServer) {
		m.Finalizers = append(m.Finalizers, finalizerName)
	})
	require.NoError(t, err)

	// Wire-level: the patch body must NOT carry spec.authzConfig because
	// the helper's DeepCopy snapshot captured it and the mutation did not
	// change it. A regression to r.Update would send the whole spec and
	// this assertion would fail.
	body := recorder.lastBody()
	require.NotEmpty(t, body)
	assert.NotContains(t, body, "authzConfig",
		"merge-patch body must omit fields the caller did not change; "+
			"regression to r.Update? body=%s", body)

	// Integration-level: fresh Get shows the finalizer landed AND the
	// disjoint spec field survived.
	live := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), live))
	assert.Contains(t, live.Finalizers, finalizerName,
		"mutated field must be persisted by the patch")
	require.NotNil(t, live.Spec.AuthzConfig,
		"disjoint spec field owned by another controller must survive; "+
			"this is the #4767 regression guard")
	assert.Equal(t, mcpv1beta1.AuthzConfigTypeConfigMap, live.Spec.AuthzConfig.Type)
	require.NotNil(t, live.Spec.AuthzConfig.ConfigMap)
	assert.Equal(t, "external-authz-policy", live.Spec.AuthzConfig.ConfigMap.Name)
}
