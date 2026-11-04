// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// enqueueReferencedConfig maps a workload to the config(s) it references, in the
// workload's namespace. It reuses the same extractor that backs the
// reverse-reference field index so the watch and the deletion check agree on
// what counts as a reference.
//
// EnqueueRequestsFromMapFunc runs the map on both the old and new object on
// update and on the object on delete, so a dropped reference or a deleted
// workload enqueues the config it used to reference. That lets a blocked
// deletion clear immediately instead of waiting for the 30s requeue, which
// stays as a backstop.
func enqueueReferencedConfig(extract func(client.Object) []string) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		names := extract(obj)
		reqs := make([]reconcile.Request, 0, len(names))
		for _, name := range names {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name},
			})
		}
		return reqs
	})
}
