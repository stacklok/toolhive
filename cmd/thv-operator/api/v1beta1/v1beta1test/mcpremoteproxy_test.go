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

func TestNewMCPRemoteProxy_Defaults(t *testing.T) {
	t.Parallel()

	p := v1beta1test.NewMCPRemoteProxy("proxy", "default")

	assert.Equal(t, "proxy", p.Name)
	assert.Equal(t, "default", p.Namespace)
	assert.Equal(t, "https://mcp.example.com", p.Spec.RemoteURL)
	assert.Equal(t, int32(8080), p.Spec.ProxyPort)
	assert.Empty(t, p.Spec.Transport, "transport has no default")
}

func TestNewMCPRemoteProxy_Options(t *testing.T) {
	t.Parallel()

	p := v1beta1test.NewMCPRemoteProxy("proxy", "toolhive",
		v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
		v1beta1test.WithRemoteProxyPort(9090),
		v1beta1test.WithRemoteProxyTransport("sse"),
		v1beta1test.WithRemoteProxyGroupRef("grp"),
		v1beta1test.WithRemoteProxyExternalAuthConfigRef("extauth"),
		v1beta1test.WithRemoteProxyToolConfigRef("tools"),
		v1beta1test.WithRemoteProxyTelemetryConfigRef("otel"),
	)

	assert.Equal(t, "https://remote.example.com", p.Spec.RemoteURL)
	assert.Equal(t, int32(9090), p.Spec.ProxyPort)
	assert.Equal(t, "sse", p.Spec.Transport)
	assert.Equal(t, "grp", p.Spec.GroupRef.Name)
	assert.Equal(t, "extauth", p.Spec.ExternalAuthConfigRef.Name)
	assert.Equal(t, "tools", p.Spec.ToolConfigRef.Name)
	assert.Equal(t, "otel", p.Spec.TelemetryConfigRef.Name)
}

func TestNewMCPRemoteProxy_AuthAndMutate(t *testing.T) {
	t.Parallel()

	p := v1beta1test.NewMCPRemoteProxy("proxy", "ns",
		v1beta1test.WithRemoteProxyOIDCConfigRef("oidc", "aud"),
		v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "auth"),
		v1beta1test.WithRemoteProxyAuthzConfigRef("authz"),
		v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
			p.Spec.EndpointPrefix = "/api"
		}),
	)

	require.NotNil(t, p.Spec.OIDCConfigRef)
	assert.Equal(t, "oidc", p.Spec.OIDCConfigRef.Name)
	assert.Equal(t, "aud", p.Spec.OIDCConfigRef.Audience)
	assert.Equal(t, "MCPExternalAuthConfig", p.Spec.AuthServerRef.Kind)
	assert.Equal(t, "authz", p.Spec.AuthzConfigRef.Name)
	assert.Equal(t, "/api", p.Spec.EndpointPrefix)
}
