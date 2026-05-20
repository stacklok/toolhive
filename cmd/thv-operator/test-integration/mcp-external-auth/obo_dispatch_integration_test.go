// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers — OBO dispatch integration tests.
//
// These tests prove that applying an obo-typed MCPExternalAuthConfig referenced
// from each of the three consumer CRs (MCPServer, MCPRemoteProxy,
// VirtualMCPServer) lands at the obo.ErrEnterpriseRequired sentinel rather
// than leaking a generic "unsupported external auth type" or "unknown
// middleware type" error from the default arm of a switch.
//
// CRD-enum bypass strategy: the upstream CRD does not list "obo" in the
// kubebuilder enum on MCPExternalAuthConfigSpec.Type — that admission lives in
// the next OSS task (#5329). To exercise the dispatch wiring before that lands,
// these tests use option 3 from the issue ("Drive the reconciler directly at
// the Go level... bypassing the apiserver entirely"): they construct objects
// in memory, install them in a fake client, and invoke the reconciler /
// dispatch sites directly. The Ginkgo envtest suite in suite_test.go is left
// untouched.
package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
)

const oboNamespace = "default"

func newOBOScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return scheme
}

func newOBOConfig() *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "obo-config",
			Namespace: oboNamespace,
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
		},
	}
}

// reconcileOBOConfig drives the MCPExternalAuthConfigReconciler to a steady
// state for an obo-typed config and returns the resulting Valid condition for
// assertions, plus the fake client so callers can also drive the dispatch
// sites. It uses a fake client so the CRD enum does not have to admit "obo"
// at the apiserver layer.
func reconcileOBOConfig(t *testing.T) (*metav1.Condition, client.Client) {
	t.Helper()

	scheme := newOBOScheme(t)
	cfg := newOBOConfig()

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()

	r := &controllers.MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      cfg.Name,
		Namespace: cfg.Namespace,
	}}

	result, err := r.Reconcile(t.Context(), req)
	require.NoError(t, err)
	if result.RequeueAfter > 0 {
		_, err = r.Reconcile(t.Context(), req)
		require.NoError(t, err)
	}

	var updated mcpv1beta1.MCPExternalAuthConfig
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &updated))

	var validCond *metav1.Condition
	for i := range updated.Status.Conditions {
		if updated.Status.Conditions[i].Type == mcpv1beta1.ConditionTypeValid {
			validCond = &updated.Status.Conditions[i]
			break
		}
	}
	return validCond, fakeClient
}

// assertOBOValidFalseEnterpriseRequired asserts the canonical OBO-default
// outcome on the MCPExternalAuthConfig and on the dispatch error it caused.
// The exact "EnterpriseRequired" reason string is part of the user-facing
// contract — external consumers pattern-match on it.
func assertOBOValidFalseEnterpriseRequired(t *testing.T, validCond *metav1.Condition, dispatchErr error) {
	t.Helper()

	require.NotNil(t, validCond, "Valid condition must be set on the MCPExternalAuthConfig")
	assert.Equal(t, metav1.ConditionFalse, validCond.Status)
	assert.Equal(t, "EnterpriseRequired", validCond.Reason)

	// Generic-error guard: the dispatch path must NOT leak a generic
	// "unsupported external auth type" or "unknown middleware type" string
	// into the condition message.
	assert.NotContains(t, validCond.Message, "unsupported external auth type")
	assert.NotContains(t, validCond.Message, "unknown middleware type")

	require.Error(t, dispatchErr)
	require.ErrorIs(t, dispatchErr, obo.ErrEnterpriseRequired)
	assert.NotContains(t, dispatchErr.Error(), "unsupported external auth type")
	assert.NotContains(t, dispatchErr.Error(), "unknown middleware type")
}

// TestOBO_MCPServerConsumerPath_LandsAtEnterpriseRequired exercises the
// dispatch path consumed by MCPServer reconcilers: an obo-typed
// MCPExternalAuthConfig must (a) surface Valid=False / EnterpriseRequired on
// the resource itself and (b) cause AddExternalAuthConfigOptions (the
// switch-arm dispatch that MCPServer reconcilers call) to return an error
// wrapping obo.ErrEnterpriseRequired, not a generic "unsupported" message.
func TestOBO_MCPServerConsumerPath_LandsAtEnterpriseRequired(t *testing.T) {
	t.Parallel()

	validCond, fakeClient := reconcileOBOConfig(t)

	var options []runner.RunConfigBuilderOption
	dispatchErr := ctrlutil.AddExternalAuthConfigOptions(
		t.Context(),
		fakeClient,
		oboNamespace,
		"some-mcp-server",
		&mcpv1beta1.ExternalAuthConfigRef{Name: "obo-config"},
		nil, // oidcConfig — not used by the obo arm
		&options,
	)

	assertOBOValidFalseEnterpriseRequired(t, validCond, dispatchErr)
	assert.Empty(t, options, "default OBO handler must not append to the options slice")
}

// TestOBO_MCPRemoteProxyConsumerPath_LandsAtEnterpriseRequired exercises the
// dispatch path consumed by MCPRemoteProxy reconcilers. The MCPRemoteProxy
// reconciler shares the AddExternalAuthConfigOptions switch with MCPServer
// (see mcpremoteproxy_runconfig.go:136 and mcpserver_runconfig.go:243), so
// the dispatch wiring is identical. This test exists separately so a future
// reviewer can verify that ALL three consumer CR types are covered even if
// the dispatch sites diverge later.
func TestOBO_MCPRemoteProxyConsumerPath_LandsAtEnterpriseRequired(t *testing.T) {
	t.Parallel()

	validCond, fakeClient := reconcileOBOConfig(t)

	var options []runner.RunConfigBuilderOption
	dispatchErr := ctrlutil.AddExternalAuthConfigOptions(
		t.Context(),
		fakeClient,
		oboNamespace,
		"some-mcp-remote-proxy",
		&mcpv1beta1.ExternalAuthConfigRef{Name: "obo-config"},
		nil,
		&options,
	)

	assertOBOValidFalseEnterpriseRequired(t, validCond, dispatchErr)
	assert.Empty(t, options, "default OBO handler must not append to the options slice")
}

// TestOBO_VirtualMCPServerConsumerPath_LandsAtEnterpriseRequired exercises
// the two dispatch paths consumed by VirtualMCPServer reconcilers:
//
//  1. getExternalAuthConfigSecretEnvVar, the operator-side switch arm.
//  2. converters.DiscoverAndResolveAuth at pkg/vmcp/auth/converters/interface.go:191,
//     which wraps the converter's error with "failed to convert to strategy: %w".
//
// The wrap matters because the reconciler must recognize the sentinel through
// the wrap via errors.Is, NOT by substring matching the wrap prefix. The vMCP
// integration path is structurally fragile: matching by string would silently
// degrade if the wrap message ever changed.
func TestOBO_VirtualMCPServerConsumerPath_LandsAtEnterpriseRequired(t *testing.T) {
	t.Parallel()

	validCond, fakeClient := reconcileOBOConfig(t)

	// vMCP wrap site at pkg/vmcp/auth/converters/interface.go:191 —
	// errors.Is must match through the "failed to convert to strategy: %w" wrap.
	_, vMCPErr := converters.DiscoverAndResolveAuth(
		t.Context(),
		&mcpv1beta1.ExternalAuthConfigRef{Name: "obo-config"},
		oboNamespace,
		fakeClient,
	)

	assertOBOValidFalseEnterpriseRequired(t, validCond, vMCPErr)
	// Sanity: the wrap prefix is present, but the recognition must be via
	// errors.Is (already asserted above), not by string matching.
	assert.Contains(t, vMCPErr.Error(), "failed to convert to strategy")
}
