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
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.VirtualMCPServer{}).
		Build()
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
	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}).
		Build()
	return &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}, fakeClient
}
