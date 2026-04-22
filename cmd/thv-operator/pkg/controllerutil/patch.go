// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MutateAndPatchSpec captures the current state of obj, applies mutate, and
// patches the object using a JSON merge patch with optimistic concurrency.
// A concurrent writer that advances resourceVersion between our read and our
// Patch triggers a 409 Conflict; controller-runtime then re-Gets, recomputes
// the diff, and writes on a fresh view — preserving cross-writer coexistence
// on the same resource.
//
// This is the canonical idiom for every spec or metadata write on a CR that
// another controller may also write (see #4767). A full PUT (r.Update) is a
// bug trap: any field the operator's local copy does not track — most
// importantly spec.authzConfig on MCPServer, which a separate authorization
// controller will own — is zeroed on every reconcile. A merge-patch body
// only carries fields the caller actually changed, so untouched fields never
// hit the wire and cannot be clobbered. MergeFromWithOptimisticLock sends
// resourceVersion as a precondition, giving 409-on-collision semantics for
// concurrent writers and defending metadata.finalizers (which has no
// array-merge semantics under RFC 7396 merge-patch) against wholesale
// replacement when another controller is mid-flight adding its own entry.
//
// Unlike MutateAndPatchStatus, this helper does NOT short-circuit on an
// empty diff. MergeFromWithOptimisticLock always emits metadata.resourceVersion
// into the patch body, so the status helper's "body == {}" check never fires;
// and every current call site carries a real mutation (finalizer add/remove,
// annotation stamp), so there is no no-op caller to optimize for.
//
// Do NOT use for status writes. Status-subresource writes are scoped to the
// status stanza, and forcing a 409 on every disjoint-field overlap would
// produce permanent churn with nothing gained — use MutateAndPatchStatus.
//
// If Patch returns an error, obj has already been mutated; callers must
// re-fetch obj before retrying rather than reusing the modified in-memory
// copy. The standard reconciler pattern — returning the error so
// controller-runtime requeues with a fresh Get — is the correct retry path.
//
// Typical usage:
//
//	err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, mcpServer,
//	    func(m *mcpv1beta1.MCPServer) {
//	        controllerutil.AddFinalizer(m, MCPServerFinalizerName)
//	    })
//	if err != nil {
//	    return ctrl.Result{}, err
//	}
//
// Expect 409s as routine log noise once external writers land — the guard
// doing its job, not a bug.
func MutateAndPatchSpec[T client.Object](
	ctx context.Context, c client.Client, obj T, mutate func(T),
) error {
	// Reject both a true-nil interface and a typed-nil pointer. T is
	// constrained to client.Object; every real implementer is a pointer
	// to a struct, so a nil obj is always a programmer error. Returning
	// an explicit error is nicer than the raw panic that the subsequent
	// .(T) type assertion would produce.
	v := reflect.ValueOf(obj)
	if !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return fmt.Errorf("MutateAndPatchSpec: obj must be non-nil")
	}
	original := obj.DeepCopyObject().(T)
	mutate(obj)
	return c.Patch(ctx, obj, client.MergeFromWithOptions(
		original, client.MergeFromWithOptimisticLock{}))
}
