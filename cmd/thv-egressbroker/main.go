// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Command thv-egressbroker is the per-pod credential-broker sidecar for
// untrusted MCP servers (ADR-0001): it serves Envoy ext_authz (destination
// binding + Authorization injection) and SDS (per-SNI bump-cert minting) on
// a loopback-only gRPC socket. It is the only component in the untrusted pod
// that ever holds an upstream credential.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	goredis "github.com/redis/go-redis/v9"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/tokenenc"
	"github.com/stacklok/toolhive/pkg/egressbroker"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// Environment contract for the token store (injected at clone time by the
// Wave-3 operator wiring; the KEK comes from a sidecar-only Secret env).
const (
	// envRedisAddr is the session/upstream-token Redis address (host:port).
	envRedisAddr = "THV_EGRESSBROKER_REDIS_ADDR"
	// envRedisKeyPrefix is the auth-server per-tenant key prefix the upstream
	// token rows live under (e.g. "thv:auth:{ns}:{name}:").
	envRedisKeyPrefix = "THV_EGRESSBROKER_REDIS_KEY_PREFIX"
	// envKEKBase64 is the base64 token-encryption KEK (32 bytes decoded).
	// When unset, token rows are read unencrypted (legacy plaintext storage);
	// encrypted rows then fail closed (Open rejects non-legacy values).
	envKEKBase64 = "THV_EGRESSBROKER_KEK"
	// envEnvoyBootstrapOut is the path the broker renders the Envoy bootstrap
	// into (a shared emptyDir the Envoy container mounts read-only). When
	// unset, bootstrap rendering is skipped (the Envoy config is managed
	// externally).
	envEnvoyBootstrapOut = "THV_EGRESSBROKER_ENVOY_BOOTSTRAP_OUT"
	// defaultKEKID is the key ID for the single static KEK.
	defaultKEKID = "kek-1"
)

func main() {
	if err := run(); err != nil {
		// Startup failures are loud and carry no credential material.
		slog.Error("egressbroker failed to start", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := egressbroker.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	resolver, err := egressbroker.NewPodIdentityResolver(os.Getenv)
	if err != nil {
		return err
	}
	policy, err := cfg.ReadPolicyFile()
	if err != nil {
		return err
	}
	ca, err := cfg.ReadBumpCA()
	if err != nil {
		return err
	}
	dialCIDRs, err := cfg.ResolveDialAllowlist(policy)
	if err != nil {
		return err
	}
	dialGuard, err := egressbroker.ParseIPAllowlist(dialCIDRs)
	if err != nil {
		return err
	}
	if err := renderEnvoyBootstrap(cfg, policy); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tokenReader, err := buildTokenReader(ctx, dialGuard)
	if err != nil {
		return err
	}

	injector, err := egressbroker.NewCredentialInjector(resolver.PodIdentity(), policy, tokenReader)
	if err != nil {
		return err
	}
	authz, err := egressbroker.NewAuthorizationServer(injector)
	if err != nil {
		return err
	}
	sds, err := egressbroker.NewSecretDiscoveryServer(ca, policy)
	if err != nil {
		return err
	}
	server, err := egressbroker.NewServer(cfg.ListenAddress, cfg.ListenPort, authz, sds)
	if err != nil {
		return err
	}
	return server.Run(ctx)
}

// renderEnvoyBootstrap writes the Envoy bootstrap for this policy into the
// shared config emptyDir, when configured. The bootstrap's route table
// carries exactly the policy's host allowlist (D6b); the ext_authz cluster
// points at this process (loopback); no redirect-following filter exists
// (D6a).
func renderEnvoyBootstrap(cfg *egressbroker.Config, policy *egressbroker.EgressPolicy) error {
	out := strings.TrimSpace(os.Getenv(envEnvoyBootstrapOut))
	if out == "" {
		return nil
	}
	return egressbroker.WriteEnvoyBootstrap(out, egressbroker.EnvoyConfigParams{
		ExtAuthzAddress: cfg.ListenAddress,
		ExtAuthzPort:    cfg.ListenPort,
		ProxyPort:       egressbroker.DefaultProxyPort,
		AllowedHosts:    policy.HostAllowlist(),
	})
}

// buildTokenReader constructs the upstream-token reader against the vMCP's
// session-storage Redis. The Redis dials go through the D7 guard: the Redis
// address is operator-injected config, and the guard keeps the broker from
// ever dialing outside the policy's destination set. No refresh is wired (the
// broker holds no OAuth client material): expired rows surface on the failed
// list and deny with "re-consent required" (Wave 4 consent UX).
func buildTokenReader(_ context.Context, dialGuard *egressbroker.IPAllowlist) (upstreamtoken.TokenReader, error) {
	addr := strings.TrimSpace(os.Getenv(envRedisAddr))
	keyPrefix := strings.TrimSpace(os.Getenv(envRedisKeyPrefix))
	if addr == "" || keyPrefix == "" {
		return nil, fmt.Errorf("egressbroker: %s and %s must be set (upstream token store)",
			envRedisAddr, envRedisKeyPrefix)
	}

	opts, err := tokenEncOption()
	if err != nil {
		return nil, err
	}

	client := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: os.Getenv(vmcpconfig.RedisPasswordEnvVar),
		Dialer:   dialGuard.DialContext,
	})
	stor := storage.NewRedisStorageWithClient(client, keyPrefix, opts...)
	return upstreamtoken.NewInProcessService(stor, nil), nil
}

// tokenEncOption builds the token-encryption option from the KEK env. A
// malformed KEK is a startup error (fail closed); an absent KEK means
// plaintext legacy rows only.
func tokenEncOption() ([]storage.RedisStorageOption, error) {
	raw := strings.TrimSpace(os.Getenv(envKEKBase64))
	if raw == "" {
		return nil, nil
	}
	kek, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: %s is not valid base64", envKEKBase64)
	}
	keyring, err := tokenenc.NewStaticKeyring(defaultKEKID, map[string][]byte{defaultKEKID: kek})
	if err != nil {
		return nil, fmt.Errorf("egressbroker: invalid token-encryption key: %w", err)
	}
	return []storage.RedisStorageOption{storage.WithTokenEncryption(keyring)}, nil
}
