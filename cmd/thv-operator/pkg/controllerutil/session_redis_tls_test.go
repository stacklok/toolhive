// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

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
	caVolume := func(secret string) corev1.Volume {
		vols, _ := SessionRedisTLSVolumes(redis(&mcpv1beta1.RedisTLSConfig{
			CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: secret, Key: "ca.crt"},
		}))
		return vols[0]
	}

	tests := []struct {
		name          string
		storage       *mcpv1beta1.SessionStorageConfig
		liveVolumes   []corev1.Volume
		wantRunConfig *redisconfig.TLSConfig
		wantVolume    bool
		wantDrift     bool
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
			liveVolumes:   []corev1.Volume{caVolume("redis-ca")},
			wantRunConfig: &redisconfig.TLSConfig{CACertFile: "/etc/toolhive/session-redis-tls/ca.crt"},
			wantVolume:    true,
		},
		{
			name:          "CA secret missing from the live pod is drift",
			storage:       redis(&mcpv1beta1.RedisTLSConfig{CACertSecretRef: caRef}),
			wantRunConfig: &redisconfig.TLSConfig{CACertFile: "/etc/toolhive/session-redis-tls/ca.crt"},
			wantVolume:    true,
			wantDrift:     true,
		},
		{
			name:          "live pod mounting a different CA secret is drift",
			storage:       redis(&mcpv1beta1.RedisTLSConfig{CACertSecretRef: caRef}),
			liveVolumes:   []corev1.Volume{caVolume("old-ca")},
			wantRunConfig: &redisconfig.TLSConfig{CACertFile: "/etc/toolhive/session-redis-tls/ca.crt"},
			wantVolume:    true,
			wantDrift:     true,
		},
		{
			name:          "stale CA volume after removing tls is drift",
			storage:       redis(nil),
			liveVolumes:   []corev1.Volume{caVolume("redis-ca")},
			wantRunConfig: nil,
			wantDrift:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.wantRunConfig, SessionRedisTLSConfig(tc.storage))

			vols, mounts := SessionRedisTLSVolumes(tc.storage)
			if tc.wantVolume {
				assert.Len(t, vols, 1)
				assert.Len(t, mounts, 1)
				assert.Equal(t, "redis-ca", vols[0].Secret.SecretName)
			} else {
				assert.Empty(t, vols)
				assert.Empty(t, mounts)
			}

			assert.Equal(t, tc.wantDrift, SessionRedisTLSVolumeNeedsUpdate(tc.liveVolumes, tc.storage))
		})
	}
}
