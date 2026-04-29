// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

// ApplyPodTemplateSpecPatch applies a raw strategic merge patch to a base
// PodTemplateSpec and returns the merged result.
//
// The patch parameter is the raw user-supplied JSON (e.g. the contents of a
// CRD's `spec.podTemplateSpec.Raw`). Using the raw bytes — rather than a
// re-marshaled struct — is intentional: Go's `json.Marshal` converts nil
// slices to `[]`, which strategic merge patch interprets as "replace with
// empty" and would clobber controller-generated defaults. Passing the user's
// JSON through unmodified preserves exactly what they specified, and
// strategic merge patch leaves controller-set fields the user did not touch
// alone.
//
// Empty inputs are handled as no-ops: if patch has zero length the base is
// returned unchanged. A patch of `{}` is also a safe no-op because strategic
// merge patch on an empty object reaches the unmarshal step but produces a
// document equal to the base.
//
// This helper is policy-neutral. It returns an error on any failure (base
// marshal, patch apply, output unmarshal) and lets the caller decide whether
// the failure should hard-fail (block resource creation) or soft-fail (log
// and fall back to controller defaults). Different controllers in this
// project make different choices for the same failure mode, and that
// decision is intentionally pushed to the call site:
//
//   - VirtualMCPServer hard-fails: an invalid pod template blocks Deployment
//     creation. The user-facing signal is the reconciler returning the error,
//     surfaced as a Kubernetes Event and a controller log line.
//   - EmbeddingServer soft-fails: the merge is skipped and the StatefulSet is
//     built from controller defaults. The user-facing signal is the
//     `PodTemplateValid=False` status condition (set elsewhere by the
//     validation pass) plus a controller log line.
//
// Both behaviors are valid; the helper does not pick one.
func ApplyPodTemplateSpecPatch(base corev1.PodTemplateSpec, patch []byte) (corev1.PodTemplateSpec, error) {
	if len(patch) == 0 {
		return base, nil
	}

	originalJSON, err := json.Marshal(base)
	if err != nil {
		return corev1.PodTemplateSpec{}, fmt.Errorf("failed to marshal base PodTemplateSpec: %w", err)
	}

	patchedJSON, err := strategicpatch.StrategicMergePatch(originalJSON, patch, corev1.PodTemplateSpec{})
	if err != nil {
		return corev1.PodTemplateSpec{}, fmt.Errorf("failed to apply strategic merge patch: %w", err)
	}

	var merged corev1.PodTemplateSpec
	if err := json.Unmarshal(patchedJSON, &merged); err != nil {
		return corev1.PodTemplateSpec{}, fmt.Errorf("failed to unmarshal patched PodTemplateSpec: %w", err)
	}

	return merged, nil
}
