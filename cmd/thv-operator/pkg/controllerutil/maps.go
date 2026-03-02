// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

// MapIsSubset returns true if every key-value pair in subset exists in superset.
// Extra keys in superset (e.g. K8s-managed annotations) are ignored.
func MapIsSubset(subset, superset map[string]string) bool {
	if len(subset) > len(superset) {
		return false
	}
	for k, v := range subset {
		if sv, ok := superset[k]; !ok || sv != v {
			return false
		}
	}
	return true
}
