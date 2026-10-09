// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// sessionTLSStorage returns a Redis session storage config whose TLS block
// references the given CA Secret.
func sessionTLSStorage(caSecret string) *mcpv1beta1.SessionStorageConfig {
	return &mcpv1beta1.SessionStorageConfig{
		Provider: mcpv1beta1.SessionStorageProviderRedis,
		Address:  "redis:6380",
		TLS: &mcpv1beta1.RedisTLSConfig{
			CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: caSecret, Key: "ca.crt"},
		},
	}
}

// TestSessionRedisTLSCAMountedAndDriftDetected verifies, for every workload
// kind that shares spec.sessionStorage, that the CA Secret referenced by
// sessionStorage.tls is mounted at the path the runtime config points to, that
// a freshly built Deployment is not flagged as drifted, and that switching the
// referenced Secret is detected as drift even though the mounted path (and so
// the runtime config) is unchanged.
func TestSessionRedisTLSCAMountedAndDriftDetected(t *testing.T) {
	t.Parallel()

	type harness struct {
		// build returns the desired Deployment for a workload using caSecret.
		build func(t *testing.T, caSecret string) *appsv1.Deployment
		// needsUpdate reports drift of live against a workload using caSecret.
		needsUpdate func(t *testing.T, live *appsv1.Deployment, caSecret string) bool
	}

	mcpServer := func(caSecret string) *mcpv1beta1.MCPServer {
		return v1beta1test.NewMCPServer("tls-mcp", "default",
			v1beta1test.WithTransport("streamable-http"),
			v1beta1test.WithSessionStorage(sessionTLSStorage(caSecret)))
	}
	remoteProxy := func(caSecret string) *mcpv1beta1.MCPRemoteProxy {
		return v1beta1test.NewMCPRemoteProxy("tls-proxy", "default",
			v1beta1test.WithRemoteProxySessionStorage(sessionTLSStorage(caSecret)))
	}
	virtualServer := func(caSecret string) *mcpv1beta1.VirtualMCPServer {
		return v1beta1test.NewVirtualMCPServer("tls-vmcp", "default",
			v1beta1test.WithVMCPGroupRef("test-group"),
			v1beta1test.WithVMCPSessionStorage(sessionTLSStorage(caSecret)))
	}

	kinds := map[string]harness{
		"MCPServer": {
			build: func(t *testing.T, caSecret string) *appsv1.Deployment {
				t.Helper()
				r := newTestMCPServerReconciler(nil, testutil.NewScheme(t), kubernetes.PlatformKubernetes)
				dep, err := r.deploymentForMCPServer(t.Context(), mcpServer(caSecret), "test-checksum")
				require.NoError(t, err)
				return dep
			},
			needsUpdate: func(t *testing.T, live *appsv1.Deployment, caSecret string) bool {
				t.Helper()
				r := newTestMCPServerReconciler(nil, testutil.NewScheme(t), kubernetes.PlatformKubernetes)
				return r.deploymentNeedsUpdate(t.Context(), live, mcpServer(caSecret), "test-checksum")
			},
		},
		"MCPRemoteProxy": {
			build: func(t *testing.T, caSecret string) *appsv1.Deployment {
				t.Helper()
				return newRemoteProxyTLSReconciler(t, remoteProxy(caSecret)).
					deploymentForMCPRemoteProxy(t.Context(), remoteProxy(caSecret), "test-checksum")
			},
			needsUpdate: func(t *testing.T, live *appsv1.Deployment, caSecret string) bool {
				t.Helper()
				return newRemoteProxyTLSReconciler(t, remoteProxy(caSecret)).
					deploymentNeedsUpdate(t.Context(), live, remoteProxy(caSecret), "test-checksum")
			},
		},
		"VirtualMCPServer": {
			build: func(t *testing.T, caSecret string) *appsv1.Deployment {
				t.Helper()
				r := &VirtualMCPServerReconciler{
					Scheme:           testutil.NewScheme(t),
					PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
				}
				return r.deploymentForVirtualMCPServer(
					t.Context(), virtualServer(caSecret), "test-checksum", "", nil, []workloads.TypedWorkload{})
			},
			needsUpdate: func(t *testing.T, live *appsv1.Deployment, caSecret string) bool {
				t.Helper()
				r := &VirtualMCPServerReconciler{
					Scheme:           testutil.NewScheme(t),
					PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
				}
				return r.deploymentNeedsUpdate(
					t.Context(), live, virtualServer(caSecret), "test-checksum", "", nil, []workloads.TypedWorkload{})
			},
		},
	}

	for name, h := range kinds {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dep := h.build(t, "redis-ca")
			require.NotNil(t, dep)

			var volume *corev1.Volume
			for i := range dep.Spec.Template.Spec.Volumes {
				if dep.Spec.Template.Spec.Volumes[i].Name == ctrlutil.SessionRedisTLSCACertVolumeName {
					volume = &dep.Spec.Template.Spec.Volumes[i]
				}
			}
			require.NotNil(t, volume, "the session storage CA Secret must be mounted")
			require.NotNil(t, volume.Secret)
			assert.Equal(t, "redis-ca", volume.Secret.SecretName)
			assert.Equal(t, []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}, volume.Secret.Items)

			require.NotEmpty(t, dep.Spec.Template.Spec.Containers)
			assert.Contains(t, dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      ctrlutil.SessionRedisTLSCACertVolumeName,
				MountPath: "/etc/toolhive/session-redis-tls/ca.crt",
				SubPath:   "ca.crt",
				ReadOnly:  true,
			}, "the CA must be mounted where the runtime TLS config points")

			assert.False(t, h.needsUpdate(t, dep, "redis-ca"),
				"a freshly built Deployment must not be flagged as drifted")
			assert.True(t, h.needsUpdate(t, dep, "rotated-redis-ca"),
				"switching the referenced CA Secret must trigger a Deployment update")
		})
	}
}

func newRemoteProxyTLSReconciler(t *testing.T, proxy *mcpv1beta1.MCPRemoteProxy) *MCPRemoteProxyReconciler {
	t.Helper()
	scheme := testutil.NewScheme(t)
	return &MCPRemoteProxyReconciler{
		Client:           fake.NewClientBuilder().WithScheme(scheme).WithObjects(proxy).Build(),
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}
}
