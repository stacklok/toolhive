// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/redisconfig"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/testkit/redistls"
)

func TestBuildSessionDataStorageTLS(t *testing.T) {
	t.Setenv(vmcpconfig.RedisPasswordEnvVar, "redis-password")
	redis, caFile := redistls.Start(t)
	redis.RequireAuth("redis-password")
	for _, tc := range []struct {
		name      string
		tls       *redisconfig.TLSConfig
		wantError string
	}{
		{name: "verified custom CA", tls: &redisconfig.TLSConfig{CACertFile: caFile}},
		{name: "untrusted certificate", tls: &redisconfig.TLSConfig{}, wantError: "certificate"},
		{name: "missing configured CA", tls: &redisconfig.TLSConfig{CACertFile: caFile + ".missing"}, wantError: "failed to read Redis CA cert file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(vmcpconfig.RedisPasswordEnvVar, "redis-password")
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			storage, err := buildSessionDataStorage(ctx, &Config{
				SessionTTL: time.Minute,
				SessionStorage: &vmcpconfig.SessionStorageConfig{
					Provider: "redis", Address: redis.Addr(), TLS: tc.tls,
				},
			})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, storage)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, storage.Close()) })
			metadata := map[string]string{"backend": "test-backend"}
			created, err := storage.Create(ctx, "tls-session", metadata)
			require.NoError(t, err)
			assert.True(t, created)
			stored, err := storage.Load(ctx, "tls-session")
			require.NoError(t, err)
			assert.Equal(t, metadata, stored)
		})
	}
}

func TestBuildSessionDataStorageRejectsTLSWithoutRedisProvider(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"", "memory"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			storage, err := buildSessionDataStorage(t.Context(), &Config{
				SessionTTL:     time.Minute,
				SessionStorage: &vmcpconfig.SessionStorageConfig{Provider: provider, TLS: &redisconfig.TLSConfig{}},
			})
			require.ErrorContains(t, err, "session storage TLS requires provider redis")
			assert.Nil(t, storage)
		})
	}
}

// TestBuildSessionDataStorageRedis_InsecureTransportWarns verifies a password
// sent without TLS, or over TLS with certificate verification disabled, is
// tolerated (existing deployments keep starting) but logged at WARN, and that
// verified TLS silences the warning. The WARN is emitted before the connection
// Ping, so an unreachable address is enough.
//
//nolint:paralleltest // env mutation and slog.SetDefault mutate process-global state
func TestBuildSessionDataStorageRedis_InsecureTransportWarns(t *testing.T) {
	t.Setenv(vmcpconfig.RedisPasswordEnvVar, "test-password")

	const (
		plaintextWarn = "without TLS"
		noVerifyWarn  = "without server certificate verification"
	)
	tests := []struct {
		name     string
		tls      *redisconfig.TLSConfig
		wantWarn string
	}{
		{name: "plaintext with password warns", tls: nil, wantWarn: plaintextWarn},
		{
			name:     "TLS without verification warns",
			tls:      &redisconfig.TLSConfig{InsecureSkipVerify: true},
			wantWarn: noVerifyWarn,
		},
		{name: "verified TLS does not warn", tls: &redisconfig.TLSConfig{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf logSyncBuffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			t.Cleanup(cancel)
			_, err := buildSessionDataStorage(ctx, &Config{
				SessionTTL: time.Minute,
				SessionStorage: &vmcpconfig.SessionStorageConfig{
					Provider: "redis",
					Address:  "127.0.0.1:1", // unreachable: Ping fails after the WARN is emitted
					TLS:      tc.tls,
				},
			})
			require.Error(t, err)

			logged := buf.String()
			for _, msg := range []string{plaintextWarn, noVerifyWarn} {
				want := 0
				if msg == tc.wantWarn {
					want = 1
				}
				assert.Equal(t, want, strings.Count(logged, msg), "occurrences of %q", msg)
			}
			assert.NotContains(t, logged, "test-password", "the password must never be logged")
		})
	}
}
