// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8sptr "k8s.io/utils/ptr"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/redisconfig"
)

func TestSessionRedisTLS(t *testing.T) {
	t.Parallel()

	caRef := &mcpv1beta1.SecretKeyRef{Name: "redis-ca", Key: "ca.crt"}
	redis := func(tls *mcpv1beta1.RedisTLSConfig) *mcpv1beta1.SessionStorageConfig {
		return &mcpv1beta1.SessionStorageConfig{
			Provider: mcpv1beta1.SessionStorageProviderRedis, Address: "redis:6380", TLS: tls,
		}
	}

	tests := []struct {
		name          string
		storage       *mcpv1beta1.SessionStorageConfig
		wantRunConfig *redisconfig.TLSConfig
		wantVolume    bool
	}{
		{
			name:          "nil session storage is plaintext with no volume",
			storage:       nil,
			wantRunConfig: nil,
		},
		{
			name:          "redis without tls stays plaintext",
			storage:       redis(nil),
			wantRunConfig: nil,
		},
		{
			name: "tls on a non-redis provider is ignored",
			storage: &mcpv1beta1.SessionStorageConfig{
				Provider: "memory", TLS: &mcpv1beta1.RedisTLSConfig{CACertSecretRef: caRef},
			},
			wantRunConfig: nil,
		},
		{
			name:          "empty tls verifies against system roots without a volume",
			storage:       redis(&mcpv1beta1.RedisTLSConfig{}),
			wantRunConfig: &redisconfig.TLSConfig{},
		},
		{
			name:          "insecureSkipVerify is carried through",
			storage:       redis(&mcpv1beta1.RedisTLSConfig{InsecureSkipVerify: true}),
			wantRunConfig: &redisconfig.TLSConfig{InsecureSkipVerify: true},
		},
		{
			name:          "CA secret is mounted and referenced by path",
			storage:       redis(&mcpv1beta1.RedisTLSConfig{CACertSecretRef: caRef}),
			wantRunConfig: &redisconfig.TLSConfig{CACertFile: "/etc/toolhive/session-redis-tls/ca.crt"},
			wantVolume:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.wantRunConfig, SessionRedisTLSConfig(tc.storage))

			vols, mounts := SessionRedisTLSVolumes(tc.storage)
			if !tc.wantVolume {
				assert.Empty(t, vols)
				assert.Empty(t, mounts)
				return
			}
			assert.Equal(t, []corev1.Volume{caSecretVolume("redis-ca", "ca.crt")}, vols)
			assert.Equal(t, []corev1.VolumeMount{{
				Name:      "session-redis-tls-ca",
				MountPath: "/etc/toolhive/session-redis-tls/ca.crt",
				SubPath:   "ca.crt",
				ReadOnly:  true,
			}}, mounts)
		})
	}
}

func TestSessionRedisTLSVolumeNeedsUpdate(t *testing.T) {
	t.Parallel()

	unrelated := corev1.Volume{Name: "other", VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{},
	}}
	// The API server defaults fields such as DefaultMode differently from what
	// the operator sets; only the projected Secret and items count as drift.
	defaulted := caSecretVolume("redis-ca", "ca.crt")
	defaulted.Secret.DefaultMode = k8sptr.To(int32(0644))

	tests := []struct {
		name    string
		live    []corev1.Volume
		desired []corev1.Volume
		want    bool
	}{
		{name: "no CA volume on either side", live: []corev1.Volume{unrelated}, desired: []corev1.Volume{unrelated}},
		{
			name:    "same CA secret",
			live:    []corev1.Volume{unrelated, caSecretVolume("redis-ca", "ca.crt")},
			desired: []corev1.Volume{caSecretVolume("redis-ca", "ca.crt")},
		},
		{
			name:    "API server defaults are not drift",
			live:    []corev1.Volume{defaulted},
			desired: []corev1.Volume{caSecretVolume("redis-ca", "ca.crt")},
		},
		{
			name:    "CA volume missing from the live pod",
			desired: []corev1.Volume{caSecretVolume("redis-ca", "ca.crt")},
			want:    true,
		},
		{
			name:    "stale CA volume after removing tls",
			live:    []corev1.Volume{caSecretVolume("redis-ca", "ca.crt")},
			desired: []corev1.Volume{unrelated},
			want:    true,
		},
		{
			name:    "different CA secret",
			live:    []corev1.Volume{caSecretVolume("old-ca", "ca.crt")},
			desired: []corev1.Volume{caSecretVolume("redis-ca", "ca.crt")},
			want:    true,
		},
		{
			name:    "different CA secret key",
			live:    []corev1.Volume{caSecretVolume("redis-ca", "tls.crt")},
			desired: []corev1.Volume{caSecretVolume("redis-ca", "ca.crt")},
			want:    true,
		},
		{
			name: "CA volume replaced by a non-secret source",
			live: []corev1.Volume{{Name: "session-redis-tls-ca", VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			}}},
			desired: []corev1.Volume{caSecretVolume("redis-ca", "ca.crt")},
			want:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, SessionRedisTLSVolumeNeedsUpdate(tc.live, tc.desired))
		})
	}
}

// caSecretVolume is the session storage CA volume the operator is expected to
// render for the given Secret name and key.
func caSecretVolume(secret, key string) corev1.Volume {
	return corev1.Volume{
		Name: "session-redis-tls-ca",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  secret,
				Items:       []corev1.KeyToPath{{Key: key, Path: "ca.crt"}},
				DefaultMode: k8sptr.To(int32(0400)),
			},
		},
	}
}

// TLS conversion and projection use their arguments without reading the environment.
func TestSessionRedisTLSIgnoresEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		storage *mcpv1beta1.SessionStorageConfig
		wantTLS *redisconfig.TLSConfig
		wantCA  string
		wantKey string
	}{
		{name: "omitted storage stays unconfigured despite global environment"},
		{name: "explicit plaintext does not inherit global TLS", storage: &mcpv1beta1.SessionStorageConfig{
			Provider: "redis", Address: "local-redis:6379"}},
		{name: "explicit system roots do not inherit global CA", storage: &mcpv1beta1.SessionStorageConfig{
			Provider: "redis", Address: "local-redis:6380", TLS: &mcpv1beta1.RedisTLSConfig{}}, wantTLS: &redisconfig.TLSConfig{}},
		{name: "explicit private CA overrides global CA", storage: &mcpv1beta1.SessionStorageConfig{
			Provider: "redis", Address: "local-redis:6380", TLS: &mcpv1beta1.RedisTLSConfig{
				CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "local-ca", Key: "local.pem"}}},
			wantTLS: &redisconfig.TLSConfig{CACertFile: "/etc/toolhive/session-redis-tls/ca.crt"},
			wantCA:  "local-ca", wantKey: "local.pem"},
		{name: "explicit memory does not select global defaults", storage: &mcpv1beta1.SessionStorageConfig{Provider: "memory"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "global-redis:6380")
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", "invalid")
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", "global-ca")
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", "global.pem")
			before := tt.storage.DeepCopy()
			assert.Equal(t, tt.wantTLS, SessionRedisTLSConfig(tt.storage))
			volumes, mounts := SessionRedisTLSVolumes(tt.storage)
			if tt.wantCA == "" {
				assert.Empty(t, volumes)
				assert.Empty(t, mounts)
			} else {
				require.Len(t, volumes, 1)
				assert.Equal(t, caSecretVolume(tt.wantCA, tt.wantKey), volumes[0])
				require.Len(t, mounts, 1)
				assert.Equal(t, tt.wantTLS.CACertFile, mounts[0].MountPath)
			}
			assert.Equal(t, before, tt.storage, "TLS conversion must not mutate the workload spec")
		})
	}

	memory := &mcpv1beta1.SessionStorageConfig{Provider: "memory"}
	defaults := &DefaultRedisConfig{Addr: "global-redis:6380", TLS: &mcpv1beta1.RedisTLSConfig{
		CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "global-ca", Key: "global.pem"},
	}}
	volumes, mounts := SessionRedisTLSVolumesWithDefault(memory, defaults)
	require.Len(t, volumes, 1)
	assert.Equal(t, caSecretVolume("global-ca", "global.pem"), volumes[0])
	require.Len(t, mounts, 1)
	assert.Equal(t, "/etc/toolhive/session-redis-tls/ca.crt", mounts[0].MountPath,
		"MCPServer and VirtualMCPServer keep their existing non-Redis global fallback")
	assert.Equal(t, &mcpv1beta1.SessionStorageConfig{Provider: "memory"}, memory)

	assert.Nil(t, SessionRedisTLSConfig(nil))
	volumes, mounts = SessionRedisTLSVolumes(nil)
	assert.Empty(t, volumes)
	assert.Empty(t, mounts)

	defaults.TLS = &mcpv1beta1.RedisTLSConfig{}
	assert.Equal(t, &redisconfig.TLSConfig{}, SessionRedisTLSConfig(defaults.SessionStorage()))
	volumes, mounts = SessionRedisTLSVolumes(defaults.SessionStorage())
	assert.Empty(t, volumes, "system roots need no CA Secret projection")
	assert.Empty(t, mounts)
}
