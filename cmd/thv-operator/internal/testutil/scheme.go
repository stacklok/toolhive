// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// NewScheme builds a runtime.Scheme registered with all built-in Kubernetes
// types (corev1, appsv1, rbacv1, …) via client-go's scheme, plus both ToolHive
// operator API versions (v1alpha1 and v1beta1). Any extra AddToScheme functions
// are applied on top of that default set, in order.
//
// It replaces the per-test boilerplate of calling runtime.NewScheme() followed
// by individual AddToScheme calls. The built-in superset is harmless for fake
// clients: registering types a given test does not use has no effect, so almost
// every caller wants the bare NewScheme(t). Pass extras only when a test needs
// an API outside the default set, e.g.:
//
//	scheme := testutil.NewScheme(t, apiextensionsv1.AddToScheme)
//
// A test that must assert behavior when a type is deliberately unregistered
// should build its scheme inline with runtime.NewScheme(); that is a rare,
// self-documenting exception this helper intentionally does not serve.
func NewScheme(t *testing.T, extra ...func(*runtime.Scheme) error) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	adders := append([]func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		discoveryv1.AddToScheme,
		mcpv1beta1.AddToScheme,
		mcpv1alpha1.AddToScheme,
	}, extra...)
	for _, add := range adders {
		require.NoError(t, add(scheme))
	}
	return scheme
}
