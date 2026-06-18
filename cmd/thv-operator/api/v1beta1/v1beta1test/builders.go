// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package v1beta1test provides test fixture builders for the v1beta1 operator
// API types. It is a companion to the api/v1beta1 package (in the spirit of the
// standard library's net/http + net/http/httptest split): the builders live
// next to the types they construct, but stay out of the production API surface
// because only *_test.go files import this package.
//
// The builders apply sensible test defaults and expose functional options for
// the high-frequency fields only. Rare or one-off spec shapes should still be
// written as inline literals at the call site rather than growing the option
// set here.
package v1beta1test

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// MCPServerOption mutates an MCPServer under construction.
type MCPServerOption func(*mcpv1beta1.MCPServer)

// NewMCPServer returns an MCPServer with test defaults (a stdio server on the
// default proxy port), customized by the supplied options. The defaults match
// the values the deleted per-file builders used, so existing tests read the
// same after migration.
func NewMCPServer(name, namespace string, opts ...MCPServerOption) *mcpv1beta1.MCPServer {
	m := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			ProxyPort: 8080,
		},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// WithImage overrides the container image.
func WithImage(image string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.Image = image }
}

// WithTransport overrides the transport (e.g. "stdio", "streamable-http").
func WithTransport(transport string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.Transport = transport }
}

// WithProxyPort overrides the proxy port.
func WithProxyPort(port int32) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.ProxyPort = port }
}

// WithGroupRef sets the MCPGroup the server belongs to.
func WithGroupRef(name string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.GroupRef = &mcpv1beta1.MCPGroupRef{Name: name} }
}

// WithEnv replaces the environment variables.
func WithEnv(env ...mcpv1beta1.EnvVar) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.Env = env }
}

// WithToolConfigRef sets the ToolConfig reference by name.
func WithToolConfigRef(name string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.ToolConfigRef = &mcpv1beta1.ToolConfigRef{Name: name} }
}

// WithDeletionTimestamp marks the server as being deleted (with the given
// finalizers so the fake client accepts the non-zero timestamp).
func WithDeletionTimestamp(ts metav1.Time, finalizers ...string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.DeletionTimestamp = &ts
		m.Finalizers = finalizers
	}
}
