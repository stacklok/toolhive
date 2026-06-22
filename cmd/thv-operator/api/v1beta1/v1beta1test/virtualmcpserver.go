// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1test

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpcrd"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// VirtualMCPServerOption mutates a VirtualMCPServer under construction.
type VirtualMCPServerOption func(*mcpv1beta1.VirtualMCPServer)

// NewVirtualMCPServer returns a VirtualMCPServer with the given name and
// namespace, customized by the supplied options. Unlike NewMCPServer it sets no
// spec defaults: VirtualMCPServer has no always-present scalar field (its
// required GroupRef carries a per-test name), so callers set what they need via
// the With* options or MutateVMCP.
//
// Its options are prefixed VMCP to coexist with the other workload builders in
// this package, which share field names (GroupRef, Replicas, Status, …) that Go
// will not let us overload.
func NewVirtualMCPServer(name, namespace string, opts ...VirtualMCPServerOption) *mcpv1beta1.VirtualMCPServer {
	v := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// WithVMCPGroupRef sets the MCPGroup the vMCP server aggregates.
func WithVMCPGroupRef(name string) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.GroupRef = &mcpv1beta1.MCPGroupRef{Name: name} }
}

// WithVMCPConfig sets the vMCP server configuration.
//
// The CRD spec field is the operator-owned vmcpcrd.Config mirror, but tests
// author fixtures using the runtime config.Config model. Because the two are
// field-for-field identical (enforced by the AssertNoDrift + round-trip tests),
// this option transcodes the runtime config into the CRD mirror via JSON. Any
// transcode error indicates a real drift between the two schemas and is fatal to
// the test rather than silently dropped.
func WithVMCPConfig(cfg config.Config) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) {
		data, err := json.Marshal(cfg)
		if err != nil {
			panic("v1beta1test: marshal config.Config: " + err.Error())
		}
		var mirror vmcpcrd.Config
		if err := json.Unmarshal(data, &mirror); err != nil {
			panic("v1beta1test: unmarshal into vmcpcrd.Config: " + err.Error())
		}
		v.Spec.Config = mirror
	}
}

// WithVMCPIncomingAuth sets the incoming auth configuration.
func WithVMCPIncomingAuth(cfg *mcpv1beta1.IncomingAuthConfig) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.IncomingAuth = cfg }
}

// WithVMCPOutgoingAuth sets the outgoing auth configuration.
func WithVMCPOutgoingAuth(cfg *mcpv1beta1.OutgoingAuthConfig) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.OutgoingAuth = cfg }
}

// WithVMCPTelemetryConfigRef sets the MCPTelemetryConfig reference by name.
func WithVMCPTelemetryConfigRef(name string) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) {
		v.Spec.TelemetryConfigRef = &mcpv1beta1.MCPTelemetryConfigReference{Name: name}
	}
}

// WithVMCPEmbeddingServerRef sets the EmbeddingServer reference by name.
func WithVMCPEmbeddingServerRef(name string) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) {
		v.Spec.EmbeddingServerRef = &mcpv1beta1.EmbeddingServerRef{Name: name}
	}
}

// WithVMCPAuthServerConfig sets the embedded auth-server configuration.
func WithVMCPAuthServerConfig(cfg *mcpv1beta1.EmbeddedAuthServerConfig) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.AuthServerConfig = cfg }
}

// WithVMCPReplicas sets the desired replica count.
func WithVMCPReplicas(replicas int32) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.Replicas = &replicas }
}

// WithVMCPPodTemplateSpec sets the raw pod template spec override.
func WithVMCPPodTemplateSpec(pts *runtime.RawExtension) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.PodTemplateSpec = pts }
}

// WithVMCPSessionStorage sets the session storage configuration.
func WithVMCPSessionStorage(cfg *mcpv1beta1.SessionStorageConfig) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.SessionStorage = cfg }
}

// WithVMCPServiceAccount sets the service account name.
func WithVMCPServiceAccount(name string) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.ServiceAccount = &name }
}

// WithVMCPStatus replaces the VirtualMCPServer status.
func WithVMCPStatus(status mcpv1beta1.VirtualMCPServerStatus) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) { v.Status = status }
}

// WithVMCPDeletionTimestamp marks the server as being deleted (with the given
// finalizers so the fake client accepts the non-zero timestamp).
func WithVMCPDeletionTimestamp(ts metav1.Time, finalizers ...string) VirtualMCPServerOption {
	return func(v *mcpv1beta1.VirtualMCPServer) {
		v.DeletionTimestamp = &ts
		v.Finalizers = finalizers
	}
}

// MutateVMCP is the escape hatch for spec or metadata fields that have no
// dedicated option. It runs in option order. Prefer a dedicated option when one
// exists; keep genuinely complex fixtures as inline literals.
func MutateVMCP(fn func(*mcpv1beta1.VirtualMCPServer)) VirtualMCPServerOption {
	return fn
}
