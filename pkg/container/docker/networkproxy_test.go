// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNetworkProxy(t *testing.T) {
	// t.Setenv is used in subtests so the outer test must NOT call t.Parallel().
	// The subtests run sequentially to avoid concurrent environment mutation.

	tests := []struct {
		name        string
		envValue    string
		wantSquid   bool
		wantEnvoy   bool
		wantErr     bool
		errContains []string
	}{
		{
			name:      "empty env returns squidProxy",
			envValue:  "",
			wantSquid: true,
			wantErr:   false,
		},
		{
			name:      "squid returns squidProxy",
			envValue:  "squid",
			wantSquid: true,
			wantErr:   false,
		},
		{
			// After Stage 1 this case becomes a success path — envoy returns
			// *envoyProxy, no error. Flip wantErr to false and add wantEnvoy
			// assertion once envoy.go exists.
			name:      "envoy returns envoyProxy",
			envValue:  "envoy",
			wantSquid: false,
			wantEnvoy: true,
			wantErr:   false,
		},
		{
			name:        "bogus value returns error",
			envValue:    "bogus",
			wantSquid:   false,
			wantErr:     true,
			errContains: []string{"bogus"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv is incompatible with t.Parallel() — env mutations are
			// global state; subtests run sequentially within this parent test.
			t.Setenv("TOOLHIVE_NETWORK_PROXY", tt.envValue)

			c := &Client{}
			proxy, err := newNetworkProxy(c)

			if tt.wantErr {
				require.Error(t, err)
				for _, substr := range tt.errContains {
					assert.Contains(t, err.Error(), substr)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, proxy)

			if tt.wantSquid {
				_, ok := proxy.(*squidProxy)
				assert.True(t, ok, "expected proxy to be *squidProxy, got %T", proxy)
			}

			if tt.wantEnvoy {
				_, ok := proxy.(*envoyProxy)
				assert.True(t, ok, "expected proxy to be *envoyProxy, got %T", proxy)
			}
		})
	}
}
