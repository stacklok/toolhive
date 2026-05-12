// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"math"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func workloadReferenceCount(refs []mcpv1beta1.WorkloadReference) int32 {
	if len(refs) > math.MaxInt32 {
		return math.MaxInt32
	}

	return int32(len(refs)) //nolint:gosec // guarded above against int32 overflow
}
