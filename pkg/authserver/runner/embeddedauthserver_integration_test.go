// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package runner

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestIntegration_EmbeddedAuthServer_SPIFFERedisRestartAndCollision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp"),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	newStorage := func() *storage.RedisStorage {
		return storage.NewRedisStorageWithClient(redis.NewClient(&redis.Options{
			Addr: fmt.Sprintf("%s:%s", host, port.Port()),
		}), "integration:spiffe:")
	}
	config := func(includeAssociation bool) authserver.RunConfig {
		cfg := authserver.RunConfig{
			SchemaVersion:    authserver.CurrentSchemaVersion,
			Issuer:           "https://auth.example.com",
			ScopesSupported:  []string{"openid"},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}
		if !includeAssociation {
			return cfg
		}
		cfg.SPIFFETrustDomains = []authserver.SPIFFETrustDomainRunConfig{{
			Name: "production", TrustDomain: "example.org",
			Methods: []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
			BundleSource: authserver.SPIFFEBundleSourceRunConfig{
				Type: authserver.SPIFFEBundleSourceTypeWorkloadAPI,
				WorkloadAPI: &authserver.SPIFFEWorkloadAPIBundleSourceRunConfig{},
			},
		}}
		cfg.InboundGrants = &authserver.InboundGrantsRunConfig{SPIFFEClientAuth: []authserver.SPIFFEClientAuthRunConfig{{
			TrustDomainRef: "production", Principal: "spiffe://example.org/ns/default/agent", ClientID: "spiffe-client",
			Methods: []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
			GrantTypes: []string{authserver.SPIFFEGrantTypeTokenExchange}, Scopes: []string{"openid"},
			Resources: []string{"https://mcp.example.com"}, Audiences: []string{"mcp-api"},
			TokenExchange: &authserver.SPIFFETokenExchangeRunConfig{Enabled: true},
		}}}
		return cfg
	}

	firstStorage := newStorage()
	require.NoError(t, firstStorage.RegisterClient(ctx, &fosite.DefaultClient{ID: "dynamic-client"}))
	initial := config(true)
	first, err := NewEmbeddedAuthServerWithStorage(ctx, &initial, firstStorage)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	secondStorage := newStorage()
	removed := config(false)
	second, err := NewEmbeddedAuthServerWithStorage(ctx, &removed, secondStorage)
	require.NoError(t, err)
	_, err = second.ClientRegistry().GetClient(ctx, "dynamic-client")
	require.NoError(t, err)
	_, err = second.ClientRegistry().GetClient(ctx, "spiffe-client")
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.NoError(t, second.Close())

	collisionStorage := newStorage()
	require.NoError(t, collisionStorage.RegisterClient(ctx, &fosite.DefaultClient{ID: "spiffe-client"}))
	collision := config(true)
	_, err = NewEmbeddedAuthServerWithStorage(ctx, &collision, collisionStorage)
	require.ErrorIs(t, err, storage.ErrAlreadyExists)
}

// TestIntegration_EmbeddedAuthServer_DelegateClientRedisRestart pins a
// restart regression: registerDelegateClients runs on every boot, and a
// blanket create-only RegisterClient would make the second boot against a
// persistent backend fail with ErrAlreadyExists once the delegate client was
// already registered by the first boot.
func TestIntegration_EmbeddedAuthServer_DelegateClientRedisRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp"),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	t.Setenv("DELEGATE_CLIENT_SECRET", "delegate-secret-well-above-the-minimum-length")
	cfg := authserver.RunConfig{
		SchemaVersion:    authserver.CurrentSchemaVersion,
		Issuer:           "https://auth.example.com",
		ScopesSupported:  []string{"openid"},
		AllowedAudiences: []string{"https://mcp.example.com"},
		Upstreams: []authserver.UpstreamRunConfig{{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://upstream.example.com/authorize",
				TokenEndpoint:         "https://upstream.example.com/token",
				ClientID:              "upstream-client-id",
				RedirectURI:           "https://auth.example.com/oauth/callback",
			},
		}},
		DelegateClients: []authserver.DelegateClientRunConfig{{
			ClientID:           "delegate",
			ClientSecretEnvVar: "DELEGATE_CLIENT_SECRET",
			Scopes:             []string{"openid"},
			Audiences:          []string{"https://mcp.example.com"},
		}},
	}

	// Same key prefix on both boots: the second EmbeddedAuthServer reuses the
	// first boot's persisted delegate-client registration in Redis.
	newStorage := func() *storage.RedisStorage {
		return storage.NewRedisStorageWithClient(redis.NewClient(&redis.Options{
			Addr: fmt.Sprintf("%s:%s", host, port.Port()),
		}), "integration:delegate-restart:")
	}

	first, err := NewEmbeddedAuthServerWithStorage(ctx, &cfg, newStorage())
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := NewEmbeddedAuthServerWithStorage(ctx, &cfg, newStorage())
	require.NoError(t, err, "second boot must not fail re-registering the same delegate client")
	require.NoError(t, second.Close())
}
