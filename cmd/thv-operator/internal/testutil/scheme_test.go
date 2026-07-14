// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestNewScheme_RegistersOperatorAndBuiltinTypes(t *testing.T) {
	t.Parallel()

	scheme := NewScheme(t)

	// Operator API versions.
	assert.True(t, scheme.Recognizes(mcpv1beta1.GroupVersion.WithKind("MCPServer")),
		"v1beta1 MCPServer must be registered")
	assert.True(t, scheme.Recognizes(mcpv1alpha1.GroupVersion.WithKind("MCPServer")),
		"v1alpha1 MCPServer must be registered")

	// Built-in Kubernetes types pulled in via client-go's scheme.
	assert.True(t, scheme.Recognizes(corev1.SchemeGroupVersion.WithKind("ConfigMap")),
		"corev1 ConfigMap must be registered")
	assert.True(t, scheme.Recognizes(appsv1.SchemeGroupVersion.WithKind("Deployment")),
		"appsv1 Deployment must be registered")
	assert.True(t, scheme.Recognizes(rbacv1.SchemeGroupVersion.WithKind("Role")),
		"rbacv1 Role must be registered")
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
