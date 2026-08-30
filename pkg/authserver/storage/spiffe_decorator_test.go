// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

func testSPIFFEClients(t *testing.T) map[string]fosite.Client {
	t.Helper()
	client, err := registration.NewSPIFFEClient(
		"spiffe-client",
		[]string{"openid"},
		[]string{"https://api.example.com"},
	)
	require.NoError(t, err)
	return map[string]fosite.Client{client.GetID(): client}
}

func TestSPIFFEStorageDecoratorStaticClients(t *testing.T) {
	t.Parallel()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, testSPIFFEClients(t))
	require.NoError(t, err)
	client, err := decorated.GetClient(context.Background(), "spiffe-client")
	require.NoError(t, err)
	assert.Equal(t, "spiffe-client", client.GetID())
	assert.Empty(t, client.GetResponseTypes())
	require.ErrorIs(t, decorated.RegisterClient(context.Background(), &fosite.DefaultClient{ID: "spiffe-client"}), ErrAlreadyExists)
}

func TestSPIFFEStorageDecoratorRejectsInvalidClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		clients map[string]fosite.Client
		wantErr string
	}{
		{
			name:    "nil client",
			clients: map[string]fosite.Client{"spiffe-client": nil},
			wantErr: `static SPIFFE client "spiffe-client" is required`,
		},
		{
			name: "map key does not match client ID",
			clients: map[string]fosite.Client{
				"map-key": &fosite.DefaultClient{ID: "client-id"},
			},
			wantErr: `map key "map-key" does not match client ID "client-id"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := NewMemoryStorage()
			t.Cleanup(func() { _ = base.Close() })

			_, err := NewSPIFFEStorageDecorator(context.Background(), base, tt.clients)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSPIFFEStorageDecoratorClonesClientMap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	clients := testSPIFFEClients(t)

	decorated, err := NewSPIFFEStorageDecorator(ctx, base, clients)
	require.NoError(t, err)
	delete(clients, "spiffe-client")
	clients["added-client"] = &fosite.DefaultClient{ID: "added-client"}

	client, err := decorated.GetClient(ctx, "spiffe-client")
	require.NoError(t, err)
	assert.Equal(t, "spiffe-client", client.GetID())
	require.ErrorIs(t, decorated.RegisterClient(ctx, &fosite.DefaultClient{ID: "spiffe-client"}), ErrAlreadyExists)

	_, err = decorated.GetClient(ctx, "added-client")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, decorated.RegisterClient(ctx, &fosite.DefaultClient{ID: "added-client"}))
}

func TestSPIFFEStorageDecoratorRejectsDurableCollision(t *testing.T) {
	t.Parallel()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	require.NoError(t, base.RegisterClient(context.Background(), &fosite.DefaultClient{ID: "spiffe-client"}))

	_, err := NewSPIFFEStorageDecorator(context.Background(), base, testSPIFFEClients(t))
	require.ErrorIs(t, err, ErrAlreadyExists)
	assert.Contains(t, err.Error(), "operator-declared")
	assert.Contains(t, err.Error(), "remove the stale registration")
}

func TestSPIFFEStorageDecoratorLeavesEmptyClientsUnchanged(t *testing.T) {
	t.Parallel()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, nil)
	require.NoError(t, err)
	assert.Same(t, base, decorated)
}
