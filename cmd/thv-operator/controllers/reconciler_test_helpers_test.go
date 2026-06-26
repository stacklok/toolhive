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
	scheme := testutil.NewScheme(t)
	fakeClient := withExternalAuthConfigRefIndexes(fake.NewClientBuilder().WithScheme(scheme)).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()
	return &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}, fakeClient
}

// withExternalAuthConfigRefIndexes registers the combined field indexes that
// MCPExternalAuthConfigReconciler.findReferencingMCPServers /
// findReferencingMCPRemoteProxies rely on, so fake-client MatchingFields lookups
// (which the real cache populates via SetupWithManager) work in unit tests.
// Without these, the fake client returns "no index with name ... has been
// registered" for MCPServer/MCPRemoteProxy. The index covers both
// spec.externalAuthConfigRef and spec.authServerRef.
func withExternalAuthConfigRefIndexes(b *fake.ClientBuilder) *fake.ClientBuilder {
	return b.
		WithIndex(&mcpv1beta1.MCPServer{}, externalAuthConfigRefIndexKey, indexMCPServerByExternalAuthConfigRef).
		WithIndex(&mcpv1beta1.MCPRemoteProxy{}, externalAuthConfigRefIndexKey, indexMCPRemoteProxyByExternalAuthConfigRef)
}

// withOIDCConfigRefIndexes registers the field indexes that
// MCPOIDCConfigReconciler.findReferencingWorkloads relies on, so fake-client
// MatchingFields lookups (which the real cache populates via SetupWithManager)
// work in unit tests. Without these, the fake client returns "no index with
// name ... has been registered" for MCPServer/VirtualMCPServer/MCPRemoteProxy.
func withOIDCConfigRefIndexes(b *fake.ClientBuilder) *fake.ClientBuilder {
	return b.
		WithIndex(&mcpv1beta1.MCPServer{}, oidcConfigRefIndexKey, indexMCPServerByOIDCConfigRef).
		WithIndex(&mcpv1beta1.VirtualMCPServer{}, vmcpOIDCConfigRefIndexKey, indexVirtualMCPServerByOIDCConfigRef).
		WithIndex(&mcpv1beta1.MCPRemoteProxy{}, oidcConfigRefIndexKey, indexMCPRemoteProxyByOIDCConfigRef)
}

// withTelemetryConfigRefIndexes registers the field indexes that
// MCPTelemetryConfigReconciler.findReferencingWorkloads relies on, so fake-client
// MatchingFields lookups (which the real cache populates via SetupWithManager)
// work in unit tests. Without these, the fake client returns "no index with
// name spec.telemetryConfigRef has been registered" for
// MCPServer/MCPRemoteProxy/VirtualMCPServer.
func withTelemetryConfigRefIndexes(b *fake.ClientBuilder) *fake.ClientBuilder {
	return b.
		WithIndex(&mcpv1beta1.MCPServer{}, telemetryConfigRefIndexKey, indexMCPServerByTelemetryConfigRef).
		WithIndex(&mcpv1beta1.MCPRemoteProxy{}, telemetryConfigRefIndexKey, indexMCPRemoteProxyByTelemetryConfigRef).
		WithIndex(&mcpv1beta1.VirtualMCPServer{}, telemetryConfigRefIndexKey, indexVirtualMCPServerByTelemetryConfigRef)
}

// withAuthzConfigRefIndexes registers the field indexes that
// MCPAuthzConfigReconciler.findReferencingWorkloads relies on, so fake-client
// MatchingFields lookups (which the real cache populates via SetupWithManager)
// work in unit tests. Without these, the fake client returns "no index with
// name ... has been registered" for MCPServer/MCPRemoteProxy/VirtualMCPServer.
func withAuthzConfigRefIndexes(b *fake.ClientBuilder) *fake.ClientBuilder {
	return b.
		WithIndex(&mcpv1beta1.MCPServer{}, authzConfigRefIndexKey, indexMCPServerByAuthzConfigRef).
		WithIndex(&mcpv1beta1.MCPRemoteProxy{}, authzConfigRefIndexKey, indexMCPRemoteProxyByAuthzConfigRef).
		WithIndex(&mcpv1beta1.VirtualMCPServer{}, vmcpAuthzConfigRefIndexKey, indexVirtualMCPServerByAuthzConfigRef)
}

// withToolConfigRefIndex registers the field index that
// ToolConfigReconciler.findReferencingWorkloads relies on, so fake-client
// MatchingFields lookups (which the real cache populates via SetupWithManager)
// work in unit tests. Without it, the fake client returns "no index with name
// spec.toolConfigRef has been registered".
func withToolConfigRefIndex(b *fake.ClientBuilder) *fake.ClientBuilder {
	return b.WithIndex(&mcpv1beta1.MCPServer{}, toolConfigRefIndexKey, indexMCPServerByToolConfigRef)
}

// withWebhookConfigRefIndex registers the field index that
// MCPWebhookConfigReconciler.findReferencingMCPServers relies on, so fake-client
// MatchingFields lookups (which the real cache populates via SetupWithManager)
// work in unit tests. Without it, the fake client returns "no index with name
// spec.webhookConfigRef has been registered".
func withWebhookConfigRefIndex(b *fake.ClientBuilder) *fake.ClientBuilder {
	return b.WithIndex(&mcpv1beta1.MCPServer{}, webhookConfigRefIndexKey, indexMCPServerByWebhookConfigRef)
}

// newTestMCPOIDCConfigReconciler builds an MCPOIDCConfigReconciler backed by a
// fake client seeded with objs, with the MCPOIDCConfig status subresource enabled
// and the OIDC config-ref field indexes registered (see withOIDCConfigRefIndexes).
// See newTestMCPExternalAuthConfigReconciler for the Recorder convention.
func newTestMCPOIDCConfigReconciler(t *testing.T, objs ...client.Object) (*MCPOIDCConfigReconciler, client.Client) {
	t.Helper()
	scheme := testutil.NewScheme(t)
	fakeClient := withOIDCConfigRefIndexes(fake.NewClientBuilder().WithScheme(scheme)).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()
	return &MCPOIDCConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}, fakeClient
}

// newTestMCPAuthzConfigReconciler builds an MCPAuthzConfigReconciler backed by a
// fake client seeded with objs, with the MCPAuthzConfig status subresource enabled
// and the authz config-ref field indexes registered (see withAuthzConfigRefIndexes).
// See newTestMCPExternalAuthConfigReconciler for the Recorder convention.
func newTestMCPAuthzConfigReconciler(t *testing.T, objs ...client.Object) (*MCPAuthzConfigReconciler, client.Client) {
	t.Helper()
	scheme := testutil.NewScheme(t)
	fakeClient := withAuthzConfigRefIndexes(fake.NewClientBuilder().WithScheme(scheme)).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.MCPAuthzConfig{}).
		Build()
	return &MCPAuthzConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}, fakeClient
}
