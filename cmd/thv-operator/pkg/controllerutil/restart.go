// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"maps"

	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// KubectlRestartedAtAnnotation is the pod-template annotation written by
// `kubectl rollout restart`. The operator must preserve it when computing
// the desired template; treating it as drift reverts the bounce (#6344).
// The string is defined once in pkg/container/kubernetes.
const KubectlRestartedAtAnnotation = kubernetes.KubectlRestartedAtAnnotation

// PreserveKubectlRestartedAt copies the kubectl rollout-restart annotation
// from live onto desired. Both maps may be nil. The returned map is a clone
// of desired (or a new map) so the caller's original is not mutated.
func PreserveKubectlRestartedAt(desired, live map[string]string) map[string]string {
	if live == nil {
		return desired
	}
	value, ok := live[KubectlRestartedAtAnnotation]
	if !ok || value == "" {
		return desired
	}
	if desired == nil {
		desired = make(map[string]string, 1)
	} else {
		desired = maps.Clone(desired)
	}
	desired[KubectlRestartedAtAnnotation] = value
	return desired
}
