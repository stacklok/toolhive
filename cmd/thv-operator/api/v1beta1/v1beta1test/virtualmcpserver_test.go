// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1test_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
)

func TestNewVirtualMCPServer_Defaults(t *testing.T) {
	t.Parallel()

	v := v1beta1test.NewVirtualMCPServer("vmcp", "default")

	assert.Equal(t, "vmcp", v.Name)
	assert.Equal(t, "default", v.Namespace)
	assert.Nil(t, v.Spec.GroupRef, "no spec defaults are set")
	assert.Nil(t, v.Spec.IncomingAuth)
}

func TestNewVirtualMCPServer_Options(t *testing.T) {
	t.Parallel()

	v := v1beta1test.NewVirtualMCPServer("vmcp", "toolhive",
		v1beta1test.WithVMCPGroupRef("my-group"),
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}),
		v1beta1test.WithVMCPTelemetryConfigRef("otel"),
		v1beta1test.WithVMCPEmbeddingServerRef("embed"),
		v1beta1test.WithVMCPReplicas(3),
	)

	assert.Equal(t, "my-group", v.Spec.GroupRef.Name)
	require.NotNil(t, v.Spec.IncomingAuth)
	assert.Equal(t, "anonymous", v.Spec.IncomingAuth.Type)
	assert.Equal(t, "otel", v.Spec.TelemetryConfigRef.Name)
	assert.Equal(t, "embed", v.Spec.EmbeddingServerRef.Name)
	require.NotNil(t, v.Spec.Replicas)
	assert.Equal(t, int32(3), *v.Spec.Replicas)
}

func TestNewVirtualMCPServer_MutateAndStatus(t *testing.T) {
	t.Parallel()

	v := v1beta1test.NewVirtualMCPServer("vmcp", "ns",
		v1beta1test.WithVMCPStatus(mcpv1beta1.VirtualMCPServerStatus{Phase: mcpv1beta1.VirtualMCPServerPhaseReady}),
		v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
			v.Spec.PassthroughHeaders = []string{"X-Tenant-ID"}
		}),
	)

	assert.Equal(t, mcpv1beta1.VirtualMCPServerPhaseReady, v.Status.Phase)
	assert.Equal(t, []string{"X-Tenant-ID"}, v.Spec.PassthroughHeaders)
}
