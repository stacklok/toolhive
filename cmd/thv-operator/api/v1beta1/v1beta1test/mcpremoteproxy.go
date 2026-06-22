// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1test

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// MCPRemoteProxyOption mutates an MCPRemoteProxy under construction.
type MCPRemoteProxyOption func(*mcpv1beta1.MCPRemoteProxy)

// NewMCPRemoteProxy returns an MCPRemoteProxy with test defaults (the most
// common remote URL and proxy port found in the suite), customized by the
// supplied options. Transport is intentionally left unset because the vast
// majority of literals omit it and rely on its zero value.
//
// Its options are prefixed RemoteProxy to coexist with the other workload
// builders in this package, which share field names (ProxyPort, Transport,
// Replicas, Status, …) that Go will not let us overload.
func NewMCPRemoteProxy(name, namespace string, opts ...MCPRemoteProxyOption) *mcpv1beta1.MCPRemoteProxy {
	p := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			ProxyPort: 8080,
		},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// WithRemoteProxyURL overrides the remote MCP server URL.
func WithRemoteProxyURL(url string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.RemoteURL = url }
}

// WithRemoteProxyPort overrides the proxy port.
func WithRemoteProxyPort(port int32) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.ProxyPort = port }
}

// WithRemoteProxyTransport sets the transport ("sse" or "streamable-http").
func WithRemoteProxyTransport(transport string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.Transport = transport }
}

// WithRemoteProxyGroupRef sets the MCPGroup the proxy belongs to.
func WithRemoteProxyGroupRef(name string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.GroupRef = &mcpv1beta1.MCPGroupRef{Name: name} }
}

// WithRemoteProxyOIDCConfigRef sets the MCPOIDCConfig reference by name and audience.
func WithRemoteProxyOIDCConfigRef(name, audience string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) {
		p.Spec.OIDCConfigRef = &mcpv1beta1.MCPOIDCConfigReference{Name: name, Audience: audience}
	}
}

// WithRemoteProxyExternalAuthConfigRef sets the MCPExternalAuthConfig reference by name.
func WithRemoteProxyExternalAuthConfigRef(name string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) {
		p.Spec.ExternalAuthConfigRef = &mcpv1beta1.ExternalAuthConfigRef{Name: name}
	}
}

// WithRemoteProxyAuthServerRef sets the embedded auth-server reference by kind and name.
func WithRemoteProxyAuthServerRef(kind, name string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) {
		p.Spec.AuthServerRef = &mcpv1beta1.AuthServerRef{Kind: kind, Name: name}
	}
}

// WithRemoteProxyAuthzConfigRef sets the MCPAuthzConfig reference by name.
func WithRemoteProxyAuthzConfigRef(name string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) {
		p.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: name}
	}
}

// WithRemoteProxyAuthzConfig sets the inline authorization configuration.
func WithRemoteProxyAuthzConfig(cfg *mcpv1beta1.AuthzConfigRef) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.AuthzConfig = cfg }
}

// WithRemoteProxyToolConfigRef sets the MCPToolConfig reference by name.
func WithRemoteProxyToolConfigRef(name string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.ToolConfigRef = &mcpv1beta1.ToolConfigRef{Name: name} }
}

// WithRemoteProxyTelemetryConfigRef sets the MCPTelemetryConfig reference by name.
func WithRemoteProxyTelemetryConfigRef(name string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) {
		p.Spec.TelemetryConfigRef = &mcpv1beta1.MCPTelemetryConfigReference{Name: name}
	}
}

// WithRemoteProxyHeaderForward sets the header-forward configuration.
func WithRemoteProxyHeaderForward(cfg *mcpv1beta1.HeaderForwardConfig) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.HeaderForward = cfg }
}

// WithRemoteProxyAudit sets the audit configuration.
func WithRemoteProxyAudit(cfg *mcpv1beta1.AuditConfig) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.Audit = cfg }
}

// WithRemoteProxyServiceAccount sets the service account name.
func WithRemoteProxyServiceAccount(name string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.ServiceAccount = &name }
}

// WithRemoteProxyReplicas sets the desired replica count.
func WithRemoteProxyReplicas(replicas int32) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.Replicas = &replicas }
}

// WithRemoteProxySessionStorage sets the session storage configuration.
func WithRemoteProxySessionStorage(cfg *mcpv1beta1.SessionStorageConfig) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Spec.SessionStorage = cfg }
}

// WithRemoteProxyStatus replaces the MCPRemoteProxy status.
func WithRemoteProxyStatus(status mcpv1beta1.MCPRemoteProxyStatus) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) { p.Status = status }
}

// WithRemoteProxyDeletionTimestamp marks the proxy as being deleted (with the
// given finalizers so the fake client accepts the non-zero timestamp).
func WithRemoteProxyDeletionTimestamp(ts metav1.Time, finalizers ...string) MCPRemoteProxyOption {
	return func(p *mcpv1beta1.MCPRemoteProxy) {
		p.DeletionTimestamp = &ts
		p.Finalizers = finalizers
	}
}

// MutateRemoteProxy is the escape hatch for spec or metadata fields that have no
// dedicated option (Resources, ResourceOverrides, EndpointPrefix,
// TrustProxyHeaders, SessionAffinity, metadata.UID/Labels, …). It runs in option
// order. Prefer a dedicated option when one exists; keep genuinely complex
// fixtures as inline literals.
func MutateRemoteProxy(fn func(*mcpv1beta1.MCPRemoteProxy)) MCPRemoteProxyOption {
	return fn
}
