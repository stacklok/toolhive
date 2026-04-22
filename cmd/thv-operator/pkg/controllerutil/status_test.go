// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// statusPatchRecordingClient wraps a client.Client and intercepts
// .Status().Patch calls so tests can assert the wire-level patch body.
type statusPatchRecordingClient struct {
	client.Client
	mu       sync.Mutex
	bodies   []string
	forceErr error
}

func (c *statusPatchRecordingClient) Status() client.SubResourceWriter {
	return &statusSubResourceRecorder{parent: c, inner: c.Client.Status()}
}

type statusSubResourceRecorder struct {
	parent *statusPatchRecordingClient
	inner  client.SubResourceWriter
}

func (r *statusSubResourceRecorder) Create(
	ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption,
) error {
	return r.inner.Create(ctx, obj, subResource, opts...)
}

func (r *statusSubResourceRecorder) Update(
	ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption,
) error {
	return r.inner.Update(ctx, obj, opts...)
}

func (r *statusSubResourceRecorder) Patch(
	ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption,
) error {
	if data, err := patch.Data(obj); err == nil {
		r.parent.mu.Lock()
		r.parent.bodies = append(r.parent.bodies, string(data))
		r.parent.mu.Unlock()
	}
	if r.parent.forceErr != nil {
		return r.parent.forceErr
	}
	return r.inner.Patch(ctx, obj, patch, opts...)
}

func (r *statusSubResourceRecorder) Apply(
	ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption,
) error {
	return r.inner.Apply(ctx, obj, opts...)
}

func (c *statusPatchRecordingClient) lastBody() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		return ""
	}
	return c.bodies[len(c.bodies)-1]
}

func buildStatusTestClient(t *testing.T, seed *mcpv1beta1.MCPServer) (*statusPatchRecordingClient, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	inner := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(seed).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()
	recorder := &statusPatchRecordingClient{Client: inner}
	return recorder, inner
}

func newSeedMCPServer(name string) *mcpv1beta1.MCPServer {
	return &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "example/mcp:latest",
			Transport: "stdio",
			ProxyMode: "sse",
			ProxyPort: 8080,
			MCPPort:   8080,
		},
	}
}

// TestMutateAndPatchStatus_AppliesMutation asserts the happy path:
// the mutation is applied to the object in place AND persisted via a
// status-subresource merge patch whose body carries the mutated fields.
func TestMutateAndPatchStatus_AppliesMutation(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("mutate-happy")
	recorder, _ := buildStatusTestClient(t, seed)

	got := seed.DeepCopy()
	err := MutateAndPatchStatus(context.TODO(), recorder, got, func(s *mcpv1beta1.MCPServer) {
		meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "Testing",
			Message: "happy path",
		})
	})
	require.NoError(t, err)

	// In-memory object reflects the mutation.
	readyCond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, readyCond)
	assert.Equal(t, metav1.ConditionTrue, readyCond.Status)

	// Patch body carries the mutated status fields.
	body := recorder.lastBody()
	require.NotEmpty(t, body)
	assert.Contains(t, body, `"conditions"`)
	assert.Contains(t, body, `"Ready"`)
}

// TestMutateAndPatchStatus_NoOpMutateSkipsWireCall asserts that when
// mutate produces no diff, the helper does not issue a PATCH. This
// matters because the apiserver runs admission, audit, and (on older
// clusters) watch-notification pipelines for every PATCH regardless of
// body content — sending {} is not free.
func TestMutateAndPatchStatus_NoOpMutateSkipsWireCall(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("mutate-noop")
	seed.Status.Conditions = []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Initial",
		LastTransitionTime: metav1.Now(),
	}}
	recorder, inner := buildStatusTestClient(t, seed)

	got := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), got))

	// meta.SetStatusCondition is idempotent when Status/Reason/Message
	// all match — the mutation produces no diff at the byte level.
	err := MutateAndPatchStatus(context.TODO(), recorder, got, func(s *mcpv1beta1.MCPServer) {
		meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{
			Type:   "Ready",
			Status: metav1.ConditionTrue,
			Reason: "Initial",
		})
	})
	require.NoError(t, err)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	assert.Empty(t, recorder.bodies,
		"helper must not issue a PATCH when mutate produces no diff; "+
			"recorded %d body/bodies: %v", len(recorder.bodies), recorder.bodies)
}

// TestMutateAndPatchStatus_DeepCopyIsolatesOriginal asserts that the
// snapshot captured before mutate is truly independent of obj. A naive
// implementation that aliased the original would produce an empty diff
// (both pointers see the mutation), so the patch body would not include
// the mutated fields. This test guards that invariant.
func TestMutateAndPatchStatus_DeepCopyIsolatesOriginal(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("mutate-deepcopy")
	// Pre-seed a condition so the diff is a clean "one condition changed"
	// rather than "conditions array created".
	seed.Status.Conditions = []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "Initial",
		Message:            "before mutate",
		LastTransitionTime: metav1.Now(),
	}}
	recorder, inner := buildStatusTestClient(t, seed)

	got := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), got))

	err := MutateAndPatchStatus(context.TODO(), recorder, got, func(s *mcpv1beta1.MCPServer) {
		meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "Promoted",
			Message: "after mutate",
		})
	})
	require.NoError(t, err)

	body := recorder.lastBody()
	require.NotEmpty(t, body)
	// If DeepCopy aliased obj, original and current would both be
	// ConditionTrue+Promoted by the time MergeFrom computes the diff,
	// and the body would contain neither the old nor new reason. The
	// presence of "Promoted" in the body proves the snapshot captured
	// the pre-mutation state.
	assert.Contains(t, body, "Promoted",
		"patch body should reflect the mutated condition reason; "+
			"DeepCopy may have aliased the original. body=%s", body)
}

// TestMutateAndPatchStatus_PreservesDisjointStatusFields is the core
// regression test for the helper's stated purpose (#4633): when a
// caller writes status from a stale snapshot, fields owned by a
// different writer must survive. A full Status().Update would clobber
// them (PUT semantics replace the whole status stanza); a merge patch
// computed from the stale snapshot only carries the fields this caller
// changed, so disjoint fields on the live object are left alone.
//
// Test shape: seed an object, snapshot it, let a "second writer" mutate
// a disjoint field on the live object, then call the helper on the
// stale snapshot and mutate a different field. Assert both fields are
// present on a fresh Get of the live object.
func TestMutateAndPatchStatus_PreservesDisjointStatusFields(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("preserve-disjoint")
	recorder, inner := buildStatusTestClient(t, seed)

	// Stale snapshot taken before the second writer modifies live state.
	staleObj := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), staleObj))

	// Simulate a second writer (e.g. a runtime reporter) that owns
	// Phase/Message on the live object. staleObj does not know about
	// these writes.
	other := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), other))
	other.Status.Phase = mcpv1beta1.MCPServerPhaseReady
	other.Status.Message = "managed by the other writer"
	require.NoError(t, inner.Status().Update(context.TODO(), other))

	// Helper mutates a disjoint field on the stale snapshot.
	err := MutateAndPatchStatus(context.TODO(), recorder, staleObj, func(s *mcpv1beta1.MCPServer) {
		s.Status.URL = "http://mutated.example"
	})
	require.NoError(t, err)

	// Fresh Get: the field we mutated must be persisted, and the fields
	// the second writer owns must survive. If the helper were swapped
	// back to Status().Update, Phase and Message would be zeroed here.
	live := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), live))
	assert.Equal(t, "http://mutated.example", live.Status.URL,
		"mutated field must be persisted by the patch")
	assert.Equal(t, mcpv1beta1.MCPServerPhaseReady, live.Status.Phase,
		"disjoint field owned by another writer must survive the patch")
	assert.Equal(t, "managed by the other writer", live.Status.Message,
		"disjoint field owned by another writer must survive the patch")
}

// TestMutateAndPatchStatus_StaleScalarComputationClobbersConcurrentWrite
// codifies the scalar-field half of the caller contract and its wire-level
// semantics. Two sub-cases:
//
//  1. Re-assigning the read value is a no-op at the wire level —
//     merge-patch omits unchanged fields, so the concurrent writer's
//     value on the live object is preserved.
//  2. Assigning a value that differs from the stale snapshot sends the
//     field in the patch body and overwrites a concurrent writer's
//     value on the live object.
//
// The test guards both cases so that a future change to the helper's
// diff semantics fails loudly and forces a design discussion.
func TestMutateAndPatchStatus_StaleScalarComputationClobbersConcurrentWrite(t *testing.T) {
	t.Parallel()

	// Sub-case (1): stale writer re-assigns the read value → no-op diff,
	// concurrent writer preserved.
	t.Run("reassigning_read_value_is_noop", func(t *testing.T) {
		t.Parallel()

		seed := newSeedMCPServer("stale-noop")
		seed.Status.Phase = mcpv1beta1.MCPServerPhasePending
		recorder, inner := buildStatusTestClient(t, seed)

		staleObj := &mcpv1beta1.MCPServer{}
		require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), staleObj))
		stalePhase := staleObj.Status.Phase // "Pending"

		// Concurrent writer sets Phase to Ready.
		other := &mcpv1beta1.MCPServer{}
		require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), other))
		other.Status.Phase = mcpv1beta1.MCPServerPhaseReady
		require.NoError(t, inner.Status().Update(context.TODO(), other))

		// Stale writer assigns the value it read.
		err := MutateAndPatchStatus(context.TODO(), recorder, staleObj, func(s *mcpv1beta1.MCPServer) {
			s.Status.Phase = stalePhase
		})
		require.NoError(t, err)

		body := recorder.lastBody()
		assert.NotContains(t, body, `"phase"`,
			"re-assigning a scalar to its pre-mutate value must be omitted from "+
				"the merge-patch body. body=%s", body)

		live := &mcpv1beta1.MCPServer{}
		require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), live))
		assert.Equal(t, mcpv1beta1.MCPServerPhaseReady, live.Status.Phase,
			"when the diff omits the field, the concurrent writer's value must survive")
	})

	// Sub-case (2): stale writer computes a new value that differs from
	// its snapshot. The field lands in the patch and overwrites live.
	t.Run("stale_computation_clobbers_concurrent_write", func(t *testing.T) {
		t.Parallel()

		seed := newSeedMCPServer("stale-clobbers-scalar")
		seed.Status.Phase = mcpv1beta1.MCPServerPhasePending
		recorder, inner := buildStatusTestClient(t, seed)

		staleObj := &mcpv1beta1.MCPServer{}
		require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), staleObj))

		// Concurrent writer sets Phase to Ready on the live object.
		other := &mcpv1beta1.MCPServer{}
		require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), other))
		other.Status.Phase = mcpv1beta1.MCPServerPhaseReady
		other.Status.Message = "set by the concurrent writer"
		require.NoError(t, inner.Status().Update(context.TODO(), other))

		// Stale writer computes a new Phase from stale-derived state
		// (here, Failed — something neither the snapshot nor the live
		// object currently has).
		err := MutateAndPatchStatus(context.TODO(), recorder, staleObj, func(s *mcpv1beta1.MCPServer) {
			s.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		})
		require.NoError(t, err)

		body := recorder.lastBody()
		assert.Contains(t, body, `"phase"`,
			"a new value distinct from the snapshot must land in the patch body. body=%s", body)

		live := &mcpv1beta1.MCPServer{}
		require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), live))
		assert.Equal(t, mcpv1beta1.MCPServerPhaseFailed, live.Status.Phase,
			"stale-computed value must overwrite the concurrent writer's Phase; "+
				"if this assertion ever fails, the helper's contract has changed "+
				"and callers co-owning scalars may need fewer defensive measures")
	})
}

// TestMutateAndPatchStatus_StaleSnapshotClobbersConditionsFromAnotherWriter
// codifies a known limitation of the helper's RFC 7396 merge-patch
// semantics: a stale snapshot combined with a concurrent writer on a
// different condition type will erase the other writer's conditions,
// because JSON merge-patch replaces arrays wholesale for CRDs.
//
// This is the mirror image of the disjoint-preservation test above:
// disjoint scalar fields survive (they are absent from the diff), but
// the Conditions array does not, because any mutation to it causes the
// full array to appear in the patch body.
//
// The test does not assert a desirable behavior — it guards the
// documented caller contract. If a future change silently "fixes" this
// (e.g., by switching to strategic-merge-patch or by having the helper
// internally refresh before writing), the test will fail and force a
// design discussion rather than quietly altering the contract.
func TestMutateAndPatchStatus_StaleSnapshotClobbersConditionsFromAnotherWriter(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("stale-clobbers-conditions")
	seed.Status.Conditions = []metav1.Condition{{
		Type:               "Foo",
		Status:             metav1.ConditionTrue,
		Reason:             "Initial",
		LastTransitionTime: metav1.Now(),
	}}
	recorder, inner := buildStatusTestClient(t, seed)

	// Stale snapshot captured before the second writer mutates live state.
	staleObj := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), staleObj))

	// Second writer owns a different condition type ("Bar") and sets it
	// on the live object. Because apiserver lacks strategic-merge-patch
	// for CRDs, the stale writer below will clobber this on merge.
	other := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), other))
	meta.SetStatusCondition(&other.Status.Conditions, metav1.Condition{
		Type:    "Bar",
		Status:  metav1.ConditionTrue,
		Reason:  "OwnedByOther",
		Message: "set by the concurrent writer",
	})
	require.NoError(t, inner.Status().Update(context.TODO(), other))

	// Stale writer mutates "Foo" on the snapshot. The merge patch will
	// carry the whole Conditions array as the stale writer sees it — a
	// single-element array containing only Foo.
	err := MutateAndPatchStatus(context.TODO(), recorder, staleObj, func(s *mcpv1beta1.MCPServer) {
		meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{
			Type:   "Foo",
			Status: metav1.ConditionFalse,
			Reason: "Demoted",
		})
	})
	require.NoError(t, err)

	live := &mcpv1beta1.MCPServer{}
	require.NoError(t, inner.Get(context.TODO(), client.ObjectKeyFromObject(seed), live))

	// Foo was mutated and should be persisted.
	fooCond := meta.FindStatusCondition(live.Status.Conditions, "Foo")
	require.NotNil(t, fooCond, "mutated condition must be persisted")
	assert.Equal(t, metav1.ConditionFalse, fooCond.Status)

	// Bar was owned by the concurrent writer and should have been erased
	// by the wholesale array replacement. If this assertion ever fails,
	// the helper's merge-patch contract has changed — update the doc
	// comment and consider whether callers in Conditions-shared paths
	// can be simplified.
	barCond := meta.FindStatusCondition(live.Status.Conditions, "Bar")
	assert.Nil(t, barCond,
		"stale snapshot + RFC 7396 merge patch must erase the concurrent "+
			"writer's condition; this test guards the documented contract "+
			"so callers know Conditions writes require a fresh Get")
}

// TestMutateAndPatchStatus_RejectsNilObj asserts that a typed-nil obj
// returns a descriptive error rather than panicking inside the .(T)
// type assertion. A nil obj is always a programmer error, but the
// helper returns an error so the reconciler's requeue and logging
// machinery handles it cleanly instead of crashing the worker.
func TestMutateAndPatchStatus_RejectsNilObj(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("mutate-nil")
	recorder, _ := buildStatusTestClient(t, seed)

	var nilObj *mcpv1beta1.MCPServer
	err := MutateAndPatchStatus(context.TODO(), recorder, nilObj, func(_ *mcpv1beta1.MCPServer) {
		t.Fatal("mutate must not be called when obj is nil")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "obj must be non-nil",
		"error message should name the offending parameter for debugging; got %v", err)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	assert.Empty(t, recorder.bodies,
		"no PATCH should be issued when the input is invalid")
}

// TestMutateAndPatchStatus_PropagatesPatchError asserts that an error
// from the underlying status.Patch is returned to the caller unmodified.
// Controllers rely on the error for requeue decisions; swallowing it
// would cause silent status drift.
func TestMutateAndPatchStatus_PropagatesPatchError(t *testing.T) {
	t.Parallel()

	seed := newSeedMCPServer("mutate-err")
	recorder, _ := buildStatusTestClient(t, seed)
	want := errors.New("simulated apiserver failure")
	recorder.forceErr = want

	got := seed.DeepCopy()
	err := MutateAndPatchStatus(context.TODO(), recorder, got, func(s *mcpv1beta1.MCPServer) {
		meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{
			Type:   "Ready",
			Status: metav1.ConditionTrue,
			Reason: "Testing",
		})
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, want,
		"helper should propagate the apiserver error unchanged; got %v", err)
}
