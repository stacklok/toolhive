// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestNewScheme_RegistersOperatorAndBuiltinTypes(t *testing.T) {
	t.Parallel()

	scheme := NewScheme(t)

	// Operator API versions.
	apiVersions := []struct {
		name         string
		groupVersion schema.GroupVersion
		rootKinds    []string
	}{
		{
			name:         "v1alpha1",
			groupVersion: mcpv1alpha1.GroupVersion,
			rootKinds: []string{
				"EmbeddingServer",
				"MCPAuthzConfig",
				"MCPExternalAuthConfig",
				"MCPGroup",
				"MCPOIDCConfig",
				"MCPRegistry",
				"MCPRemoteProxy",
				"MCPServer",
				"MCPServerEntry",
				"MCPTelemetryConfig",
				"MCPWebhookConfig",
				"MCPToolConfig",
				"VirtualMCPCompositeToolDefinition",
				"VirtualMCPServer",
			},
		},
		{
			name:         "v1beta1",
			groupVersion: mcpv1beta1.GroupVersion,
			rootKinds: []string{
				"EmbeddingServer",
				"MCPAuthzConfig",
				"MCPExternalAuthConfig",
				"MCPGroup",
				"MCPOIDCConfig",
				"MCPRegistry",
				"MCPRemoteProxy",
				"MCPServer",
				"MCPServerEntry",
				"MCPTelemetryConfig",
				"MCPToolConfig",
				"VirtualMCPCompositeToolDefinition",
				"VirtualMCPServer",
			},
		},
	}
	for _, apiVersion := range apiVersions {
		t.Run(apiVersion.name, func(t *testing.T) {
			t.Parallel()

			for _, rootKind := range apiVersion.rootKinds {
				for _, kind := range []string{rootKind, rootKind + "List"} {
					assert.True(t, scheme.Recognizes(apiVersion.groupVersion.WithKind(kind)),
						"%s %s must be registered", apiVersion.name, kind)
				}
			}
		})
	}

	// Built-in Kubernetes types pulled in via client-go's scheme.
	assert.True(t, scheme.Recognizes(corev1.SchemeGroupVersion.WithKind("ConfigMap")),
		"corev1 ConfigMap must be registered")
	assert.True(t, scheme.Recognizes(appsv1.SchemeGroupVersion.WithKind("Deployment")),
		"appsv1 Deployment must be registered")
	assert.True(t, scheme.Recognizes(rbacv1.SchemeGroupVersion.WithKind("Role")),
		"rbacv1 Role must be registered")
}

func TestSchemeBuilderBuild_RegistersTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		groupVersion schema.GroupVersion
		build        func() (*runtime.Scheme, error)
	}{
		{"v1alpha1", mcpv1alpha1.GroupVersion, mcpv1alpha1.SchemeBuilder.Build},
		{"v1beta1", mcpv1beta1.GroupVersion, mcpv1beta1.SchemeBuilder.Build},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scheme, err := test.build()
			require.NoError(t, err)
			assert.True(t, scheme.Recognizes(test.groupVersion.WithKind("MCPServer")))
			assert.True(t, scheme.Recognizes(test.groupVersion.WithKind("MCPServerList")))
			assert.True(t, scheme.Recognizes(test.groupVersion.WithKind("WatchEvent")))
		})
	}
}

func TestNewScheme_AppliesExtraAddersOnTopOfDefault(t *testing.T) {
	t.Parallel()

	scheme := NewScheme(t, apiextensionsv1.AddToScheme)

	// Default set is still present.
	assert.True(t, scheme.Recognizes(mcpv1beta1.GroupVersion.WithKind("MCPServer")),
		"default operator types must remain registered")
	// The extra adder's types are now registered too.
	assert.True(t, scheme.Recognizes(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")),
		"extra adder types must be registered alongside the default set")
}
