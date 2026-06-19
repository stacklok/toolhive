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
	"k8s.io/apimachinery/pkg/runtime"

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

// WithMCPGroupRef sets the MCPGroup the server belongs to.
func WithMCPGroupRef(name string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.GroupRef = &mcpv1beta1.MCPGroupRef{Name: name} }
}

// WithEnv replaces the environment variables.
func WithEnv(env ...mcpv1beta1.EnvVar) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.Env = env }
}

// WithProxyMode overrides the proxy mode (e.g. "sse", "streamable-http").
func WithProxyMode(mode string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.ProxyMode = mode }
}

// WithArgs sets the container args.
func WithArgs(args ...string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.Args = args }
}

// WithToolConfigRef sets the ToolConfig reference by name.
func WithToolConfigRef(name string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.ToolConfigRef = &mcpv1beta1.ToolConfigRef{Name: name} }
}

// WithExternalAuthConfigRef sets the MCPExternalAuthConfig reference by name.
func WithExternalAuthConfigRef(name string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.Spec.ExternalAuthConfigRef = &mcpv1beta1.ExternalAuthConfigRef{Name: name}
	}
}

// WithWebhookConfigRef sets the MCPWebhookConfig reference by name.
func WithWebhookConfigRef(name string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.WebhookConfigRef = &mcpv1beta1.WebhookConfigRef{Name: name} }
}

// WithTelemetryConfigRef sets the MCPTelemetryConfig reference by name.
func WithTelemetryConfigRef(name string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.Spec.TelemetryConfigRef = &mcpv1beta1.MCPTelemetryConfigReference{Name: name}
	}
}

// WithOIDCConfigRef sets the MCPOIDCConfig reference by name and audience.
func WithOIDCConfigRef(name, audience string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.Spec.OIDCConfigRef = &mcpv1beta1.MCPOIDCConfigReference{Name: name, Audience: audience}
	}
}

// WithAuthzConfigRef sets the MCPAuthzConfig reference by name.
func WithAuthzConfigRef(name string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: name}
	}
}

// WithMCPPort sets the MCP container port.
func WithMCPPort(port int32) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.MCPPort = port }
}

// WithReplicas sets the desired replica count.
func WithReplicas(replicas int32) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.Replicas = &replicas }
}

// WithPodTemplateSpec sets the raw pod template spec override.
func WithPodTemplateSpec(pts *runtime.RawExtension) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.PodTemplateSpec = pts }
}

// WithSessionStorage sets the session storage configuration.
func WithSessionStorage(cfg *mcpv1beta1.SessionStorageConfig) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.SessionStorage = cfg }
}

// WithAudit sets the audit configuration.
func WithAudit(cfg *mcpv1beta1.AuditConfig) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Spec.Audit = cfg }
}

// WithStatus replaces the MCPServer status.
func WithStatus(status mcpv1beta1.MCPServerStatus) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) { m.Status = status }
}

// WithDeletionTimestamp marks the server as being deleted (with the given
// finalizers so the fake client accepts the non-zero timestamp).
func WithDeletionTimestamp(ts metav1.Time, finalizers ...string) MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.DeletionTimestamp = &ts
		m.Finalizers = finalizers
	}
}

// Mutate is the escape hatch for spec or metadata fields that have no dedicated
// option (AuthzConfig, ResourceOverrides, Secrets, Volumes, AuthServerRef,
// metadata.UID/Generation, …). It runs last, after all other options. Prefer a
// dedicated option when one exists; reach for Mutate only for the less common
// fields, and keep genuinely complex fixtures as inline literals rather than
// threading everything through here.
func Mutate(fn func(*mcpv1beta1.MCPServer)) MCPServerOption {
	return fn
}
