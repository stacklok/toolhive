// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func testSPIFFEAssociationRegistry(t *testing.T, clientID string) (*SPIFFEAssociationRegistry, []SPIFFEClientAuthRunConfig) {
	t.Helper()

	associations := []SPIFFEClientAuthRunConfig{{
		TrustDomainRef: "production",
		Principal:      "spiffe://example.org/ns/default/agent",
		ClientID:       clientID,
		Methods:        []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
		GrantTypes:     []string{SPIFFEGrantTypeTokenExchange},
		Scopes:         []string{"openid"},
		Resources:      []string{"https://resource.example.com"},
		Audiences:      []string{"https://api.example.com"},
		TokenExchange:  &SPIFFETokenExchangeRunConfig{Enabled: true},
	}}
	trust, err := NewSPIFFETrustConfig([]SPIFFETrustDomainRunConfig{{
		Name:        "production",
		TrustDomain: "example.org",
		Methods:     []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
		BundleSource: SPIFFEBundleSourceRunConfig{
			Type:        SPIFFEBundleSourceTypeWorkloadAPI,
			WorkloadAPI: &SPIFFEWorkloadAPIBundleSourceRunConfig{},
		},
	}}, &InboundGrantsRunConfig{SPIFFEClientAuth: associations}, []string{"openid"}, []string{"https://resource.example.com", "https://api.example.com"})
	require.NoError(t, err)
	registry, err := NewSPIFFEAssociationRegistry(trust)
	require.NoError(t, err)
	return registry, associations
}

func TestSPIFFEStorageDecorator_StaticClients(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	registry, sourceAssociations := testSPIFFEAssociationRegistry(t, "spiffe-client")

	// Neither the caller-owned source config nor the immutable registry may
	// change the static client authority.
	sourceAssociations[0].ClientID = "changed-client"
	sourceAssociations[0].GrantTypes[0] = "changed"
	sourceAssociations[0].Scopes[0] = "changed"
	sourceAssociations[0].Resources[0] = "changed"
	sourceAssociations[0].Audiences[0] = "changed"
	sourceAssociations[0].TokenExchange.Enabled = false

	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, registry)
	require.NoError(t, err)
	client, err := decorated.GetClient(context.Background(), "spiffe-client")
	require.NoError(t, err)
	assert.Equal(t, "spiffe-client", client.GetID())
	assert.False(t, client.IsPublic())
	assert.Nil(t, client.GetHashedSecret())
	assert.Equal(t, fosite.Arguments{SPIFFEGrantTypeTokenExchange}, client.GetGrantTypes())
	assert.Equal(t, fosite.Arguments{"openid"}, client.GetScopes())
	staticClient, ok := client.(*registration.SPIFFEClient)
	require.True(t, ok)
	assert.Equal(t, []string{"https://resource.example.com"}, staticClient.Resources())
	assert.Equal(t, []string{"https://api.example.com"}, staticClient.Audiences())
	assert.True(t, staticClient.TokenExchangeEnabled())
	assert.Empty(t, client.GetRedirectURIs())
	assert.Empty(t, client.GetResponseTypes())
	assert.ElementsMatch(t, []string{"https://resource.example.com", "https://api.example.com"}, client.GetAudience())

	client.GetGrantTypes()[0] = "mutated"
	client.GetScopes()[0] = "mutated"
	staticClient.Resources()[0] = "mutated"
	staticClient.Audiences()[0] = "mutated"
	assert.Equal(t, fosite.Arguments{SPIFFEGrantTypeTokenExchange}, client.GetGrantTypes())
	assert.Equal(t, fosite.Arguments{"openid"}, client.GetScopes())
	assert.Equal(t, []string{"https://resource.example.com"}, staticClient.Resources())
	assert.Equal(t, []string{"https://api.example.com"}, staticClient.Audiences())

	_, err = base.GetClient(context.Background(), "spiffe-client")
	require.ErrorIs(t, err, storage.ErrNotFound)

	err = decorated.RegisterClient(context.Background(), &fosite.DefaultClient{ID: "spiffe-client"})
	require.ErrorIs(t, err, storage.ErrAlreadyExists)

	require.NoError(t, decorated.RegisterClient(context.Background(), &fosite.DefaultClient{ID: "dcr-client"}))
	client, err = base.GetClient(context.Background(), "dcr-client")
	require.NoError(t, err)
	assert.Equal(t, "dcr-client", client.GetID())
}

func TestSPIFFEStorageDecorator_RebuildsOnFreshMemoryRestart(t *testing.T) {
	t.Parallel()

	registry, _ := testSPIFFEAssociationRegistry(t, "spiffe-client")
	for range 2 {
		base := storage.NewMemoryStorage()
		decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, registry)
		require.NoError(t, err)

		client, err := decorated.GetClient(context.Background(), "spiffe-client")
		require.NoError(t, err)
		assert.Equal(t, "spiffe-client", client.GetID())
		require.NoError(t, base.Close())
	}
}

func TestSPIFFEStorageDecorator_RemovedConfigDoesNotRestoreStaticClient(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	registry, _ := testSPIFFEAssociationRegistry(t, "spiffe-client")
	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, registry)
	require.NoError(t, err)
	client, err := decorated.GetClient(context.Background(), "spiffe-client")
	require.NoError(t, err)
	_, ok := client.(*registration.SPIFFEClient)
	require.True(t, ok)

	// A row that exists after configuration removal is dynamic authority, not a
	// restored static policy. The static client was never persisted.
	require.NoError(t, base.RegisterClient(context.Background(), &fosite.DefaultClient{ID: "spiffe-client"}))
	removed, err := NewSPIFFEStorageDecorator(context.Background(), base, nil)
	require.NoError(t, err)
	client, err = removed.GetClient(context.Background(), "spiffe-client")
	require.NoError(t, err)
	_, ok = client.(*registration.SPIFFEClient)
	assert.False(t, ok)
}

func TestSPIFFEStorageDecorator_ReservedIDCannotReachDurableStorage(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	registry, _ := testSPIFFEAssociationRegistry(t, "spiffe-client")
	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, registry)
	require.NoError(t, err)

	const registrations = 16
	start := make(chan struct{})
	errs := make(chan error, registrations)
	var wg sync.WaitGroup
	for range registrations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- decorated.RegisterClient(context.Background(), &fosite.DefaultClient{ID: "spiffe-client"})
		}()
	}
	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reserved-ID registrations")
	}
	close(errs)

	for err := range errs {
		require.ErrorIs(t, err, storage.ErrAlreadyExists)
	}
	_, err = base.GetClient(context.Background(), "spiffe-client")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestSPIFFEStorageDecorator_DelegatesCIMDAndUnknownSPIFFEIDs(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	cimd, err := storage.NewCIMDStorageDecorator(base, storage.CIMDDecoratorConfig{Enabled: true, CacheMaxSize: 1})
	require.NoError(t, err)
	registry, _ := testSPIFFEAssociationRegistry(t, "https://static.example/client")

	decorated, err := NewSPIFFEStorageDecorator(context.Background(), cimd, registry)
	require.NoError(t, err)
	assert.Same(t, base, storage.Unwrap(decorated))

	// A configured HTTPS ID wins over CIMD, while an unknown spiffe:// ID is
	// delegated. CIMD recognizes HTTPS IDs only, so no SPIFFE value is resolved
	// over the network.
	client, err := decorated.GetClient(context.Background(), "https://static.example/client")
	require.NoError(t, err)
	assert.Equal(t, "https://static.example/client", client.GetID())

	_, err = decorated.GetClient(context.Background(), "spiffe://example.org/ns/default/unknown")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestNewSPIFFEStorageDecorator_RejectsDurableCollision(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	require.NoError(t, base.RegisterClient(context.Background(), &fosite.DefaultClient{ID: "spiffe-client"}))
	registry, _ := testSPIFFEAssociationRegistry(t, "spiffe-client")

	_, err := NewSPIFFEStorageDecorator(context.Background(), base, registry)
	require.ErrorIs(t, err, storage.ErrAlreadyExists)
}

func TestNewSPIFFEAssociationRegistry_RejectsUnvalidatedTrust(t *testing.T) {
	t.Parallel()

	_, err := NewSPIFFEAssociationRegistry(&SPIFFETrustConfig{})
	require.Error(t, err)
}

func TestNewSPIFFEStorageDecorator_NilRegistryLeavesDynamicStorage(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })

	decorated, err := NewSPIFFEStorageDecorator(context.Background(), base, nil)
	require.NoError(t, err)
	assert.Same(t, base, decorated)
}
