// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
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

func TestNewFailureAfterDelegateRegistration(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { assert.NoError(t, base.Close()) })
	var events []string
	stor := &eventRecordingStorage{MemoryStorage: base, record: func(event string) { events = append(events, event) }}
	provider := &closeCountingProvider{}
	cfg := Config{
		Issuer:           "https://example.com",
		KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		AllowedAudiences: []string{"https://mcp.example.com"},
		DelegateClients: []DelegateClient{{
			ClientID: "delegate", ClientSecret: "delegate-secret-well-above-the-minimum-length",
			Scopes: []string{"openid"}, Audiences: []string{"https://mcp.example.com"},
		}},
		Upstreams: []UpstreamConfig{
			{Name: "first", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()},
			{Name: "second", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()},
		},
		UpstreamFactory: func(_ context.Context, cfg *UpstreamConfig) (upstream.OAuth2Provider, error) {
			if cfg.Name == "first" {
				return provider, nil
			}
			return nil, errors.New("upstream unavailable")
		},
	}

	srv, err := New(t.Context(), cfg, stor)
	require.ErrorContains(t, err, "upstream unavailable")
	require.True(t, srv == nil)
	assert.Empty(t, events, "storage remains caller-owned")
	assert.Equal(t, int32(1), provider.closes.Load(), "constructed upstream is drained")
	client, err := base.GetClient(t.Context(), "delegate")
	require.NoError(t, err, "failed construction may have written to storage")
	assert.Equal(t, "delegate", client.GetID())
}

type failingCloseStorage struct {
	*storage.MemoryStorage
	calls atomic.Int32
	err   error
}

func (s *failingCloseStorage) Close() error {
	s.calls.Add(1)
	return s.err
}

func TestServerCloseOnce(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { assert.NoError(t, base.Close()) })
	closeErr := errors.New("storage close failed")
	stor := &failingCloseStorage{MemoryStorage: base, err: closeErr}
	provider := &closeCountingProvider{}
	srv := &server{storage: stor, upstreams: []handlers.NamedUpstream{{Name: "first", Provider: provider}}}
	results := make(chan error, 10)
	for range 10 {
		go func() { results <- srv.Close() }()
	}
	var first error
	for i := 0; i < 10; i++ {
		select {
		case err := <-results:
			require.ErrorIs(t, err, closeErr)
			if i == 0 {
				first = err
			} else {
				assert.Same(t, first, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Close")
		}
	}
	assert.Same(t, first, srv.Close())
	assert.Equal(t, int32(1), stor.calls.Load())
	assert.Equal(t, int32(1), provider.closes.Load())
}
