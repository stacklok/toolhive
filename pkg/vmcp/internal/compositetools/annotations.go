// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package compositetools

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// StepAnnotationResolver resolves a composite-tool step's tool reference
// ("{workloadID}.{toolName}") to the backend tool's annotations. It returns nil
// when the step tool is unknown or the backend declares no annotations.
type StepAnnotationResolver func(stepTool string) *vmcp.ToolAnnotations

// DeriveCompositeAnnotations computes the safety-floor annotations for a
// composite tool from the annotations of its step tools.
//
// Fail-closed contract (issue #6192):
//   - A workflow with NO tool steps (e.g. only elicitation steps) yields an
//     empty/nil stepAnn slice, and Derive returns nil — there is nothing to
//     derive, so no floor is advertised.
//   - A workflow with one or more tool steps yields a non-nil stepAnn slice,
//     and Derive ALWAYS returns a conservative floor, even when every step
//     tool's annotations are nil/unknown. An unknown step never makes the tool
//     look safer — it taints destructive/openWorld to true and readOnly to
//     false. This means an explicit readOnlyHint:true over backends that
//     declare no annotations (the common case) CONTRADICTS the floor and is
//     dropped by CheckAnnotationContradiction.
//
// Derivation rules per hint:
//   - ReadOnlyHint: AND across steps — true only when every step declares
//     readOnlyHint=true; any step that is nil or false makes it non-read-only.
//   - DestructiveHint: OR across steps — true when any step is nil or declares
//     destructiveHint=true (an unknown step is tainted conservatively).
//   - OpenWorldHint: OR across steps — true when any step is nil or declares
//     openWorldHint=true.
//   - IdempotentHint: never derived (always nil) — idempotency does not
//     compose across multi-step workflows.
func DeriveCompositeAnnotations(stepAnn []*vmcp.ToolAnnotations) *vmcp.ToolAnnotations {
	// No tool steps at all: nothing to derive. This is distinct from "≥1 tool
	// step exists but none declares annotations", which produces a conservative
	// floor below.
	if len(stepAnn) == 0 {
		return nil
	}

	readOnly := true
	destructive := false
	openWorld := false
	for _, ann := range stepAnn {
		if ann == nil {
			readOnly = false
			destructive = true
			openWorld = true
			continue
		}
		if ann.ReadOnlyHint == nil || !*ann.ReadOnlyHint {
			readOnly = false
		}
		if ann.DestructiveHint == nil || *ann.DestructiveHint {
			destructive = true
		}
		if ann.OpenWorldHint == nil || *ann.OpenWorldHint {
			openWorld = true
		}
	}

	return &vmcp.ToolAnnotations{
		ReadOnlyHint:    &readOnly,
		DestructiveHint: &destructive,
		OpenWorldHint:   &openWorld,
	}
}

// CheckAnnotationContradiction reports whether explicit annotations would make
// a composite tool look SAFER than its derived safety floor allows:
//   - readOnlyHint=true while the floor is not true
//   - destructiveHint=false while the floor is true
//   - openWorldHint=false while the floor is true
//
// idempotentHint never contradicts, and an explicit hint MORE conservative than
// the floor (e.g. readOnlyHint=false when the floor is true) is allowed.
//
// DeriveCompositeAnnotations always populates ReadOnlyHint, DestructiveHint,
// and OpenWorldHint when it returns a non-nil floor, so the nil-hint branches
// below are defensive (they would only matter for a hand-built floor). A nil
// floor (no tool steps) or nil explicit means there is nothing to guard and
// the check returns nil.
func CheckAnnotationContradiction(explicit, floor *vmcp.ToolAnnotations) error {
	if explicit == nil || floor == nil {
		return nil
	}

	if explicit.ReadOnlyHint != nil && *explicit.ReadOnlyHint &&
		(floor.ReadOnlyHint == nil || !*floor.ReadOnlyHint) {
		return fmt.Errorf(
			"annotations declare readOnlyHint=true, but not every step tool is read-only; " +
				"the composite tool cannot be advertised as read-only")
	}
	if explicit.DestructiveHint != nil && !*explicit.DestructiveHint &&
		floor.DestructiveHint != nil && *floor.DestructiveHint {
		return fmt.Errorf(
			"annotations declare destructiveHint=false, but at least one step tool is destructive or unknown; " +
				"the composite tool cannot be advertised as non-destructive")
	}
	if explicit.OpenWorldHint != nil && !*explicit.OpenWorldHint &&
		floor.OpenWorldHint != nil && *floor.OpenWorldHint {
		return fmt.Errorf(
			"annotations declare openWorldHint=false, but at least one step tool is open-world or unknown; " +
				"the composite tool cannot be advertised as closed-world")
	}
	return nil
}

// MergeAnnotations merges explicit annotations over the derived floor. Per
// hint, an explicit non-nil value wins over the floor; Title behaves the same
// (a non-empty explicit Title wins). Returns nil when both inputs are nil.
//
// The returned value is a deep copy: *bool pointer fields and the Title string
// are copied so the advertised tool does not alias the author's config
// pointers. The inputs are not modified.
func MergeAnnotations(floor, explicit *vmcp.ToolAnnotations) *vmcp.ToolAnnotations {
	if floor == nil && explicit == nil {
		return nil
	}

	merged := &vmcp.ToolAnnotations{}
	if floor != nil {
		copyBoolPtr(&merged.ReadOnlyHint, floor.ReadOnlyHint)
		copyBoolPtr(&merged.DestructiveHint, floor.DestructiveHint)
		copyBoolPtr(&merged.IdempotentHint, floor.IdempotentHint)
		copyBoolPtr(&merged.OpenWorldHint, floor.OpenWorldHint)
		merged.Title = floor.Title
	}
	if explicit != nil {
		if explicit.Title != "" {
			merged.Title = explicit.Title
		}
		copyBoolPtr(&merged.ReadOnlyHint, explicit.ReadOnlyHint)
		copyBoolPtr(&merged.DestructiveHint, explicit.DestructiveHint)
		copyBoolPtr(&merged.IdempotentHint, explicit.IdempotentHint)
		copyBoolPtr(&merged.OpenWorldHint, explicit.OpenWorldHint)
	}
	return merged
}

// copyBoolPtr overwrites dst with a fresh copy of src when src is non-nil, so
// dst does not alias src's memory. A nil src leaves dst untouched (preserving
// the prior value, if any).
func copyBoolPtr(dst **bool, src *bool) {
	if src == nil {
		return
	}
	v := *src
	*dst = &v
}
