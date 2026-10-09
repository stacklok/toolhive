// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/redisconfig"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestSessionStorageTLSValidationAndSerialization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, provider string
		tls            *redisconfig.TLSConfig
		wantError      bool
	}{
		{name: "memory TLS rejected", provider: "memory", tls: &redisconfig.TLSConfig{}, wantError: true},
		{name: "empty provider TLS rejected", tls: &redisconfig.TLSConfig{}, wantError: true},
		{name: "Redis empty TLS", provider: "redis", tls: &redisconfig.TLSConfig{}},
		{name: "Redis mounted CA file not read during validation", provider: "redis", tls: &redisconfig.TLSConfig{CACertFile: "/not-present-until-pod-start/ca.pem"}},
		{name: "Redis plaintext", provider: "redis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := &Config{
				Name: "tls-vmcp", Group: "group",
				IncomingAuth:   &IncomingAuthConfig{Type: "anonymous"},
				OutgoingAuth:   &OutgoingAuthConfig{Source: "inline"},
				Aggregation:    &AggregationConfig{ConflictResolution: vmcp.ConflictStrategyPrefix, ConflictResolutionConfig: &ConflictResolutionConfig{PrefixFormat: "{workload}_"}},
				SessionStorage: &SessionStorageConfig{Provider: tc.provider, Address: "redis:6379", TLS: tc.tls},
			}
			err := NewValidator().Validate(config)
			if tc.wantError {
				require.ErrorContains(t, err, "TLS requires provider redis")
				return
			}
			require.NoError(t, err)
			for _, encoding := range []struct {
				name      string
				marshal   func(any) ([]byte, error)
				unmarshal func([]byte, any) error
			}{
				{"JSON", json.Marshal, json.Unmarshal}, {"YAML", yaml.Marshal, yaml.Unmarshal},
			} {
				t.Run(encoding.name, func(t *testing.T) {
					data, err := encoding.marshal(config.SessionStorage)
					require.NoError(t, err)
					var storage SessionStorageConfig
					require.NoError(t, encoding.unmarshal(data, &storage))
					assert.Equal(t, config.SessionStorage, &storage)
				})
			}
		})
	}
}
