// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive-core/redisconn"
)

//nolint:paralleltest // slog.SetDefault mutates process-global state
func TestWarnInsecureRedisTransport(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *redisconn.Config
		wantWarn string
	}{
		{name: "nil config does not warn"},
		{
			name:     "plaintext with password warns about the password",
			cfg:      &redisconn.Config{Addr: "redis:6379", Password: "secret-password"},
			wantWarn: "sends its password and session data without TLS",
		},
		{
			name:     "plaintext without password still warns about session data",
			cfg:      &redisconn.Config{Addr: "redis:6379"},
			wantWarn: "sends session data without TLS",
		},
		{
			name: "TLS without verification warns",
			cfg: &redisconn.Config{Addr: "redis:6379", Password: "secret-password",
				TLS: &redisconn.TLSConfig{InsecureSkipVerify: true}},
			wantWarn: "without server certificate verification",
		},
		{
			name: "verified TLS does not warn",
			cfg:  &redisconn.Config{Addr: "redis:6379", Password: "secret-password", TLS: &redisconn.TLSConfig{}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			warnInsecureRedisTransport(tc.cfg)

			logged := buf.String()
			assert.NotContains(t, logged, "secret-password", "the password must never be logged")
			if tc.wantWarn == "" {
				assert.Empty(t, logged)
				return
			}
			assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("level=WARN")), "exactly one WARN")
			assert.Contains(t, logged, tc.wantWarn)
			assert.Contains(t, logged, "store=redis:6379")
		})
	}
}
