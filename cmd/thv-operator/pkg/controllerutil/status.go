// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"bytes"
	"context"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MutateAndPatchStatus captures the current state of obj, applies mutate,
// and patches the status subresource using a plain JSON merge patch.
//
// This is the canonical idiom for every status write in the operator. See
// #4633: a full status PUT (r.Status().Update) clobbers fields the operator
// does not track (e.g. runtime-reporter-owned fields on VirtualMCPServer
// status). A merge-patch body only carries fields the caller actually
// changed, so disjoint status writers coexist.
//
// The patch is NOT optimistic-locked: status-subresource writes are scoped
// to the status stanza, and forcing a 409 on every disjoint-field overlap
// would produce permanent churn with nothing gained.
//
// Caller contract (important — the patch body is the diff between the
// pre-mutate snapshot and the post-mutate object; it does NOT reflect
// what is live on the apiserver):
//
//   - Conditions-array writes require the caller to be the sole owner
//     of the entire Status.Conditions array on that CRD. Per-condition-
//     type ownership is NOT sufficient: client.MergeFrom produces an
//     RFC 7396 merge patch, which replaces the array wholesale for CRDs
//     (the +listType=map marker is only honored by strategic-merge-patch).
//     Any concurrent writer whose Patch lands between this caller's Get
//     and this caller's Patch — on any condition type, including ones
//     this caller does not touch — will be erased. A fresh Get narrows
//     the TOCTOU window but cannot eliminate it. If two code paths must
//     write conditions on the same CRD, consolidate them into a single
//     owner or move one writer to a dedicated status field outside the
//     array.
//
//   - Scalar fields land in the patch body only when the post-mutate
//     value differs from the pre-mutate snapshot. Re-assigning a scalar
//     to the same value it was read as is a no-op at the wire level —
//     the field is absent from the patch and a concurrent writer's
//     value on the live object is preserved. BUT if mutate assigns a
//     value that differs from the snapshot (e.g., a stale derivation
//     from pod state), that value will overwrite whatever a concurrent
//     writer wrote to the live object. There is no defense against
//     this at the helper level: a stale computation wins. For scalars
//     co-owned by multiple writers, use a single-owner design or
//     refresh the object via a fresh Get before calling this helper.
//
// Do NOT use for metadata or spec writes. Those need optimistic locking
// via the sibling helper MutateAndPatchSpec.
// Rationale and MCPServer spec migration: #4767 (tracking), #4914 (implementation).
//
// If Patch returns an error, obj has already been mutated; callers must
// re-fetch obj before retrying rather than reusing the modified in-memory
// copy. The standard reconciler pattern — returning the error so
// controller-runtime requeues with a fresh Get — is the correct retry path.
//
// Typical usage:
//
//	err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, mcpServer,
//	    func(s *mcpv1alpha1.MCPServer) {
//	        meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{
//	            Type:   mcpv1alpha1.ConditionReady,
//	            Status: metav1.ConditionTrue,
//	            Reason: mcpv1alpha1.ConditionReasonReady,
//	        })
//	    })
func MutateAndPatchStatus[T client.Object](
	ctx context.Context, c client.Client, obj T, mutate func(T),
) error {
	// Reject both a true-nil interface and a typed-nil pointer. T is
	// constrained to client.Object; every real implementer is a pointer
	// to a struct, so a nil obj is always a programmer error. Returning
	// an explicit error is nicer than the raw panic that the subsequent
	// .(T) type assertion would produce.
	v := reflect.ValueOf(obj)
	if !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return fmt.Errorf("MutateAndPatchStatus: obj must be non-nil")
	}
	original := obj.DeepCopyObject().(T)
	mutate(obj)
	data, err := client.MergeFrom(original).Data(obj)
	if err != nil {
		return err
	}
	// Skip the wire call for a no-op mutate. The apiserver runs the full
	// admission and audit pipeline for every PATCH regardless of body
	// content, so sending {} costs watch-cascade and audit log noise for
	// no benefit. Controllers like EmbeddingServerReconciler that requeue
	// at 1s would otherwise generate steady-state no-op PATCH traffic.
	if bytes.Equal(data, []byte("{}")) {
		return nil
	}
	return c.Status().Patch(ctx, obj, client.RawPatch(types.MergePatchType, data))
}
