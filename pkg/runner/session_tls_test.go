// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/redisconfig"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/test/testkit/redistls"
)

// TestRunner_RunPassesSessionRedisTLS proves Runner.Run wires
// ScalingConfig.SessionRedis.TLS into the session store client. RemoteURL
// skips container setup, and the minimal config makes Run stop at transport
// creation right after the session store step, so the returned error shows
// how far the Redis connection got:
//   - TLS against a server whose CA is not trusted fails certificate
//     verification, so the handshake was attempted.
//   - Omitted TLS fails without any certificate check (plaintext dial).
//   - The right CA but an address the certificate does not cover
//     ("localhost", the certificate only has the 127.0.0.1 IP SAN) fails
//     hostname verification, so ServerName is derived from the address.
//   - The right CA and a matching address passes the session store step and
//     only fails afterwards, when creating the transport.
func TestRunner_RunPassesSessionRedisTLS(t *testing.T) {
	t.Parallel()
	certs, err := redistls.NewCertificates(t.TempDir())
	require.NoError(t, err)
	srv := redistls.StartWithCertificates(t, certs)

	tests := []struct {
		name      string
		addr      string
		tls       *redisconfig.TLSConfig
		wantErr   []string
		unwantErr string
	}{
		{
			name:    "configured TLS performs a verified handshake",
			tls:     &redisconfig.TLSConfig{},
			wantErr: []string{"failed to create Redis session storage", "failed to verify certificate"},
		},
		{
			name:      "omitted TLS stays plaintext",
			wantErr:   []string{"failed to create Redis session storage"},
			unwantErr: "certificate",
		},
		{
			name:    "hostname is verified against the configured address",
			addr:    "localhost:" + srv.Port(),
			tls:     &redisconfig.TLSConfig{CACertFile: certs.CACertFile},
			wantErr: []string{"failed to verify certificate", "localhost"},
		},
		{
			name:      "trusted CA and matching address connect",
			tls:       &redisconfig.TLSConfig{CACertFile: certs.CACertFile},
			wantErr:   []string{"failed to create transport"},
			unwantErr: "session storage",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr := tc.addr
			if addr == "" {
				addr = srv.Addr()
			}
			config := NewRunConfig()
			config.Name = "redis-tls-run"
			config.RemoteURL = "http://127.0.0.1:1/mcp"
			config.ScalingConfig = &ScalingConfig{
				SessionRedis: &SessionRedisConfig{Address: addr, TLS: tc.tls},
			}

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			t.Cleanup(cancel)
			err := NewRunner(config, nil).Run(ctx)
			require.Error(t, err)
			for _, want := range tc.wantErr {
				assert.ErrorContains(t, err, want)
			}
			if tc.unwantErr != "" {
				assert.NotContains(t, err.Error(), tc.unwantErr)
			}
		})
	}
}

func TestSessionRedisTLSRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		tls  *redisconfig.TLSConfig
	}{
		{name: "omitted"},
		{name: "empty enables TLS", tls: &redisconfig.TLSConfig{}},
		{name: "custom CA", tls: &redisconfig.TLSConfig{CACertFile: "/etc/redis/ca.pem"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := SessionRedisConfig{Address: "redis:6379", TLS: tc.tls}
			for _, encoding := range []struct {
				name      string
				marshal   func(any) ([]byte, error)
				unmarshal func([]byte, any) error
			}{{"JSON", json.Marshal, json.Unmarshal}, {"YAML", yaml.Marshal, yaml.Unmarshal}} {
				t.Run(encoding.name, func(t *testing.T) {
					data, err := encoding.marshal(original)
					require.NoError(t, err)
					var got SessionRedisConfig
					require.NoError(t, encoding.unmarshal(data, &got))
					assert.Equal(t, original, got)
					if tc.tls == nil {
						assert.NotContains(t, string(data), "tls")
					} else {
						require.NotNil(t, got.TLS)
					}
				})
			}
		})
	}
}

func TestRateLimitMiddlewareReceivesSessionTLS(t *testing.T) {
	t.Parallel()
	tls := &redisconfig.TLSConfig{CACertFile: "/etc/redis/ca.pem"}
	middlewares, err := addRateLimitMiddleware([]types.MiddlewareConfig{}, &RunConfig{
		Name: "tls-server", RateLimitNamespace: "default",
		RateLimitConfig: &v1beta1.RateLimitConfig{Shared: &v1beta1.RateLimitBucket{
			MaxTokens: 1, RefillPeriod: metav1.Duration{Duration: time.Minute},
		}},
		ScalingConfig: &ScalingConfig{SessionRedis: &SessionRedisConfig{Address: "redis:6379", TLS: tls}},
	})
	require.NoError(t, err)
	require.Len(t, middlewares, 1)
	var params ratelimit.MiddlewareParams
	require.NoError(t, json.Unmarshal(middlewares[0].Parameters, &params))
	assert.Equal(t, tls, params.RedisTLS)
}
