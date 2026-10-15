// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

func TestReadDefaultRedisConfig(t *testing.T) {
	tests := []struct {
		name           string
		addr           string
		passwordSecret string
		passwordKey    string
		enabled        string
		caSecret       string
		caKey          string
		want           *ctrlutil.DefaultRedisConfig
	}{
		{name: "no address leaves global Redis disabled"},
		{name: "legacy address only stays plaintext", addr: "redis:6379",
			want: &ctrlutil.DefaultRedisConfig{Addr: "redis:6379"}},
		{name: "explicitly disabled TLS stays plaintext", addr: "redis:6379", enabled: "false",
			want: &ctrlutil.DefaultRedisConfig{Addr: "redis:6379"}},
		{name: "existing password key default is preserved", addr: "redis:6379", passwordSecret: "redis-creds",
			want: &ctrlutil.DefaultRedisConfig{Addr: "redis:6379", SecretName: "redis-creds", SecretKey: "redis-password"}},
		{name: "configured password key is preserved", addr: "redis:6379", passwordSecret: "redis-creds", passwordKey: "custom",
			want: &ctrlutil.DefaultRedisConfig{Addr: "redis:6379", SecretName: "redis-creds", SecretKey: "custom"}},
		{name: "enabled TLS uses system roots", addr: "redis:6380", enabled: "true",
			want: &ctrlutil.DefaultRedisConfig{Addr: "redis:6380", TLS: &mcpv1beta1.RedisTLSConfig{}}},
		{name: "private CA and password secrets stay independent", addr: "redis:6380", enabled: "true",
			passwordSecret: "redis-creds", caSecret: "redis-ca", caKey: "bundle.pem",
			want: &ctrlutil.DefaultRedisConfig{Addr: "redis:6380", SecretName: "redis-creds", SecretKey: "redis-password",
				TLS: &mcpv1beta1.RedisTLSConfig{CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: "redis-ca", Key: "bundle.pem"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", tt.addr)
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", tt.enabled)
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", tt.caSecret)
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", tt.caKey)
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME", tt.passwordSecret)
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY", tt.passwordKey)
			require.NoError(t, ctrlutil.ValidateDefaultRedisConfig())
			assert.Equal(t, tt.want, ctrlutil.ReadDefaultRedisConfig())
		})
	}
}

func TestValidateDefaultRedisConfigRejectsInvalidTLS(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		enabled  string
		caSecret string
		caKey    string
	}{
		{name: "malformed enable flag", addr: "redis:6380", enabled: "tru"},
		{name: "malformed enable flag without address", enabled: "tru"},
		{name: "TLS enabled without Redis address", enabled: "true"},
		{name: "CA secret without key", addr: "redis:6380", enabled: "true", caSecret: "redis-ca"},
		{name: "CA key without secret", addr: "redis:6380", enabled: "true", caKey: "ca.crt"},
		{name: "CA configured with TLS disabled", addr: "redis:6379", enabled: "false", caSecret: "redis-ca", caKey: "ca.crt"},
		{name: "CA configured with TLS omitted", addr: "redis:6379", caSecret: "redis-ca", caKey: "ca.crt"},
		{name: "CA configured without address", enabled: "true", caSecret: "redis-ca", caKey: "ca.crt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", tt.addr)
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_ENABLED", tt.enabled)
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_NAME", tt.caSecret)
			t.Setenv("TOOLHIVE_DEFAULT_REDIS_TLS_CA_SECRET_KEY", tt.caKey)
			require.Error(t, ctrlutil.ValidateDefaultRedisConfig())
			assert.Panics(t, func() { ctrlutil.ReadDefaultRedisConfig() },
				"invalid global TLS must never silently produce a plaintext configuration")
		})
	}
}
