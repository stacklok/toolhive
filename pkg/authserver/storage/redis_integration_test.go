// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Tests use the withIntegrationStorage helper which calls t.Parallel() internally,
// making all subtests parallel despite not having explicit t.Parallel() calls.
//
//nolint:paralleltest // parallel execution handled by withIntegrationStorage helper
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
)

// --- Constants ---

const (
	testMasterName = "mymaster"
	testACLUser    = "thvuser"
	testACLPass    = "integration-test-password"
	testRedisImage = "redis:7-alpine"
)

// --- Redis Sentinel Cluster ---

// redisSentinelCluster manages a Docker-based Redis Sentinel cluster for integration testing.
// It consists of 1 primary + 2 replicas + 3 sentinels.
type redisSentinelCluster struct {
	primary  testcontainers.Container
	replicas [2]testcontainers.Container
	sents    [3]testcontainers.Container
	net      *testcontainers.DockerNetwork

	// Host-accessible sentinel addresses (localhost:port).
	SentinelAddrs []string

	// Maps Docker-internal addresses to host-accessible addresses for the Dialer.
	addrMap map[string]string
}

// newRedisSentinelCluster creates a Redis Sentinel cluster for testing.
// Returns nil and an error if Docker is unavailable.
func newRedisSentinelCluster(ctx context.Context) (_ *redisSentinelCluster, retErr error) {
	c := &redisSentinelCluster{addrMap: make(map[string]string)}
	defer func() {
		if retErr != nil {
			_ = c.close(ctx)
		}
	}()

	// Create Docker network.
	n, err := tcnetwork.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create docker network: %w", err)
	}
	c.net = n
	netName := n.Name

	// Start primary.
	c.primary, err = startRedisNode(ctx, netName, "redis-primary", nil)
	if err != nil {
		return nil, fmt.Errorf("start primary: %w", err)
	}
	if err := configureACL(ctx, c.primary); err != nil {
		return nil, fmt.Errorf("configure ACL on primary: %w", err)
	}

	primaryIP, err := c.primary.ContainerIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("get primary IP: %w", err)
	}
	primaryPort, err := c.primary.MappedPort(ctx, "6379/tcp")
	if err != nil {
		return nil, fmt.Errorf("get primary mapped port: %w", err)
	}
	c.addrMap[primaryIP+":6379"] = "localhost:" + primaryPort.Port()

	// Start replicas.
	for i := range c.replicas {
		alias := fmt.Sprintf("redis-replica-%d", i)
		c.replicas[i], err = startRedisNode(ctx, netName, alias, []string{primaryIP, "6379"})
		if err != nil {
			return nil, fmt.Errorf("start replica %d: %w", i, err)
		}
		if err := configureACL(ctx, c.replicas[i]); err != nil {
			return nil, fmt.Errorf("configure ACL on replica %d: %w", i, err)
		}
		ip, err := c.replicas[i].ContainerIP(ctx)
		if err != nil {
			return nil, fmt.Errorf("get replica %d IP: %w", i, err)
		}
		port, err := c.replicas[i].MappedPort(ctx, "6379/tcp")
		if err != nil {
			return nil, fmt.Errorf("get replica %d mapped port: %w", i, err)
		}
		c.addrMap[ip+":6379"] = "localhost:" + port.Port()
	}

	// Generate sentinel config.
	sentConf := fmt.Sprintf(
		"port 26379\nsentinel monitor %s %s 6379 2\nsentinel down-after-milliseconds %s 5000\nsentinel failover-timeout %s 10000\nsentinel parallel-syncs %s 1\n",
		testMasterName, primaryIP, testMasterName, testMasterName, testMasterName,
	)

	// Start sentinels.
	for i := range c.sents {
		c.sents[i], err = startSentinel(ctx, netName, sentConf)
		if err != nil {
			return nil, fmt.Errorf("start sentinel %d: %w", i, err)
		}
		sentPort, err := c.sents[i].MappedPort(ctx, "26379/tcp")
		if err != nil {
			return nil, fmt.Errorf("get sentinel %d mapped port: %w", i, err)
		}
		c.SentinelAddrs = append(c.SentinelAddrs, "localhost:"+sentPort.Port())
	}

	// Wait for sentinels to discover the master.
	if err := c.waitForSentinelReady(ctx); err != nil {
		return nil, fmt.Errorf("sentinel readiness: %w", err)
	}

	return c, nil
}

func startRedisNode(ctx context.Context, networkName, alias string, replicaOf []string) (testcontainers.Container, error) {
	cmd := []string{"redis-server", "--protected-mode", "no", "--port", "6379"}
	if len(replicaOf) == 2 {
		cmd = append(cmd, "--replicaof", replicaOf[0], replicaOf[1])
	}
	req := testcontainers.ContainerRequest{
		Image:        testRedisImage,
		ExposedPorts: []string{"6379/tcp"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {alias},
		},
		Cmd:        cmd,
		WaitingFor: wait.ForLog("Ready to accept connections").WithStartupTimeout(30 * time.Second),
	}
	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
}

func startSentinel(ctx context.Context, networkName, config string) (testcontainers.Container, error) {
	req := testcontainers.ContainerRequest{
		Image:        testRedisImage,
		ExposedPorts: []string{"26379/tcp"},
		Networks:     []string{networkName},
		Cmd:          []string{"redis-sentinel", "/data/sentinel.conf"},
		Files: []testcontainers.ContainerFile{
			{
				Reader:            strings.NewReader(config),
				ContainerFilePath: "/data/sentinel.conf",
				FileMode:          0o664,
			},
		},
		WaitingFor: wait.ForLog("+monitor master").WithStartupTimeout(30 * time.Second),
	}
	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
}

func configureACL(ctx context.Context, container testcontainers.Container) error {
	exitCode, reader, err := container.Exec(ctx, []string{
		"redis-cli", "ACL", "SETUSER", testACLUser, "on",
		">" + testACLPass, "~thv:*", "&*", "+@all",
	})
	if reader != nil {
		_, _ = io.ReadAll(reader)
	}
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("ACL SETUSER exited with code %d", exitCode)
	}
	return nil
}

func (c *redisSentinelCluster) waitForSentinelReady(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for i, addr := range c.SentinelAddrs {
		if err := waitForSentinel(ctx, addr, deadline); err != nil {
			return fmt.Errorf("sentinel %d (%s): %w", i, addr, err)
		}
	}
	return nil
}

func waitForSentinel(ctx context.Context, addr string, deadline time.Time) error {
	sentClient := redis.NewSentinelClient(&redis.Options{Addr: addr})
	defer sentClient.Close()

	for time.Now().Before(deadline) {
		master, err := sentClient.GetMasterAddrByName(ctx, testMasterName).Result()
		if err == nil && len(master) == 2 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("did not discover master %q within deadline", testMasterName)
}

func (c *redisSentinelCluster) close(ctx context.Context) error {
	var errs []error
	for i := range c.sents {
		if c.sents[i] != nil {
			errs = append(errs, c.sents[i].Terminate(ctx))
		}
	}
	for i := range c.replicas {
		if c.replicas[i] != nil {
			errs = append(errs, c.replicas[i].Terminate(ctx))
		}
	}
	if c.primary != nil {
		errs = append(errs, c.primary.Terminate(ctx))
	}
	if c.net != nil {
		errs = append(errs, c.net.Remove(ctx))
	}
	return errors.Join(errs...)
}

// newTestClient creates a go-redis failover client with address translation.
// The custom Dialer translates Docker-internal addresses to host-mapped ports.
func (c *redisSentinelCluster) newTestClient() redis.UniversalClient {
	return redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    testMasterName,
		SentinelAddrs: c.SentinelAddrs,
		Username:      testACLUser,
		Password:      testACLPass,
		DB:            0,
		DialTimeout:   5 * time.Second,
		ReadTimeout:   3 * time.Second,
		WriteTimeout:  3 * time.Second,
		Dialer: func(_ context.Context, network, addr string) (net.Conn, error) {
			if mapped, ok := c.addrMap[addr]; ok {
				addr = mapped
			}
			return net.DialTimeout(network, addr, 5*time.Second)
		},
	})
}

// triggerFailover forces a Sentinel failover for testing.
func (c *redisSentinelCluster) triggerFailover(ctx context.Context) error {
	sentClient := redis.NewSentinelClient(&redis.Options{Addr: c.SentinelAddrs[0]})
	defer sentClient.Close()
	return sentClient.Failover(ctx, testMasterName).Err()
}

// getMasterAddr returns the current master address as reported by Sentinel.
func (c *redisSentinelCluster) getMasterAddr(ctx context.Context) (string, error) {
	sentClient := redis.NewSentinelClient(&redis.Options{Addr: c.SentinelAddrs[0]})
	defer sentClient.Close()
	master, err := sentClient.GetMasterAddrByName(ctx, testMasterName).Result()
	if err != nil {
		return "", err
	}
	return master[0] + ":" + master[1], nil
}

// waitForFailover polls Sentinel until it reports a different master than originalAddr.
// This replaces a fixed sleep with an adaptive wait that completes as soon as failover finishes.
func (c *redisSentinelCluster) waitForFailover(ctx context.Context, originalAddr string) error {
	sentClient := redis.NewSentinelClient(&redis.Options{Addr: c.SentinelAddrs[0]})
	defer sentClient.Close()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		master, err := sentClient.GetMasterAddrByName(ctx, testMasterName).Result()
		if err == nil && len(master) == 2 {
			currentAddr := master[0] + ":" + master[1]
			if currentAddr != originalAddr {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("failover did not complete within deadline: master still at %s", originalAddr)
}

// --- Package-level Setup ---

var testCluster *redisSentinelCluster

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	var err error
	testCluster, err = newRedisSentinelCluster(ctx)
	cancel()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set up Redis Sentinel cluster: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if testCluster != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = testCluster.close(cleanupCtx)
		cleanupCancel()
	}

	os.Exit(code)
}

// --- Test Helpers ---

func withIntegrationStorage(t *testing.T, fn func(context.Context, *RedisStorage)) {
	t.Helper()
	if testCluster == nil {
		t.Skip("Redis Sentinel cluster not available")
	}
	t.Parallel()

	client := testCluster.newTestClient()
	prefix := DeriveKeyPrefix("inttest", sanitizeTestName(t.Name()))
	storage := NewRedisStorageWithClient(client, prefix)
	t.Cleanup(func() { _ = storage.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	fn(ctx, storage)
}

func sanitizeTestName(name string) string {
	return strings.NewReplacer("/", "-", " ", "_").Replace(name)
}

// --- Storage Interface: Client Operations ---

func TestIntegration_ClientOperations(t *testing.T) {
	t.Parallel()

	t.Run("register and retrieve", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := &mockClient{id: "int-client", scopes: []string{"openid", "profile"}}
			require.NoError(t, s.RegisterClient(ctx, client))

			retrieved, err := s.GetClient(ctx, "int-client")
			require.NoError(t, err)
			assert.Equal(t, "int-client", retrieved.GetID())
			assert.Equal(t, client.GetScopes(), retrieved.GetScopes())
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			_, err := s.GetClient(ctx, "no-such-client")
			requireRedisNotFoundError(t, err)
		})
	})
}

func TestIntegration_ClientAssertionJWT(t *testing.T) {
	t.Parallel()

	t.Run("unknown JTI is valid", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.ClientAssertionJWTValid(ctx, "unknown-jti"))
		})
	})

	t.Run("known JTI is invalid", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.SetClientAssertionJWT(ctx, "int-jti", time.Now().Add(time.Hour)))
			assert.ErrorIs(t, s.ClientAssertionJWTValid(ctx, "int-jti"), fosite.ErrJTIKnown)
		})
	})

	t.Run("expired JTI not stored", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.SetClientAssertionJWT(ctx, "exp-jti", time.Now().Add(-time.Hour)))
			require.NoError(t, s.ClientAssertionJWTValid(ctx, "exp-jti"))
		})
	})
}

// --- Storage Interface: Authorization Code ---

func TestIntegration_AuthorizeCodeFlow(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-ac-1", client)
			require.NoError(t, s.CreateAuthorizeCodeSession(ctx, "code-int-1", request))

			retrieved, err := s.GetAuthorizeCodeSession(ctx, "code-int-1", nil)
			require.NoError(t, err)
			assert.Equal(t, "req-ac-1", retrieved.GetID())
		})
	})

	t.Run("invalidate code", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-ac-inv", client)
			require.NoError(t, s.CreateAuthorizeCodeSession(ctx, "code-inv", request))
			require.NoError(t, s.InvalidateAuthorizeCodeSession(ctx, "code-inv"))

			retrieved, err := s.GetAuthorizeCodeSession(ctx, "code-inv", nil)
			assert.ErrorIs(t, err, fosite.ErrInvalidatedAuthorizeCode)
			assert.NotNil(t, retrieved, "must return request with invalidated error")
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			_, err := s.GetAuthorizeCodeSession(ctx, "no-such-code", nil)
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Storage Interface: Access Tokens ---

func TestIntegration_AccessTokenLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("create get delete", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-at-1", client)
			require.NoError(t, s.CreateAccessTokenSession(ctx, "at-sig-1", request))

			retrieved, err := s.GetAccessTokenSession(ctx, "at-sig-1", nil)
			require.NoError(t, err)
			assert.Equal(t, "req-at-1", retrieved.GetID())

			require.NoError(t, s.DeleteAccessTokenSession(ctx, "at-sig-1"))

			_, err = s.GetAccessTokenSession(ctx, "at-sig-1", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			_, err := s.GetAccessTokenSession(ctx, "no-such-at", nil)
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Storage Interface: Refresh Tokens ---

func TestIntegration_RefreshTokenLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("create get delete", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-rt-1", client)
			require.NoError(t, s.CreateRefreshTokenSession(ctx, "rt-sig-1", "at-sig-1", request))

			retrieved, err := s.GetRefreshTokenSession(ctx, "rt-sig-1", nil)
			require.NoError(t, err)
			assert.Equal(t, "req-rt-1", retrieved.GetID())

			require.NoError(t, s.DeleteRefreshTokenSession(ctx, "rt-sig-1"))

			_, err = s.GetRefreshTokenSession(ctx, "rt-sig-1", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("rotation deletes refresh and access tokens", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-rotate", client)
			require.NoError(t, s.CreateRefreshTokenSession(ctx, "rt-rotate", "at-rotate", request))
			require.NoError(t, s.CreateAccessTokenSession(ctx, "at-rotate", request))

			require.NoError(t, s.RotateRefreshToken(ctx, "req-rotate", "rt-rotate"))

			_, err := s.GetRefreshTokenSession(ctx, "rt-rotate", nil)
			requireRedisNotFoundError(t, err)
			_, err = s.GetAccessTokenSession(ctx, "at-rotate", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("rotate non-existent is no-op", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.RotateRefreshToken(ctx, "no-req", "no-sig"))
		})
	})
}

// --- Storage Interface: Token Revocation ---

func TestIntegration_TokenRevocation(t *testing.T) {
	t.Parallel()

	t.Run("revoke access tokens by request ID", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-revoke-at", client)
			require.NoError(t, s.CreateAccessTokenSession(ctx, "at-rev-1", request))
			require.NoError(t, s.CreateAccessTokenSession(ctx, "at-rev-2", request))

			require.NoError(t, s.RevokeAccessToken(ctx, "req-revoke-at"))

			_, err := s.GetAccessTokenSession(ctx, "at-rev-1", nil)
			requireRedisNotFoundError(t, err)
			_, err = s.GetAccessTokenSession(ctx, "at-rev-2", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("revoke refresh tokens by request ID", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-revoke-rt", client)
			require.NoError(t, s.CreateRefreshTokenSession(ctx, "rt-rev-1", "at-rev-1", request))

			require.NoError(t, s.RevokeRefreshToken(ctx, "req-revoke-rt"))

			_, err := s.GetRefreshTokenSession(ctx, "rt-rev-1", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("revoke refresh tokens with grace period", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-revoke-gp", client)
			require.NoError(t, s.CreateRefreshTokenSession(ctx, "rt-gp-1", "at-gp-1", request))

			require.NoError(t, s.RevokeRefreshTokenMaybeGracePeriod(ctx, "req-revoke-gp", "rt-gp-1"))

			_, err := s.GetRefreshTokenSession(ctx, "rt-gp-1", nil)
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Storage Interface: PKCE ---

func TestIntegration_PKCEFlow(t *testing.T) {
	t.Parallel()

	t.Run("create get delete", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-pkce-1", client)
			require.NoError(t, s.CreatePKCERequestSession(ctx, "pkce-sig-1", request))

			retrieved, err := s.GetPKCERequestSession(ctx, "pkce-sig-1", nil)
			require.NoError(t, err)
			assert.Equal(t, "req-pkce-1", retrieved.GetID())

			require.NoError(t, s.DeletePKCERequestSession(ctx, "pkce-sig-1"))

			_, err = s.GetPKCERequestSession(ctx, "pkce-sig-1", nil)
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Storage Interface: Upstream Tokens ---

func TestIntegration_UpstreamTokens(t *testing.T) {
	t.Parallel()

	t.Run("store and get", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			tokens := &UpstreamTokens{
				ProviderID:      "google",
				AccessToken:     "upstream-access",
				RefreshToken:    "upstream-refresh",
				IDToken:         "upstream-id",
				ExpiresAt:       time.Now().Add(time.Hour),
				UserID:          "user-up-1",
				UpstreamSubject: "google-sub",
				ClientID:        "client-up-1",
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-up-1", tokens))

			retrieved, err := s.GetUpstreamTokens(ctx, "sess-up-1")
			require.NoError(t, err)
			assert.Equal(t, "upstream-access", retrieved.AccessToken)
			assert.Equal(t, "user-up-1", retrieved.UserID)
			assert.Equal(t, "google-sub", retrieved.UpstreamSubject)
		})
	})

	t.Run("nil tokens stored and retrieved", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-nil", nil))
			retrieved, err := s.GetUpstreamTokens(ctx, "sess-nil")
			require.NoError(t, err)
			assert.Nil(t, retrieved)
		})
	})

	t.Run("overwrite tokens", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-ow", &UpstreamTokens{
				AccessToken: "old", UserID: "user1", ExpiresAt: time.Now().Add(time.Hour),
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-ow", &UpstreamTokens{
				AccessToken: "new", UserID: "user2", ExpiresAt: time.Now().Add(time.Hour),
			}))
			retrieved, err := s.GetUpstreamTokens(ctx, "sess-ow")
			require.NoError(t, err)
			assert.Equal(t, "new", retrieved.AccessToken)
			assert.Equal(t, "user2", retrieved.UserID)
		})
	})

	t.Run("expired tokens return ErrExpired", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-exp", &UpstreamTokens{
				AccessToken: "expired", ExpiresAt: time.Now().Add(-time.Hour),
			}))
			_, err := s.GetUpstreamTokens(ctx, "sess-exp")
			assert.ErrorIs(t, err, ErrExpired)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-del", &UpstreamTokens{
				AccessToken: "del-me", ExpiresAt: time.Now().Add(time.Hour),
			}))
			require.NoError(t, s.DeleteUpstreamTokens(ctx, "sess-del"))
			_, err := s.GetUpstreamTokens(ctx, "sess-del")
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Storage Interface: Pending Authorization ---

func TestIntegration_PendingAuthorization(t *testing.T) {
	t.Parallel()

	makePending := func(state string) *PendingAuthorization {
		return &PendingAuthorization{
			ClientID: "pa-client", RedirectURI: "https://example.com/callback",
			State: "client-state", PKCEChallenge: "challenge", PKCEMethod: "S256",
			Scopes: []string{"openid"}, InternalState: state,
			UpstreamPKCEVerifier: "verifier", UpstreamNonce: "nonce", CreatedAt: time.Now(),
		}
	}

	t.Run("store load delete", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			pending := makePending("int-state-1")
			require.NoError(t, s.StorePendingAuthorization(ctx, "int-state-1", pending))

			retrieved, err := s.LoadPendingAuthorization(ctx, "int-state-1")
			require.NoError(t, err)
			assert.Equal(t, "pa-client", retrieved.ClientID)
			assert.Equal(t, "challenge", retrieved.PKCEChallenge)

			require.NoError(t, s.DeletePendingAuthorization(ctx, "int-state-1"))

			_, err = s.LoadPendingAuthorization(ctx, "int-state-1")
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Storage Interface: User Management ---

func TestIntegration_UserManagement(t *testing.T) {
	t.Parallel()

	t.Run("create get delete", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			now := time.Now()
			user := &User{ID: "user-int-1", CreatedAt: now, UpdatedAt: now}
			require.NoError(t, s.CreateUser(ctx, user))

			retrieved, err := s.GetUser(ctx, "user-int-1")
			require.NoError(t, err)
			assert.Equal(t, "user-int-1", retrieved.ID)

			require.NoError(t, s.DeleteUser(ctx, "user-int-1"))

			_, err = s.GetUser(ctx, "user-int-1")
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("duplicate creation fails", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			now := time.Now()
			user := &User{ID: "user-dup", CreatedAt: now, UpdatedAt: now}
			require.NoError(t, s.CreateUser(ctx, user))
			assert.ErrorIs(t, s.CreateUser(ctx, user), ErrAlreadyExists)
		})
	})
}

// --- Storage Interface: Provider Identity ---

func TestIntegration_ProviderIdentity(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "pi-user-1", CreatedAt: now, UpdatedAt: now}))

			identity := &ProviderIdentity{
				UserID: "pi-user-1", ProviderID: "google",
				ProviderSubject: "google-sub-1", LinkedAt: now, LastUsedAt: now,
			}
			require.NoError(t, s.CreateProviderIdentity(ctx, identity))

			retrieved, err := s.GetProviderIdentity(ctx, "google", "google-sub-1")
			require.NoError(t, err)
			assert.Equal(t, "pi-user-1", retrieved.UserID)
			assert.Equal(t, "google", retrieved.ProviderID)
		})
	})

	t.Run("list user identities", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "pi-user-multi", CreatedAt: now, UpdatedAt: now}))

			require.NoError(t, s.CreateProviderIdentity(ctx, &ProviderIdentity{
				UserID: "pi-user-multi", ProviderID: "google", ProviderSubject: "g-sub", LinkedAt: now,
			}))
			require.NoError(t, s.CreateProviderIdentity(ctx, &ProviderIdentity{
				UserID: "pi-user-multi", ProviderID: "github", ProviderSubject: "gh-sub", LinkedAt: now,
			}))

			identities, err := s.GetUserProviderIdentities(ctx, "pi-user-multi")
			require.NoError(t, err)
			assert.Len(t, identities, 2)

			providers := map[string]bool{}
			for _, id := range identities {
				providers[id.ProviderID] = true
			}
			assert.True(t, providers["google"])
			assert.True(t, providers["github"])
		})
	})

	t.Run("update last used", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "pi-user-upd", CreatedAt: now, UpdatedAt: now}))
			require.NoError(t, s.CreateProviderIdentity(ctx, &ProviderIdentity{
				UserID: "pi-user-upd", ProviderID: "google", ProviderSubject: "g-upd", LinkedAt: now,
			}))

			newTime := now.Add(time.Hour)
			require.NoError(t, s.UpdateProviderIdentityLastUsed(ctx, "google", "g-upd", newTime))

			retrieved, err := s.GetProviderIdentity(ctx, "google", "g-upd")
			require.NoError(t, err)
			assert.WithinDuration(t, newTime, retrieved.LastUsedAt, time.Second)
		})
	})

	t.Run("delete user cascades identities and tokens", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "cascade-user", CreatedAt: now, UpdatedAt: now}))
			require.NoError(t, s.CreateProviderIdentity(ctx, &ProviderIdentity{
				UserID: "cascade-user", ProviderID: "google", ProviderSubject: "cascade-sub", LinkedAt: now,
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "cascade-sess", &UpstreamTokens{
				ProviderID: "google", AccessToken: "cascade-token",
				UserID: "cascade-user", ExpiresAt: now.Add(time.Hour),
			}))

			require.NoError(t, s.DeleteUser(ctx, "cascade-user"))

			_, err := s.GetUser(ctx, "cascade-user")
			assert.ErrorIs(t, err, ErrNotFound)
			_, err = s.GetProviderIdentity(ctx, "google", "cascade-sub")
			assert.ErrorIs(t, err, ErrNotFound)
			_, err = s.GetUpstreamTokens(ctx, "cascade-sess")
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})
}

// --- Session Round-Trip ---

func TestIntegration_SessionRoundTrip(t *testing.T) {
	withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
		client := testClient()
		require.NoError(t, s.RegisterClient(ctx, client))

		sess := session.New("user-rt", "upstream-sess-rt", "test-client-id")
		request := &fosite.Request{
			ID:             "req-rt-jwt",
			RequestedAt:    time.Now(),
			Client:         client,
			RequestedScope: fosite.Arguments{"openid"},
			GrantedScope:   fosite.Arguments{"openid"},
			Form:           make(url.Values),
			Session:        sess,
		}

		require.NoError(t, s.CreateAccessTokenSession(ctx, "rt-jwt-sig", request))

		retrieved, err := s.GetAccessTokenSession(ctx, "rt-jwt-sig", nil)
		require.NoError(t, err)

		upstreamSess, ok := retrieved.GetSession().(session.UpstreamSession)
		require.True(t, ok, "session must implement UpstreamSession")

		claims := upstreamSess.GetJWTClaims().ToMapClaims()
		assert.Equal(t, "user-rt", claims["sub"])
		assert.Equal(t, "upstream-sess-rt", claims["tsid"])
		assert.Equal(t, "upstream-sess-rt", upstreamSess.GetIDPSessionID())
	})
}

// --- Health Check ---

func TestIntegration_Health(t *testing.T) {
	withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
		require.NoError(t, s.Health(ctx))
	})
}

// --- Sentinel-Specific Tests ---
//
// Note: Quorum-based failover detection is configured (quorum=2 of 3 sentinels)
// but not explicitly tested. A quorum test would require stopping individual
// sentinel containers to verify that failover succeeds with 2/3 sentinels and
// fails with only 1/3. This is deferred as a future enhancement.

func TestIntegration_SentinelConnection(t *testing.T) {
	if testCluster == nil {
		t.Skip("Redis Sentinel cluster not available")
	}
	t.Parallel()

	// Verify connection through Sentinel works end-to-end.
	client := testCluster.newTestClient()
	defer client.Close()

	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err(), "should connect to Redis via Sentinel")

	// Verify we can write and read data through the Sentinel-routed connection.
	key := "thv:auth:sentinel-test:ping"
	require.NoError(t, client.Set(ctx, key, "pong", time.Minute).Err())
	val, err := client.Get(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, "pong", val)
}

func TestIntegration_SentinelFailover(t *testing.T) {
	if testCluster == nil {
		t.Skip("Redis Sentinel cluster not available")
	}

	ctx := context.Background()
	client := testCluster.newTestClient()
	defer client.Close()

	// Write data before failover.
	key := "thv:auth:failover-test:data"
	require.NoError(t, client.Set(ctx, key, "pre-failover", 5*time.Minute).Err())

	// Wait for replication to propagate to at least one replica.
	// WAIT blocks until the write is acknowledged by N replicas or timeout (ms).
	result, err := client.Do(ctx, "WAIT", 1, 5000).Int64()
	require.NoError(t, err)
	require.GreaterOrEqual(t, result, int64(1), "at least one replica should acknowledge the write")

	// Capture original master address before triggering failover.
	originalAddr, err := testCluster.getMasterAddr(ctx)
	require.NoError(t, err, "should get current master address from sentinel")

	// Trigger failover.
	require.NoError(t, testCluster.triggerFailover(ctx))

	// Poll Sentinel until it reports a different master, adapting to actual failover duration.
	require.NoError(t, testCluster.waitForFailover(ctx, originalAddr), "failover should complete")

	// Verify data is still accessible after failover.
	// The failover client should automatically reconnect to the new master.
	var val string
	for i := 0; i < 20; i++ {
		val, err = client.Get(ctx, key).Result()
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	require.NoError(t, err, "data should be accessible after failover")
	assert.Equal(t, "pre-failover", val)

	// Verify we can write new data after failover.
	for i := 0; i < 10; i++ {
		err = client.Set(ctx, key, "post-failover", 5*time.Minute).Err()
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	require.NoError(t, err, "should write after failover")
	val, err = client.Get(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, "post-failover", val)
}

// --- ACL Authentication Tests ---

func TestIntegration_ACLValidCredentials(t *testing.T) {
	if testCluster == nil {
		t.Skip("Redis Sentinel cluster not available")
	}
	t.Parallel()

	// The standard test client uses ACL credentials — verify operations succeed.
	client := testCluster.newTestClient()
	defer client.Close()

	ctx := context.Background()
	key := "thv:auth:acl-test:valid"
	require.NoError(t, client.Set(ctx, key, "ok", time.Minute).Err())
	val, err := client.Get(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, "ok", val)
}

func TestIntegration_ACLInvalidCredentials(t *testing.T) {
	if testCluster == nil {
		t.Skip("Redis Sentinel cluster not available")
	}
	t.Parallel()

	t.Run("wrong username", func(t *testing.T) {
		t.Parallel()
		client := redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    testMasterName,
			SentinelAddrs: testCluster.SentinelAddrs,
			Username:      "wrong-user",
			Password:      testACLPass,
			DialTimeout:   3 * time.Second,
			Dialer: func(_ context.Context, network, addr string) (net.Conn, error) {
				if mapped, ok := testCluster.addrMap[addr]; ok {
					addr = mapped
				}
				return net.DialTimeout(network, addr, 3*time.Second)
			},
		})
		defer client.Close()

		err := client.Ping(context.Background()).Err()
		require.Error(t, err, "connection with wrong username should fail")
	})

	t.Run("wrong password", func(t *testing.T) {
		t.Parallel()
		client := redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    testMasterName,
			SentinelAddrs: testCluster.SentinelAddrs,
			Username:      testACLUser,
			Password:      "wrong-password",
			DialTimeout:   3 * time.Second,
			Dialer: func(_ context.Context, network, addr string) (net.Conn, error) {
				if mapped, ok := testCluster.addrMap[addr]; ok {
					addr = mapped
				}
				return net.DialTimeout(network, addr, 3*time.Second)
			},
		})
		defer client.Close()

		err := client.Ping(context.Background()).Err()
		require.Error(t, err, "connection with wrong password should fail")
	})
}

func TestIntegration_ACLKeyPatternRestriction(t *testing.T) {
	if testCluster == nil {
		t.Skip("Redis Sentinel cluster not available")
	}
	t.Parallel()

	client := testCluster.newTestClient()
	defer client.Close()
	ctx := context.Background()

	// Operations on thv:* keys should succeed.
	require.NoError(t, client.Set(ctx, "thv:auth:acl:allowed", "yes", time.Minute).Err())

	// Operations outside thv:* should fail.
	err := client.Set(ctx, "forbidden:key", "no", time.Minute).Err()
	require.Error(t, err, "writing to non-thv: key should be denied by ACL")
}

// --- TTL Expiration Tests (Real Redis) ---

func TestIntegration_RealTTLExpiration(t *testing.T) {
	t.Parallel()

	t.Run("access token expires via Redis TTL", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			// Create token with 2-second TTL.
			request := newRedisTestRequesterWithExpiration(
				"req-ttl-at", client, fosite.AccessToken, time.Now().Add(2*time.Second),
			)
			require.NoError(t, s.CreateAccessTokenSession(ctx, "ttl-at-sig", request))

			// Should exist immediately.
			_, err := s.GetAccessTokenSession(ctx, "ttl-at-sig", nil)
			require.NoError(t, err)

			// Wait for expiration.
			time.Sleep(3 * time.Second)

			// Should be gone.
			_, err = s.GetAccessTokenSession(ctx, "ttl-at-sig", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("JTI expires via Redis TTL", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			require.NoError(t, s.SetClientAssertionJWT(ctx, "ttl-jti", time.Now().Add(2*time.Second)))
			assert.ErrorIs(t, s.ClientAssertionJWTValid(ctx, "ttl-jti"), fosite.ErrJTIKnown)

			time.Sleep(3 * time.Second)

			require.NoError(t, s.ClientAssertionJWTValid(ctx, "ttl-jti"))
		})
	})

	t.Run("TTL matches session expiration", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			expiry := time.Now().Add(30 * time.Second)
			request := newRedisTestRequesterWithExpiration("req-ttl-check", client, fosite.AccessToken, expiry)
			require.NoError(t, s.CreateAccessTokenSession(ctx, "ttl-check-sig", request))

			// Verify the Redis TTL on the key matches the session expiration.
			key := redisKey(s.keyPrefix, KeyTypeAccess, "ttl-check-sig")
			ttl := s.client.TTL(ctx, key).Val()
			assert.InDelta(t, 30, ttl.Seconds(), 5, "Redis TTL should be close to session expiry")
		})
	})
}

// --- Concurrent Access Tests (Real Redis) ---

func TestIntegration_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	t.Run("concurrent writes to different keys", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					request := newRedisTestRequester(fmt.Sprintf("conc-req-%d", idx), client)
					_ = s.CreateAccessTokenSession(ctx, fmt.Sprintf("conc-at-%d", idx), request)
				}(i)
			}
			wg.Wait()

			// Verify all tokens exist.
			for i := 0; i < 50; i++ {
				_, err := s.GetAccessTokenSession(ctx, fmt.Sprintf("conc-at-%d", i), nil)
				require.NoError(t, err, "token %d should exist", i)
			}
		})
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			// Preload data.
			for i := 0; i < 10; i++ {
				request := newRedisTestRequester(fmt.Sprintf("pre-%d", i), client)
				require.NoError(t, s.CreateAccessTokenSession(ctx, fmt.Sprintf("pre-%d", i), request))
			}

			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(2)
				go func(idx int) {
					defer wg.Done()
					request := newRedisTestRequester(fmt.Sprintf("rw-req-%d", idx), client)
					_ = s.CreateAccessTokenSession(ctx, fmt.Sprintf("rw-at-%d", idx), request)
				}(i)
				go func(idx int) {
					defer wg.Done()
					_, _ = s.GetAccessTokenSession(ctx, fmt.Sprintf("pre-%d", idx%10), nil)
				}(i)
			}
			wg.Wait()
		})
	})

	t.Run("concurrent client registration and lookup", func(t *testing.T) {
		withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
			numClients := 25

			var wg sync.WaitGroup
			for i := 0; i < numClients; i++ {
				wg.Add(2)
				go func(idx int) {
					defer wg.Done()
					_ = s.RegisterClient(ctx, &mockClient{id: fmt.Sprintf("conc-cl-%d", idx)})
				}(i)
				go func(idx int) {
					defer wg.Done()
					_, _ = s.GetClient(ctx, fmt.Sprintf("conc-cl-%d", idx))
				}(i)
			}
			wg.Wait()

			// Verify all clients exist.
			for i := 0; i < numClients; i++ {
				cl, err := s.GetClient(ctx, fmt.Sprintf("conc-cl-%d", i))
				require.NoError(t, err, "client %d should exist", i)
				assert.Equal(t, fmt.Sprintf("conc-cl-%d", i), cl.GetID())
			}
		})
	})
}

// --- Unicode and Edge Case Tests ---

func TestIntegration_UnicodeInIdentifiers(t *testing.T) {
	withIntegrationStorage(t, func(ctx context.Context, s *RedisStorage) {
		now := time.Now()

		// Create user with Unicode ID.
		userID := "user-日本語-émojis-🎉"
		require.NoError(t, s.CreateUser(ctx, &User{ID: userID, CreatedAt: now, UpdatedAt: now}))

		retrieved, err := s.GetUser(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, userID, retrieved.ID)

		// Create provider identity with Unicode subject.
		require.NoError(t, s.CreateProviderIdentity(ctx, &ProviderIdentity{
			UserID: userID, ProviderID: "keycloak",
			ProviderSubject: "sub-données-中文", LinkedAt: now,
		}))

		pi, err := s.GetProviderIdentity(ctx, "keycloak", "sub-données-中文")
		require.NoError(t, err)
		assert.Equal(t, userID, pi.UserID)
		assert.Equal(t, "sub-données-中文", pi.ProviderSubject)
	})
}
