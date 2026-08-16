// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
)

func TestMCPRemoteProxyEnsureServiceURL(t *testing.T) {
	t.Parallel()

	const (
		httpURL  = "http://mcp-proxy-remote-proxy.default.svc.cluster.local:8080"
		httpsURL = "https://mcp-proxy-remote-proxy.default.svc.cluster.local:8080"
	)
	tests := []struct {
		name       string
		initialURL string
		useTLS     bool
		wantURL    string
	}{
		{name: "sets HTTP URL", wantURL: httpURL},
		{name: "sets HTTPS URL", useTLS: true, wantURL: httpsURL},
		{name: "transitions HTTP to HTTPS", initialURL: httpURL, useTLS: true, wantURL: httpsURL},
		{name: "transitions HTTPS to HTTP", initialURL: httpsURL, wantURL: httpURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
				v1beta1test.WithRemoteProxyStatus(mcpv1beta1.MCPRemoteProxyStatus{URL: tt.initialURL}))
			objects := []client.Object{proxy}
			if tt.useTLS {
				proxy.Spec.AuthServerRef = &mcpv1beta1.AuthServerRef{
					Kind: "MCPExternalAuthConfig",
					Name: "auth",
				}
				objects = append(objects, listenerTLSAuthConfig())
			}

			reconciler, fakeClient := newTestMCPRemoteProxyReconciler(t, objects...)
			require.NoError(t, reconciler.ensureServiceURL(t.Context(), proxy))

			actual := &mcpv1beta1.MCPRemoteProxy{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(proxy), actual))
			assert.Equal(t, tt.wantURL, actual.Status.URL)

			resourceVersion := actual.ResourceVersion
			require.NoError(t, reconciler.ensureServiceURL(t.Context(), actual))
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(proxy), actual))
			assert.Equal(t, resourceVersion, actual.ResourceVersion, "identical reconciliation must not patch status")
		})
	}
}

func TestMCPServerEnsureServiceURL(t *testing.T) {
	t.Parallel()

	const (
		serviceName = "mcp-server-proxy"
		httpURL     = "http://mcp-server-proxy.default.svc.cluster.local:8080/mcp"
		httpsURL    = "https://mcp-server-proxy.default.svc.cluster.local:8080/mcp"
	)
	tests := []struct {
		name       string
		initialURL string
		useTLS     bool
		wantURL    string
	}{
		{name: "sets HTTP URL", wantURL: httpURL},
		{name: "sets HTTPS URL", useTLS: true, wantURL: httpsURL},
		{name: "transitions HTTP to HTTPS", initialURL: httpURL, useTLS: true, wantURL: httpsURL},
		{name: "transitions HTTPS to HTTP", initialURL: httpsURL, wantURL: httpURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := []v1beta1test.MCPServerOption{
				v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{URL: tt.initialURL}),
			}
			objects := []client.Object{}
			if tt.useTLS {
				options = append(options, v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth"))
				objects = append(objects, listenerTLSAuthConfig())
			}
			server := v1beta1test.NewMCPServer("server", "default", options...)
			objects = append(objects, server)
			fakeClient, _ := newTestFakeClient(t, &mcpv1beta1.MCPServer{}, objects...)
			reconciler := &MCPServerReconciler{Client: fakeClient}

			require.NoError(t, reconciler.ensureServiceURL(t.Context(), server, serviceName))

			actual := &mcpv1beta1.MCPServer{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(server), actual))
			assert.Equal(t, tt.wantURL, actual.Status.URL)

			resourceVersion := actual.ResourceVersion
			require.NoError(t, reconciler.ensureServiceURL(t.Context(), actual, serviceName))
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(server), actual))
			assert.Equal(t, resourceVersion, actual.ResourceVersion, "identical reconciliation must not patch status")
		})
	}
}

func listenerTLSAuthConfig() *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				ListenerTLS: &mcpv1beta1.ListenerTLSConfig{},
			},
		},
	}
}
