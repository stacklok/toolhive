// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/redisconfig"
	"github.com/stacklok/toolhive/pkg/runner"
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
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME", "")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY", "")
	tests := []struct {
		name   string
		global bool
		memory bool
	}{
		{name: "explicit Redis TLS"},
		{name: "global Redis TLS", global: true},
		{name: "global Redis TLS with explicit memory", global: true, memory: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "")
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", "")
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", "")
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "")
			testSessionRedisTLSCAMountedAndDriftDetected(t, tt.global, tt.memory)
		})
	}
}

func testSessionRedisTLSCAMountedAndDriftDetected(t *testing.T, global, memory bool) {
	t.Helper()
	storage := sessionTLSStorage
	if global {
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "global-redis:6380")
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", "true")
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "ca.crt")
		storage = func(caSecret string) *mcpv1beta1.SessionStorageConfig {
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", caSecret)
			if memory {
				return &mcpv1beta1.SessionStorageConfig{Provider: "memory"}
			}
			return nil
		}
	}

	type harness struct {
		// build returns the desired Deployment for a workload using caSecret.
		build func(t *testing.T, caSecret string) *appsv1.Deployment
		// needsUpdate reports drift of live against a workload using caSecret.
		needsUpdate func(t *testing.T, live *appsv1.Deployment, caSecret string) bool
	}

	mcpServer := func(caSecret string) *mcpv1beta1.MCPServer {
		return v1beta1test.NewMCPServer("tls-mcp", "default",
			v1beta1test.WithTransport("streamable-http"),
			v1beta1test.WithSessionStorage(storage(caSecret)))
	}
	remoteProxy := func(caSecret string) *mcpv1beta1.MCPRemoteProxy {
		return v1beta1test.NewMCPRemoteProxy("tls-proxy", "default",
			v1beta1test.WithRemoteProxySessionStorage(storage(caSecret)))
	}
	virtualServer := func(caSecret string) *mcpv1beta1.VirtualMCPServer {
		return v1beta1test.NewVirtualMCPServer("tls-vmcp", "default",
			v1beta1test.WithVMCPGroupRef("test-group"),
			v1beta1test.WithVMCPSessionStorage(storage(caSecret)))
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
			dep := h.build(t, "redis-ca")
			require.NotNil(t, dep)

			var volume *corev1.Volume
			for i := range dep.Spec.Template.Spec.Volumes {
				if dep.Spec.Template.Spec.Volumes[i].Name == ctrlutil.SessionRedisTLSCACertVolumeName {
					volume = &dep.Spec.Template.Spec.Volumes[i]
				}
			}
			if memory && name == "MCPRemoteProxy" {
				assert.Nil(t, volume, "explicit memory suppresses the remote proxy global fallback")
				assert.False(t, h.needsUpdate(t, dep, "redis-ca"))
				return
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
			if global {
				t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "rotated.pem")
				assert.True(t, h.needsUpdate(t, dep, "redis-ca"),
					"switching the global CA Secret key must trigger a Deployment update")
				t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "ca.crt")
			}
		})
	}
}

// TestRemoteProxySessionRedisTLSPodTemplatePatchIsNotDrift verifies that a
// podTemplateSpec overriding the session storage CA volume is compared against
// the patched Deployment, so the reconciler does not report drift forever.
func TestRemoteProxySessionRedisTLSPodTemplatePatchIsNotDrift(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("tls-proxy", "default",
		v1beta1test.WithRemoteProxySessionStorage(sessionTLSStorage("redis-ca")))
	proxy.Spec.PodTemplateSpec = &runtime.RawExtension{Raw: []byte(`{"spec":{"volumes":[` +
		`{"name":"` + ctrlutil.SessionRedisTLSCACertVolumeName + `","secret":{"secretName":"patched-ca"}}]}}`)}
	r := newRemoteProxyTLSReconciler(t, proxy)

	dep := r.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
	require.NotNil(t, dep)

	var secretName string
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name == ctrlutil.SessionRedisTLSCACertVolumeName {
			require.NotNil(t, v.Secret)
			secretName = v.Secret.SecretName
		}
	}
	require.Equal(t, "patched-ca", secretName, "the podTemplateSpec patch must override the CA volume")

	assert.False(t, r.deploymentNeedsUpdate(t.Context(), dep, proxy, "test-checksum"),
		"a Deployment matching the patched template must not be flagged as drifted")
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

func TestGlobalRedisTLSRunConfigSelection(t *testing.T) {
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "global-redis:6380")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", "true")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "ca.crt")

	caTLS := &redisconfig.TLSConfig{CACertFile: "/etc/toolhive/session-redis-tls/ca.crt"}
	tests := []struct {
		name     string
		storage  *mcpv1beta1.SessionStorageConfig
		globalCA string
		wantAddr string
		wantTLS  *redisconfig.TLSConfig
	}{
		{name: "global system roots", wantAddr: "global-redis:6380", wantTLS: &redisconfig.TLSConfig{}},
		{name: "global private CA", globalCA: "global-ca", wantAddr: "global-redis:6380", wantTLS: caTLS},
		{name: "explicit plaintext overrides global TLS", globalCA: "global-ca",
			storage: &mcpv1beta1.SessionStorageConfig{Provider: "redis", Address: "local-redis:6379"}, wantAddr: "local-redis:6379"},
		{name: "explicit system roots override global CA", globalCA: "global-ca",
			storage:  &mcpv1beta1.SessionStorageConfig{Provider: "redis", Address: "local-redis:6380", TLS: &mcpv1beta1.RedisTLSConfig{}},
			wantAddr: "local-redis:6380", wantTLS: &redisconfig.TLSConfig{}},
		{name: "explicit private CA overrides global CA", globalCA: "global-ca", storage: sessionTLSStorage("local-ca"),
			wantAddr: "redis:6380", wantTLS: caTLS},
		{name: "memory retains each resource existing precedence", globalCA: "global-ca",
			storage: &mcpv1beta1.SessionStorageConfig{Provider: "memory"}, wantAddr: "global-redis:6380", wantTLS: caTLS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", tt.globalCA)
			key := ""
			if tt.globalCA != "" {
				key = "ca.crt"
			}
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", key)
			before := tt.storage.DeepCopy()
			for _, remote := range []bool{false, true} {
				name := "MCPServer"
				if remote {
					name = "MCPRemoteProxy"
				}
				t.Run(name, func(t *testing.T) {
					cfg := &runner.RunConfig{}
					if remote {
						proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxySessionStorage(tt.storage))
						populateScalingConfigForRemoteProxy(cfg, proxy)
					} else {
						server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithSessionStorage(tt.storage))
						populateScalingConfig(cfg, server)
					}
					if remote && tt.storage != nil && tt.storage.Provider == "memory" {
						if cfg.ScalingConfig != nil {
							assert.Nil(t, cfg.ScalingConfig.SessionRedis)
						}
						return
					}
					require.NotNil(t, cfg.ScalingConfig)
					require.NotNil(t, cfg.ScalingConfig.SessionRedis)
					assert.Equal(t, tt.wantAddr, cfg.ScalingConfig.SessionRedis.Address)
					assert.Equal(t, tt.wantTLS, cfg.ScalingConfig.SessionRedis.TLS)
				})
			}
			assert.Equal(t, before, tt.storage, "global fallback must not mutate spec.sessionStorage")
		})
	}
}
