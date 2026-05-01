// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSessionStorageConfigJSONRoundtrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    SessionStorageConfig
		wantJSON string
	}{
		{
			name: "memory provider",
			input: SessionStorageConfig{
				Provider: "memory",
			},
			wantJSON: `{"provider":"memory"}`,
		},
		{
			name: "redis provider with address",
			input: SessionStorageConfig{
				Provider: "redis",
				Address:  "redis:6379",
			},
			wantJSON: `{"provider":"redis","address":"redis:6379"}`,
		},
		{
			name: "redis provider with all fields",
			input: SessionStorageConfig{
				Provider:  "redis",
				Address:   "redis:6379",
				DB:        1,
				KeyPrefix: "thv:",
			},
			wantJSON: `{"provider":"redis","address":"redis:6379","db":1,"keyPrefix":"thv:"}`,
		},
		{
			name: "db zero is omitted",
			input: SessionStorageConfig{
				Provider: "redis",
				Address:  "redis:6379",
				DB:       0,
			},
			wantJSON: `{"provider":"redis","address":"redis:6379"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tc.input)
			require.NoError(t, err)
			assert.JSONEq(t, tc.wantJSON, string(b))
		})
	}
}

func TestRateLimitConfigJSONRoundtrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    RateLimitConfig
		wantJSON string
	}{
		{
			name: "shared only",
			input: RateLimitConfig{
				Shared: &RateLimitBucket{MaxTokens: 100, RefillPeriod: metav1.Duration{Duration: time.Minute}},
			},
			wantJSON: `{"shared":{"maxTokens":100,"refillPeriod":"1m0s"}}`,
		},
		{
			name: "tools only",
			input: RateLimitConfig{
				Tools: []ToolRateLimitConfig{
					{Name: "search", Shared: &RateLimitBucket{MaxTokens: 5, RefillPeriod: metav1.Duration{Duration: 10 * time.Second}}},
				},
			},
			wantJSON: `{"tools":[{"name":"search","shared":{"maxTokens":5,"refillPeriod":"10s"}}]}`,
		},
		{
			name: "shared with tools",
			input: RateLimitConfig{
				Shared: &RateLimitBucket{MaxTokens: 100, RefillPeriod: metav1.Duration{Duration: time.Minute}},
				Tools: []ToolRateLimitConfig{
					{
						Name:   "search",
						Shared: &RateLimitBucket{MaxTokens: 5, RefillPeriod: metav1.Duration{Duration: 10 * time.Second}},
					},
				},
			},
			wantJSON: `{"shared":{"maxTokens":100,"refillPeriod":"1m0s"},"tools":[{"name":"search","shared":{"maxTokens":5,"refillPeriod":"10s"}}]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tc.input)
			require.NoError(t, err)
			assert.JSONEq(t, tc.wantJSON, string(b))
		})
	}
}

func TestMCPServerSpecScalingFieldsJSONRoundtrip(t *testing.T) {
	t.Parallel()

	replicas := int32(3)
	backendReplicas := int32(2)

	tests := []struct {
		name       string
		spec       MCPServerSpec
		wantKeys   []string
		wantAbsent []string
	}{
		{
			name:       "nil replicas are omitted",
			spec:       MCPServerSpec{Image: "example/mcp:latest"},
			wantAbsent: []string{`"replicas"`, `"backendReplicas"`, `"sessionStorage"`, `"rateLimiting"`},
		},
		{
			name: "set replicas are serialized",
			spec: MCPServerSpec{
				Image:           "example/mcp:latest",
				Replicas:        &replicas,
				BackendReplicas: &backendReplicas,
			},
			wantKeys: []string{`"replicas":3`, `"backendReplicas":2`},
		},
		{
			name: "sessionStorage is serialized when set",
			spec: MCPServerSpec{
				Image: "example/mcp:latest",
				SessionStorage: &SessionStorageConfig{
					Provider: "redis",
					Address:  "redis:6379",
				},
			},
			wantKeys: []string{`"sessionStorage"`, `"provider":"redis"`},
		},
		{
			name: "rateLimiting is serialized when set",
			spec: MCPServerSpec{
				Image: "example/mcp:latest",
				RateLimiting: &RateLimitConfig{
					Shared: &RateLimitBucket{MaxTokens: 100, RefillPeriod: metav1.Duration{Duration: time.Minute}},
				},
			},
			wantKeys: []string{`"rateLimiting"`, `"maxTokens":100`, `"refillPeriod":"1m0s"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tc.spec)
			require.NoError(t, err)
			out := string(b)
			for _, key := range tc.wantKeys {
				assert.Contains(t, out, key)
			}
			for _, key := range tc.wantAbsent {
				assert.NotContains(t, out, key)
			}
		})
	}
}
