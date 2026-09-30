// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
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
)

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
