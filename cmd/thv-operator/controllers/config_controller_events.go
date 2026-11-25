// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
)

// Event reasons emitted by the policy-shaped config controllers
// (MCPOIDCConfig, MCPExternalAuthConfig, MCPAuthzConfig). They are shared so
// `kubectl describe` surfaces the same vocabulary across all three sibling
// resources.
const (
	// eventReasonConfigInvalid is the Warning event reason emitted when a
	// config's spec is rejected by validation.
	eventReasonConfigInvalid = "ConfigInvalid"

	// eventReasonConfigValid is the Normal event reason emitted when a config
	// recovers (validation passes after a prior failure).
	eventReasonConfigValid = "ConfigValid"

	// eventReasonDeletionBlocked is the Warning event reason emitted when a
	// config's deletion is gated by a still-referencing workload.
	eventReasonDeletionBlocked = "DeletionBlocked"

	// eventActionValidate is the action recorded on validation events.
	eventActionValidate = "Validate"

	// eventActionDelete is the action recorded on deletion events.
	eventActionDelete = "Delete"
)

// emitConfigEvent records an event against obj when rec is non-nil. The event
// has no "related" object (the nil second argument to Eventf): these are
// single-object lifecycle events on the config itself. Centralizing the nil
// guard keeps the call sites in the three config controllers free of repeated
// `if r.Recorder != nil` boilerplate. note is a format string applied
// to args, so callers must pass any caller-controlled text (e.g. an error
// string) as an arg rather than interpolating it into note — that keeps a
// literal `%` in the text from being mis-parsed as a format verb.
func emitConfigEvent(
	rec events.EventRecorder,
	obj runtime.Object,
	eventType, reason, action, note string,
	args ...any,
) {
	if rec == nil {
		return
	}
	rec.Eventf(obj, nil, eventType, reason, action, note, args...)
}

// emitConfigRecoveryEvent records a Normal ConfigValid event against obj when
// wasInvalid is true — i.e. validation has just passed after a prior failure.
// It is a no-op otherwise (and when rec is nil), so a first-time-valid config
// and a steady-state valid reconcile produce no event spam. Callers must read
// wasInvalid from the object's conditions BEFORE the success status patch (which
// mutates conditions in place).
func emitConfigRecoveryEvent(rec events.EventRecorder, obj runtime.Object, wasInvalid bool) {
	if !wasInvalid {
		return
	}
	emitConfigEvent(rec, obj, corev1.EventTypeNormal,
		eventReasonConfigValid, eventActionValidate, "spec validation passed")
}

// conditionStatusIs reports whether the named condition is currently present on
// conditions with the given status. It is used to gate event emission on a real
// state transition: a Warning fires only when entering the failure/blocked state
// (not on every steady-state reconcile that re-observes it), and a recovery
// Normal fires only when leaving a prior failure.
//
// Callers must read the transition BEFORE calling MutateAndPatchStatus, which
// mutates the in-memory object's conditions in place.
func conditionStatusIs(conditions []metav1.Condition, condType string, status metav1.ConditionStatus) bool {
	c := meta.FindStatusCondition(conditions, condType)
	return c != nil && c.Status == status
}
