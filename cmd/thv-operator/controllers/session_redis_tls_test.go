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
	operatorvmcpconfig "github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpconfig"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/redisconfig"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/session"
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

type sessionRedisTLSCase struct {
	name      string
	global    bool
	memory    bool
	plaintext bool
	disabled  bool
}

// TestSessionRedisTLSCAMountedAndDriftDetected verifies, for every workload
// kind that shares spec.sessionStorage, that the CA Secret referenced by
// sessionStorage.tls is mounted at the path the runtime config points to, that
// a freshly built Deployment is not flagged as drifted, and that switching the
// referenced Secret is detected as drift even though the mounted path (and so
// the runtime config) is unchanged.
func TestSessionRedisTLSCAMountedAndDriftDetected(t *testing.T) {
	tests := []sessionRedisTLSCase{
		{name: "explicit Redis TLS"},
		{name: "global Redis TLS", global: true},
		{name: "global Redis TLS with explicit memory", global: true, memory: true},
		{name: "global Redis stays plaintext when TLS is disabled", global: true, disabled: true},
		{name: "explicit plaintext overrides global CA", global: true, plaintext: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", "")
			testSessionRedisTLSCAMountedAndDriftDetected(t, tt)
		})
	}
}

func testSessionRedisTLSCAMountedAndDriftDetected(t *testing.T, tc sessionRedisTLSCase) {
	t.Helper()
	var startupDefaults *ctrlutil.DefaultRedisConfig
	if tc.global {
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "global-redis:6380")
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", "false")
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", "")
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "")
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME", "global-password")
		t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY", "")
		if !tc.disabled {
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", "true")
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", "redis-ca")
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "ca.crt")
		}
		var err error
		startupDefaults, err = ctrlutil.LoadDefaultRedisConfigFromEnv()
		require.NoError(t, err)
	}
	// Reconciliation must use the validated startup snapshot for the address,
	// password and CA. Re-reading this environment would panic or select a
	// different connection or Secret after startup.
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "changed-redis:6379")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", "invalid")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", "changed-ca")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "changed.pem")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME", "changed-password")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY", "changed-key")

	globalCAKey := "ca.crt"
	defaults := func(caSecret string) *ctrlutil.DefaultRedisConfig {
		if startupDefaults == nil {
			return nil
		}
		cfg := *startupDefaults
		cfg.TLS = startupDefaults.TLS.DeepCopy()
		if cfg.TLS != nil && cfg.TLS.CACertSecretRef != nil {
			cfg.TLS.CACertSecretRef.Name = caSecret
			cfg.TLS.CACertSecretRef.Key = globalCAKey
		}
		return &cfg
	}
	storage := func(caSecret string) *mcpv1beta1.SessionStorageConfig {
		if !tc.global {
			return sessionTLSStorage(caSecret)
		}
		if tc.plaintext {
			return &mcpv1beta1.SessionStorageConfig{Provider: "redis", Address: "local-redis:6379"}
		}
		if tc.memory {
			return &mcpv1beta1.SessionStorageConfig{Provider: "memory"}
		}
		return nil
	}
	assertAddress := func(t *testing.T, address string) {
		t.Helper()
		want := "redis:6380"
		if tc.global {
			want = "global-redis:6380"
		}
		if tc.plaintext {
			want = "local-redis:6379"
		}
		assert.Equal(t, want, address, "the selected address must not be re-read from the environment")
	}

	type harness struct {
		// build returns the desired Deployment for a workload using caSecret.
		build func(t *testing.T, caSecret string) (*appsv1.Deployment, *redisconfig.TLSConfig)
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
			build: func(t *testing.T, caSecret string) (*appsv1.Deployment, *redisconfig.TLSConfig) {
				t.Helper()
				server := mcpServer(caSecret)
				scheme := testutil.NewScheme(t)
				k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).Build()
				r := newTestMCPServerReconciler(k8sClient, scheme, kubernetes.PlatformKubernetes)
				r.DefaultRedisConfig = defaults(caSecret)
				cfg, err := r.createRunConfigFromMCPServer(server)
				require.NoError(t, err)
				require.NotNil(t, cfg.ScalingConfig)
				require.NotNil(t, cfg.ScalingConfig.SessionRedis)
				assertAddress(t, cfg.ScalingConfig.SessionRedis.Address)
				dep, err := r.deploymentForMCPServer(t.Context(), server, "test-checksum")
				require.NoError(t, err)
				return dep, cfg.ScalingConfig.SessionRedis.TLS
			},
			needsUpdate: func(t *testing.T, live *appsv1.Deployment, caSecret string) bool {
				t.Helper()
				r := newTestMCPServerReconciler(nil, testutil.NewScheme(t), kubernetes.PlatformKubernetes)
				r.DefaultRedisConfig = defaults(caSecret)
				return r.deploymentNeedsUpdate(t.Context(), live, mcpServer(caSecret), "test-checksum")
			},
		},
		"MCPRemoteProxy": {
			build: func(t *testing.T, caSecret string) (*appsv1.Deployment, *redisconfig.TLSConfig) {
				t.Helper()
				proxy := remoteProxy(caSecret)
				r := newRemoteProxyTLSReconciler(t, proxy)
				r.DefaultRedisConfig = defaults(caSecret)
				cfg, err := r.createRunConfigFromMCPRemoteProxy(t.Context(), proxy)
				require.NoError(t, err)
				dep := r.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
				if cfg.ScalingConfig == nil || cfg.ScalingConfig.SessionRedis == nil {
					return dep, nil
				}
				assertAddress(t, cfg.ScalingConfig.SessionRedis.Address)
				return dep, cfg.ScalingConfig.SessionRedis.TLS
			},
			needsUpdate: func(t *testing.T, live *appsv1.Deployment, caSecret string) bool {
				t.Helper()
				r := newRemoteProxyTLSReconciler(t, remoteProxy(caSecret))
				r.DefaultRedisConfig = defaults(caSecret)
				return r.deploymentNeedsUpdate(t.Context(), live, remoteProxy(caSecret), "test-checksum")
			},
		},
		"VirtualMCPServer": {
			build: func(t *testing.T, caSecret string) (*appsv1.Deployment, *redisconfig.TLSConfig) {
				t.Helper()
				r := &VirtualMCPServerReconciler{
					DefaultRedisConfig: defaults(caSecret),
					Scheme:             testutil.NewScheme(t),
					PlatformDetector:   ctrlutil.NewSharedPlatformDetector(),
				}
				server := virtualServer(caSecret)
				converter := newTestConverter(t, newNoOpMockResolver(t), operatorvmcpconfig.WithDefaultRedisConfig(r.DefaultRedisConfig))
				cfg, _, err := converter.Convert(t.Context(), server, nil)
				require.NoError(t, err)
				require.NotNil(t, cfg.SessionStorage)
				assertAddress(t, cfg.SessionStorage.Address)
				return r.deploymentForVirtualMCPServer(
					t.Context(), server, "test-checksum", "", nil, []workloads.TypedWorkload{}), cfg.SessionStorage.TLS
			},
			needsUpdate: func(t *testing.T, live *appsv1.Deployment, caSecret string) bool {
				t.Helper()
				r := &VirtualMCPServerReconciler{
					DefaultRedisConfig: defaults(caSecret),
					Scheme:             testutil.NewScheme(t),
					PlatformDetector:   ctrlutil.NewSharedPlatformDetector(),
				}
				return r.deploymentNeedsUpdate(
					t.Context(), live, virtualServer(caSecret), "test-checksum", "", nil, []workloads.TypedWorkload{})
			},
		},
	}

	for name, h := range kinds {
		t.Run(name, func(t *testing.T) {
			dep, runtimeTLS := h.build(t, "redis-ca")
			require.NotNil(t, dep)
			require.NotEmpty(t, dep.Spec.Template.Spec.Containers)
			var password *corev1.EnvVar
			for i := range dep.Spec.Template.Spec.Containers[0].Env {
				env := &dep.Spec.Template.Spec.Containers[0].Env[i]
				if env.Name == session.RedisPasswordEnvVar {
					password = env
				}
			}
			if tc.global && !tc.memory && !tc.plaintext {
				require.NotNil(t, password)
				require.NotNil(t, password.ValueFrom)
				require.NotNil(t, password.ValueFrom.SecretKeyRef)
				assert.Equal(t, "global-password", password.ValueFrom.SecretKeyRef.Name)
				assert.Equal(t, "redis-password", password.ValueFrom.SecretKeyRef.Key)
			} else {
				assert.Nil(t, password, "explicit storage retains its existing password selection")
			}

			var volume *corev1.Volume
			for i := range dep.Spec.Template.Spec.Volumes {
				if dep.Spec.Template.Spec.Volumes[i].Name == ctrlutil.SessionRedisTLSCACertVolumeName {
					volume = &dep.Spec.Template.Spec.Volumes[i]
				}
			}
			if tc.disabled || tc.plaintext || tc.memory && name == "MCPRemoteProxy" {
				assert.Nil(t, runtimeTLS, "plaintext or memory selection must not acquire global TLS")
				assert.Nil(t, volume, "unused global CA must not be projected")
				for _, container := range dep.Spec.Template.Spec.Containers {
					for _, mount := range container.VolumeMounts {
						assert.NotEqual(t, ctrlutil.SessionRedisTLSCACertVolumeName, mount.Name,
							"plaintext or memory selection must not leave an orphan CA mount")
					}
				}
				assert.False(t, h.needsUpdate(t, dep, "redis-ca"))
				return
			}
			require.NotNil(t, runtimeTLS)
			assert.False(t, runtimeTLS.InsecureSkipVerify)
			assert.Equal(t, "/etc/toolhive/session-redis-tls/ca.crt", runtimeTLS.CACertFile)
			require.NotNil(t, volume, "the session storage CA Secret must be mounted")
			require.NotNil(t, volume.Secret)
			assert.Equal(t, "redis-ca", volume.Secret.SecretName)
			assert.Equal(t, []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}, volume.Secret.Items)

			require.NotEmpty(t, dep.Spec.Template.Spec.Containers)
			var caMount *corev1.VolumeMount
			for i := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
				mount := &dep.Spec.Template.Spec.Containers[0].VolumeMounts[i]
				if mount.MountPath == runtimeTLS.CACertFile {
					caMount = mount
				}
			}
			require.NotNil(t, caMount, "the actual runtime CA path must resolve to a workload mount")
			assert.Equal(t, volume.Name, caMount.Name, "the runtime mount must use the selected CA Secret volume")
			assert.True(t, caMount.ReadOnly)
			require.Len(t, volume.Secret.Items, 1)
			assert.Equal(t, volume.Secret.Items[0].Path, caMount.SubPath,
				"the mounted file must be the projected CA key")

			assert.False(t, h.needsUpdate(t, dep, "redis-ca"),
				"a freshly built Deployment must not be flagged as drifted")
			assert.True(t, h.needsUpdate(t, dep, "rotated-redis-ca"),
				"switching the referenced CA Secret must trigger a Deployment update")
			if tc.global {
				globalCAKey = "rotated.pem"
				assert.True(t, h.needsUpdate(t, dep, "redis-ca"),
					"switching the global CA Secret key must trigger a Deployment update")
				globalCAKey = "ca.crt"
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
	t.Parallel()
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
			t.Parallel()
			defaults := &ctrlutil.DefaultRedisConfig{Addr: "global-redis:6380", TLS: &mcpv1beta1.RedisTLSConfig{}}
			if tt.globalCA != "" {
				defaults.TLS.CACertSecretRef = &mcpv1beta1.SecretKeyRef{Name: tt.globalCA, Key: "ca.crt"}
			}
			before := tt.storage.DeepCopy()
			t.Cleanup(func() {
				assert.Equal(t, before, tt.storage, "global fallback must not mutate spec.sessionStorage")
			})
			for _, remote := range []bool{false, true} {
				name := "MCPServer"
				if remote {
					name = "MCPRemoteProxy"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					cfg := &runner.RunConfig{}
					if remote {
						proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxySessionStorage(tt.storage))
						populateScalingConfigForRemoteProxy(cfg, proxy, defaults)
					} else {
						server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithSessionStorage(tt.storage))
						populateScalingConfig(cfg, server, defaults)
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
		})
	}
}
