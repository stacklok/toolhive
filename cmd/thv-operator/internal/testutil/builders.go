// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// NOTE: This is the CENTRAL-placement alternative to the companion package in
// cmd/thv-operator/api/v1beta1/v1beta1test. Both define the same NewMCPServer
// builder; this prototype carries both so reviewers can compare ergonomics
// side-by-side. Only one placement will survive into the real fixture-builder PR.

// MCPServerOption mutates an MCPServer under construction.
type MCPServerOption func(*mcpv1beta1.MCPServer)

// NewMCPServer returns an MCPServer with test defaults (a stdio server on the
// default proxy port), customized by the supplied options.
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
