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
	t.Skip("RunConfig.Validate() now hard-rejects any non-empty spiffe_trust_domains " +
		"(config.go's validateSPIFFENotYetEnforced, per PR #6467 review) until a real " +
		"SVID-verification consumer lands, so a server can no longer be constructed with " +
		"a SPIFFE association configured at all -- there is no way to exercise the " +
		"Redis-backed restart/collision behavior this test proved through " +
		"NewEmbeddedAuthServerWithStorage without routing around cfg.Validate() in " +
		"production code. Re-enable this test -- unmodified -- when the future PR that " +
		"adds real SVID verification removes the hard-reject.")

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
	newStorageWithPrefix := func(prefix string) *storage.RedisStorage {
		return storage.NewRedisStorageWithClient(redis.NewClient(&redis.Options{
			Addr: fmt.Sprintf("%s:%s", host, port.Port()),
		}), prefix)
	}
	config := func(includeAssociation bool) authserver.RunConfig {
		cfg := authserver.RunConfig{
			SchemaVersion:    authserver.CurrentSchemaVersion,
			Issuer:           "https://auth.example.com",
			ScopesSupported:  []string{"openid"},
			AllowedAudiences: []string{"https://mcp.example.com"},
			// A static OAuth2 upstream satisfies runtime configuration validation
			// without discovery or DCR network I/O during server construction.
			Upstreams: []authserver.UpstreamRunConfig{{
				Name: "static-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					AuthorizationEndpoint: "https://upstream.example.com/authorize",
					TokenEndpoint:         "https://upstream.example.com/token",
					ClientID:              "test-client-id",
					RedirectURI:           "https://auth.example.com/oauth/callback",
				},
			}},
		}
		if !includeAssociation {
			return cfg
		}
		cfg.SPIFFETrustDomains = []authserver.SPIFFETrustDomainRunConfig{{
			Name: "production", TrustDomain: "example.org",
			Methods: []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
			BundleSource: authserver.SPIFFEBundleSourceRunConfig{
				Type:        authserver.SPIFFEBundleSourceTypeWorkloadAPI,
				WorkloadAPI: &authserver.SPIFFEWorkloadAPIBundleSourceRunConfig{},
			},
		}}
		cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
			SPIFFEClientAuth: []authserver.SPIFFEClientAuthRunConfig{{
				TrustDomainRef:   "production",
				PrincipalPattern: "spiffe://example.org/ns/default/agent",
				ClientID:         "spiffe-client",
				Methods:          []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
				Scopes:           []string{"openid"},
				Audiences:        []string{"https://mcp.example.com"},
				GrantTypes:       []string{authserver.SPIFFEGrantTypeTokenExchange},
			}},
		}
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
	_, err = secondStorage.GetClient(ctx, "dynamic-client")
	require.NoError(t, err)
	// The static association is gone from the current config, but the earlier
	// SPIFFE-enabled boot durably claimed "spiffe-client" in this same Redis
	// keyspace to close the cross-replica registration race; that durable
	// claim is not retracted just because a later boot's config no longer
	// configures the association. The claim must remain unauthenticatable:
	// no secret and not a public client, so it can never pass client
	// authentication for any grant. (fosite.DefaultClient.GetGrantTypes
	// applies its own single-grant default when the underlying field reads
	// back empty from Redis, so "no grant types" is asserted directly
	// against MemoryStorage in storage/spiffe_decorator_test.go instead.)
	claimed, err := secondStorage.GetClient(ctx, "spiffe-client")
	require.NoError(t, err, "the earlier durable claim for spiffe-client must persist")
	require.Nil(t, claimed.GetHashedSecret())
	require.False(t, claimed.IsPublic())
	require.NoError(t, second.Close())

	// Isolated keyspace so this collision seed isn't itself rejected by the
	// durable claim the earlier steps above left behind.
	collisionStorage := newStorageWithPrefix("integration:spiffe-collision:")
	require.NoError(t, collisionStorage.RegisterClient(ctx, &fosite.DefaultClient{ID: "spiffe-client"}))
	collision := config(true)
	_, err = NewEmbeddedAuthServerWithStorage(ctx, &collision, collisionStorage)
	require.ErrorIs(t, err, storage.ErrAlreadyExists)
}
