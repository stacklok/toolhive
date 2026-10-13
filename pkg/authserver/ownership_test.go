// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func TestNewFailureLeavesStorageCallerOwned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         Config
		errContains string
	}{
		{name: "validation failure", errContains: "issuer is required"},
		{
			name: "upstream construction failure",
			cfg: Config{
				Issuer:           "https://example.com",
				KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
				AllowedAudiences: []string{"https://mcp.example.com"},
				Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
				UpstreamFactory: func(context.Context, *UpstreamConfig) (upstream.OAuth2Provider, error) {
					return nil, errors.New("upstream construction failed")
				},
			},
			errContains: "upstream construction failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { assert.NoError(t, base.Close()) })
			var events []string
			stor := &eventRecordingStorage{MemoryStorage: base, record: func(event string) { events = append(events, event) }}
			srv, err := New(t.Context(), tt.cfg, stor)
			require.ErrorContains(t, err, tt.errContains)
			require.True(t, srv == nil, "failed construction must return a nil server interface")
			assert.Empty(t, events, "failed construction must not close caller storage")
		})
	}
}
