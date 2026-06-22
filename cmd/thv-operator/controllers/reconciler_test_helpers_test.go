// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// mockPlatformDetector is a mock implementation of PlatformDetector for testing
type mockPlatformDetector struct {
	platform kubernetes.Platform
	err      error
}

func (m *mockPlatformDetector) DetectPlatform(_ *rest.Config) (kubernetes.Platform, error) {
	return m.platform, m.err
}

// newTestFakeClient builds a fake client and scheme shared by the per-reconciler
// test factories below. statusType is registered as a status subresource so
// Status().Update/Patch behaves like a real apiserver for that type; objs seed
// the tracker. The scheme is the full testutil.NewScheme(t) superset.
//
// Callers that need a different status subresource set (multiple types, or a
// type other than the reconciler's own) should build the client inline rather
// than using a factory — the extra registration is load-bearing.
func newTestFakeClient(t *testing.T, statusType client.Object, objs ...client.Object) (client.Client, *runtime.Scheme) {
	t.Helper()
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(statusType).
		Build()
	return fakeClient, scheme
}

// newTestMCPServerReconciler creates a properly initialized MCPServerReconciler for testing.
// This ensures all required fields are set, including the PlatformDetector.
//
//nolint:unparam // platform parameter is intentionally flexible for future test cases
func newTestMCPServerReconciler(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	platform kubernetes.Platform,
) *MCPServerReconciler {
	mockDetector := &mockPlatformDetector{
		platform: platform,
		err:      nil,
	}
	return &MCPServerReconciler{
		Client:           k8sClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetectorWithDetector(mockDetector),
	}
}

// newTestVirtualMCPServerReconciler builds a VirtualMCPServerReconciler backed by
// a fake client seeded with objs. The fake client and scheme are returned so
// callers can inspect or mutate cluster state directly. The status subresource is
// enabled for VirtualMCPServer.
//
// PlatformDetector defaults to ctrlutil.NewSharedPlatformDetector(). Tests that
// need a specific platform can override r.PlatformDetector after construction,
// e.g. ctrlutil.NewSharedPlatformDetectorWithDetector(&mockPlatformDetector{...}).
// Recorder and ImagePullSecretsDefaults are left as zero values; set them on the
// returned reconciler when a test needs them.
func newTestVirtualMCPServerReconciler(t *testing.T, objs ...client.Object) (*VirtualMCPServerReconciler, client.Client) {
	t.Helper()
	fakeClient, scheme := newTestFakeClient(t, &mcpv1beta1.VirtualMCPServer{}, objs...)
	return &VirtualMCPServerReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}, fakeClient
}

// newTestMCPRemoteProxyReconciler builds an MCPRemoteProxyReconciler backed by a
// fake client seeded with objs. See newTestVirtualMCPServerReconciler for the
// shared conventions (returned client, default PlatformDetector, zero-value
// Recorder and ImagePullSecretsDefaults).
func newTestMCPRemoteProxyReconciler(t *testing.T, objs ...client.Object) (*MCPRemoteProxyReconciler, client.Client) {
	t.Helper()
	fakeClient, scheme := newTestFakeClient(t, &mcpv1beta1.MCPRemoteProxy{}, objs...)
	return &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}, fakeClient
}

// newTestMCPExternalAuthConfigReconciler builds an MCPExternalAuthConfigReconciler
// backed by a fake client seeded with objs, with the MCPExternalAuthConfig status
// subresource enabled. Recorder is left nil; set it on the returned reconciler
// (e.g. r.Recorder = events.NewFakeRecorder(n)) when a test asserts on events.
func newTestMCPExternalAuthConfigReconciler(
	t *testing.T, objs ...client.Object,
) (*MCPExternalAuthConfigReconciler, client.Client) {
	t.Helper()
	fakeClient, scheme := newTestFakeClient(t, &mcpv1beta1.MCPExternalAuthConfig{}, objs...)
	return &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}, fakeClient
}

// newTestMCPOIDCConfigReconciler builds an MCPOIDCConfigReconciler backed by a
// fake client seeded with objs, with the MCPOIDCConfig status subresource enabled.
// See newTestMCPExternalAuthConfigReconciler for the Recorder convention.
func newTestMCPOIDCConfigReconciler(t *testing.T, objs ...client.Object) (*MCPOIDCConfigReconciler, client.Client) {
	t.Helper()
	fakeClient, scheme := newTestFakeClient(t, &mcpv1beta1.MCPOIDCConfig{}, objs...)
	return &MCPOIDCConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}, fakeClient
}

// newTestMCPAuthzConfigReconciler builds an MCPAuthzConfigReconciler backed by a
// fake client seeded with objs, with the MCPAuthzConfig status subresource enabled.
// See newTestMCPExternalAuthConfigReconciler for the Recorder convention.
func newTestMCPAuthzConfigReconciler(t *testing.T, objs ...client.Object) (*MCPAuthzConfigReconciler, client.Client) {
	t.Helper()
	fakeClient, scheme := newTestFakeClient(t, &mcpv1beta1.MCPAuthzConfig{}, objs...)
	return &MCPAuthzConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}, fakeClient
}
