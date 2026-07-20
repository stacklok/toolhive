// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestWorkloadReferenceCount(t *testing.T) {
	t.Parallel()

	assert.EqualValues(t, 0, workloadReferenceCount(nil))
	assert.EqualValues(t, 0, workloadReferenceCount([]mcpv1beta1.WorkloadReference{}))

	refs := []mcpv1beta1.WorkloadReference{
		{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: "server-1"},
		{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: "proxy-1"},
	}

	assert.EqualValues(t, 2, workloadReferenceCount(refs))
	assert.Equal(t, int32(math.MaxInt32), workloadReferenceCountFromLen(math.MaxInt32+1))
}
