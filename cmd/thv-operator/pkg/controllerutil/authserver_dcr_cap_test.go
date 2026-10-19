// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilptr "k8s.io/utils/ptr"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func redisStorageConfig(maxDCRClients *int32) *mcpv1beta1.AuthServerStorageConfig {
	return &mcpv1beta1.AuthServerStorageConfig{
		Type: mcpv1beta1.AuthServerStorageTypeRedis,
		Redis: &mcpv1beta1.RedisStorageConfig{
			Addr: "redis.example.com:6379",
			ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
				PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-secret", Key: "password"},
			},
		},
		MaxDCRClients: maxDCRClients,
	}
}

func TestBuildStorageRunConfig_MaxDCRClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		storage  *mcpv1beta1.AuthServerStorageConfig
		wantNil  bool
		wantType storage.Type
		wantMax  *int
	}{
		{
			name:    "memory without a cap keeps the default config",
			storage: &mcpv1beta1.AuthServerStorageConfig{Type: mcpv1beta1.AuthServerStorageTypeMemory},
			wantNil: true,
		},
		{
			name: "memory with a cap",
			storage: &mcpv1beta1.AuthServerStorageConfig{
				Type:          mcpv1beta1.AuthServerStorageTypeMemory,
				MaxDCRClients: utilptr.To[int32](500),
			},
			wantType: storage.TypeMemory,
			wantMax:  utilptr.To(500),
		},
		{
			name: "memory with an unlimited cap",
			storage: &mcpv1beta1.AuthServerStorageConfig{
				Type:          mcpv1beta1.AuthServerStorageTypeMemory,
				MaxDCRClients: utilptr.To[int32](0),
			},
			wantType: storage.TypeMemory,
			wantMax:  utilptr.To(0),
		},
		{
			name:     "redis without a cap leaves it unset",
			storage:  redisStorageConfig(nil),
			wantType: storage.TypeRedis,
		},
		{
			name:     "redis with a cap",
			storage:  redisStorageConfig(utilptr.To[int32](50000)),
			wantType: storage.TypeRedis,
			wantMax:  utilptr.To(50000),
		},
		{
			name:     "redis with an unlimited cap",
			storage:  redisStorageConfig(utilptr.To[int32](0)),
			wantType: storage.TypeRedis,
			wantMax:  utilptr.To(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := buildStorageRunConfig("default", "test-server", &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:  "https://auth.example.com",
				Storage: tt.storage,
			})
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, cfg)
				return
			}
			require.NotNil(t, cfg)
			assert.Equal(t, string(tt.wantType), cfg.Type)
			assert.Equal(t, tt.wantMax, cfg.MaxDCRClients)

			// The auth server must accept what the operator writes.
			resolved, err := cfg.ResolvedMaxDCRClients()
			require.NoError(t, err)
			if tt.wantMax == nil {
				assert.Equal(t, storage.DefaultMaxDCRClients, resolved)
			} else {
				assert.Equal(t, *tt.wantMax, resolved)
			}
		})
	}
}

func TestBuildAuthServerRunConfig_RegistrationRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		bucket      *mcpv1beta1.RateLimitBucket
		want        *authserver.RegistrationRateLimitRunConfig
		errContains string
	}{
		{
			name: "omitted keeps the auth server default",
		},
		{
			name:   "documented default expressed as a bucket",
			bucket: &mcpv1beta1.RateLimitBucket{MaxTokens: 5, RefillPeriod: metav1.Duration{Duration: 5 * time.Second}},
			want:   &authserver.RegistrationRateLimitRunConfig{RequestsPerSecond: 1, Burst: 5},
		},
		{
			name:   "slower than one per second",
			bucket: &mcpv1beta1.RateLimitBucket{MaxTokens: 10, RefillPeriod: metav1.Duration{Duration: 100 * time.Second}},
			want:   &authserver.RegistrationRateLimitRunConfig{RequestsPerSecond: 0.1, Burst: 10},
		},
		{
			name:        "zero refill period is rejected",
			bucket:      &mcpv1beta1.RateLimitBucket{MaxTokens: 5},
			errContains: "registrationRateLimit.refillPeriod must be positive",
		},
		{
			name:        "negative refill period is rejected",
			bucket:      &mcpv1beta1.RateLimitBucket{MaxTokens: 5, RefillPeriod: metav1.Duration{Duration: -time.Second}},
			errContains: "registrationRateLimit.refillPeriod must be positive",
		},
		{
			name:        "zero maxTokens is rejected",
			bucket:      &mcpv1beta1.RateLimitBucket{RefillPeriod: metav1.Duration{Duration: time.Second}},
			errContains: "registration_rate_limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := BuildAuthServerRunConfig("default", "test-server", &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:                "https://auth.example.com",
				RegistrationRateLimit: tt.bucket,
			}, []string{"https://mcp.example.com"}, []string{"openid"}, "https://mcp.example.com")

			if tt.errContains != "" {
				require.ErrorContains(t, err, tt.errContains)
				var invalid *InvalidEmbeddedAuthServerConfigError
				assert.ErrorAs(t, err, &invalid, "a bad spec must surface as an invalid config, not a transient error")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.RegistrationRateLimit)
		})
	}
}
