// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

// KubectlRestartedAtAnnotation is the pod-template annotation written by
// `kubectl rollout restart`. The operator must preserve it when computing
// the desired template; treating it as drift reverts the bounce (#6344).
const KubectlRestartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

// PreserveKubectlRestartedAt copies the kubectl rollout-restart annotation
// from live onto desired. Both maps may be nil. The returned map is desired
// (possibly allocated) with the live value applied when present.
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
	}
	desired[KubectlRestartedAtAnnotation] = value
	return desired
}
