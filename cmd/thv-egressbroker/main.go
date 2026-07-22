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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/tokenenc"
	"github.com/stacklok/toolhive/pkg/egressbroker"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
)

// Environment contract for the token store (injected at clone time by the
// Wave-3 operator wiring; the KEK comes from a sidecar-only Secret env).
const (
	// envRedisAddr is the session/upstream-token Redis address (host:port).
	envRedisAddr = "THV_EGRESSBROKER_REDIS_ADDR"
	// envRedisKeyPrefix is the auth-server per-tenant key prefix the upstream
	// token rows live under (e.g. "thv:auth:{ns}:{name}:").
	envRedisKeyPrefix = "THV_EGRESSBROKER_REDIS_KEY_PREFIX"
	// envKEKActiveID carries the key ID the auth server encrypts NEW writes
	// under (THV_EGRESSBROKER_KEK_ID). It must match the operator's
	// activeKeyId — the clone wiring passes it through verbatim; a drift here
	// would deny every injection.
	envKEKActiveID = "THV_EGRESSBROKER_KEK_ID"
	// envKEKPrefix is the per-key-ID env prefix: THV_EGRESSBROKER_KEK_<ID>
	// holds the base64 32-byte KEK for that ID (active + retired, so key
	// rotation never orphans rows sealed under a retired ID). When no
	// THV_EGRESSBROKER_KEK_* env is present, token rows are read unencrypted
	// (legacy plaintext storage); encrypted rows then fail closed (Open
	// rejects non-legacy values).
	envKEKPrefix = "THV_EGRESSBROKER_KEK_"
	// envEnvoyBootstrapOut is the path the broker renders the Envoy bootstrap
	// into (a shared emptyDir the Envoy container mounts read-only). When
	// unset, bootstrap rendering is skipped (the Envoy config is managed
	// externally).
	envEnvoyBootstrapOut = "THV_EGRESSBROKER_ENVOY_BOOTSTRAP_OUT"
	// healthPingTimeout bounds the Redis reachability check per probe.
	healthPingTimeout = 2 * time.Second
	// scanEvictionSweep is the background TTL sweep cadence for the D6c
	// scan-correlation map.
	scanEvictionSweep = 30 * time.Second
)

func main() {
	// Seed mode: the untrusted pod's ca-seed init container runs the broker
	// binary with `seed-ca` because the production image is distroless (no
	// shell) — a plain `cp` in an init container is impossible without it.
	// Copies the operator's CA bundle into the shared emptyDir and exits.
	if len(os.Args) > 1 && os.Args[1] == "seed-ca" {
		if err := seedCA(os.Args[2:]); err != nil {
			slog.Error("seed-ca failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		// Startup failures are loud and carry no credential material.
		slog.Error("egressbroker failed to start", "error", err)
		os.Exit(1)
	}
}

// seedCA copies <src> to <dst> read-only (0444). Usage: seed-ca <src> <dst>.
func seedCA(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("seed-ca requires <src> <dst>, got %d args", len(args))
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}
	// #nosec G306 -- the CA public cert is not secret; 0444 is the intended mode
	if err := os.WriteFile(args[1], data, 0o444); err != nil {
		return fmt.Errorf("write CA: %w", err)
	}
	return nil
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
	// The dial allowlist is enforced at the Envoy data plane (D7), not by the
	// broker's own dials. Parse it here only to fail fast on a corrupt policy
	// before the pod goes further.
	if _, err := egressbroker.ParseIPAllowlist(dialCIDRs); err != nil {
		return err
	}
	if err := renderEnvoyBootstrap(cfg, policy); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start the health listener BEFORE the blocking dependency init so /livez
	// answers immediately (liveness = process alive). /healthz (readiness)
	// flips to 200 only once policyLoaded is set and Redis pings. A wedged
	// listener wedges the pod — the untrusted reaper deletes it once it is
	// older than the readiness timeout — so log a listener failure at Error.
	var policyLoaded atomic.Bool
	health := &healthServer{ca: ca, policyLoaded: &policyLoaded, ping: func(context.Context) error {
		// Before dependencies load, readiness reports not-ready without
		// touching Redis (liveness is the separate, always-200 /livez).
		return errDependenciesNotReady
	}}
	healthSrv := newHealthServerHandler(health)
	go func() {
		if err := runHealthServer(ctx, healthSrv); err != nil {
			slog.Error("egressbroker: health listener stopped; probes will now fail", "error", err)
		}
	}()

	tokens, err := buildTokenReader(ctx)
	if err != nil {
		return err
	}

	server, err := buildGRPCServer(ctx, cfg, resolver, policy, ca, tokens)
	if err != nil {
		return err
	}

	// Dependencies are up: readiness may now answer 200 once Redis pings.
	health.ping = tokens.ping
	policyLoaded.Store(true)

	return server.Run(ctx)
}

// buildGRPCServer wires the broker's gRPC surface: the D6c response-scan
// correlation map shared by ext_authz (record at injection) and ext_proc
// (scan on response), the D11 audit/metrics sinks, the injector, and the
// three Envoy services (ext_authz, SDS, ext_proc) on one loopback socket.
func buildGRPCServer(
	ctx context.Context,
	cfg *egressbroker.Config,
	resolver *egressbroker.PodIdentityResolver,
	policy *egressbroker.EgressPolicy,
	ca *egressbroker.BumpCA,
	tokens *tokenStore,
) (*egressbroker.Server, error) {
	scanMap, err := egressbroker.NewTokenMap(egressbroker.ScanCorrelationTTL, egressbroker.ScanCorrelationMaxEntries)
	if err != nil {
		return nil, err
	}
	go scanMap.RunEvictionLoop(ctx, scanEvictionSweep)
	auditLog := egressbroker.NewAuditLogger()
	brokerMetrics, err := egressbroker.NewBrokerMetrics(otel.GetMeterProvider())
	if err != nil {
		return nil, err
	}
	podName := resolver.PodName(os.Getenv)

	injector, err := egressbroker.NewCredentialInjector(resolver.PodIdentity(), policy, tokens)
	if err != nil {
		return nil, err
	}
	injector.WithScanCorrelation(scanMap).WithObservability(brokerMetrics, auditLog, podName)
	authz, err := egressbroker.NewAuthorizationServer(injector)
	if err != nil {
		return nil, err
	}
	authz.WithObservability(auditLog, brokerMetrics, resolver.PodIdentity(), podName)
	sds, err := egressbroker.NewSecretDiscoveryServer(ca, policy)
	if err != nil {
		return nil, err
	}
	extproc, err := egressbroker.NewExternalProcessorServer(
		scanMap,
		egressbroker.ScannerBounds{MaxBodyBytes: cfg.ScanMaxBodyBytes},
		!cfg.ScanFailClosed, // failOpen: the documented default (D6c), inverted config knob
		resolver.PodIdentity(),
		brokerMetrics,
		auditLog,
	)
	if err != nil {
		return nil, err
	}
	return egressbroker.NewServer(cfg.ListenAddress, cfg.ListenPort, authz, sds, extproc)
}

// redisPinger abstracts the Redis reachability check for tests.
type redisPinger func(ctx context.Context) error

// healthServer is the /healthz handler. 200 iff the bump CA is not past
// rotation-due AND the policy is loaded AND Redis answers PING within
// healthPingTimeout; otherwise 503 with a coarse reason (never credential
// material).
var errDependenciesNotReady = fmt.Errorf("dependencies not ready")

type healthServer struct {
	ca           *egressbroker.BumpCA
	policyLoaded *atomic.Bool
	ping         redisPinger
}

// newHealthServer builds the loopback-only HTTP server bound to the health
// port. The handler is exported through the returned server's Handler for
// tests.
func newHealthServerHandler(h *healthServer) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handle)
	// /livez is dependency-free: 200 whenever the process is alive. Served
	// from process start so the liveness probe never kills a broker that is
	// still initialising (slow Redis/CA). Readiness stays on /healthz.
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return &http.Server{
		// Bind all interfaces, not 127.0.0.1: kubelet httpGet probes are
		// delivered to the pod IP (from the node network), not loopback, so a
		// loopback-only bind makes every probe fail and the container is killed
		// at the liveness threshold before it can initialize. The port is not
		// otherwise reachable: the pod's NetworkPolicy confines ingress, and
		// /healthz discloses only coarse subsystem names (no addresses/dates).
		Addr:              fmt.Sprintf(":%d", untrusted.BrokerHealthPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// runHealthServer serves until ctx is cancelled, then shuts down gracefully.
func runHealthServer(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("egressbroker: health server shutdown failed: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (h *healthServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.ca.NeedsRotation(time.Now()) {
		http.Error(w, "bump CA past rotation-due", http.StatusServiceUnavailable)
		return
	}
	if !h.policyLoaded.Load() {
		http.Error(w, "policy not loaded", http.StatusServiceUnavailable)
		return
	}
	pingCtx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
	defer cancel()
	if err := h.ping(pingCtx); err != nil {
		http.Error(w, "redis unreachable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// renderEnvoyBootstrap writes the Envoy bootstrap for this policy into the
// shared config emptyDir, when configured. The bootstrap's route table
// carries exactly the policy's host allowlist (D6b); the ext_authz cluster
// points at this process (loopback); no redirect-following filter exists
// (D6a). The ext_proc scanner's failure_mode_allow is the inverse of the
// broker's fail-closed setting (D6c documented fail-open default).
func renderEnvoyBootstrap(cfg *egressbroker.Config, policy *egressbroker.EgressPolicy) error {
	out := strings.TrimSpace(os.Getenv(envEnvoyBootstrapOut))
	if out == "" {
		return nil
	}
	return egressbroker.WriteEnvoyBootstrap(out, egressbroker.EnvoyConfigParams{
		ExtAuthzAddress:  cfg.ListenAddress,
		ExtAuthzPort:     cfg.ListenPort,
		ProxyPort:        egressbroker.DefaultProxyPort,
		AllowedHosts:     policy.HostAllowlist(),
		ScanFailOpen:     !cfg.ScanFailClosed,
		ScanMaxBodyBytes: cfg.ScanMaxBodyBytes,
	})
}

// tokenStore bundles the upstream-token reader with its Redis reachability
// probe (the health endpoint's Redis check).
type tokenStore struct {
	upstreamtoken.TokenReader
	ping redisPinger
}

// buildTokenReader constructs the upstream-token reader against the vMCP's
// session-storage Redis. The Redis address is operator-injected config. The
// dial does NOT pass through the D7 destination allowlist: that allowlist is
// derived from the credential-egress allowedHosts and deliberately excludes
// private IPs, which would brick any in-cluster token store (the common
// case). Instead the broker dials exactly the configured Redis address and
// nothing else — the connection target is fixed by operator config, not by
// anything the untrusted workload can influence, so the SSRF concern the D7
// guard addresses for upstream egress does not apply here. No refresh is
// wired (the broker holds no OAuth client material): expired rows surface on
// the failed list and deny with "re-consent required" (Wave 4 consent UX).
//
// The Redis ACL password arrives via vmcpconfig.RedisPasswordEnvVar,
// rendered at clone time as a SecretKeyRef env (never a literal). It is
// REQUIRED: without it every Redis call fails AUTH and every credential
// lookup denies.
// injection denies — fail loud at startup instead of crash-looping on 403s.
func buildTokenReader(_ context.Context) (*tokenStore, error) {
	addr := strings.TrimSpace(os.Getenv(envRedisAddr))
	keyPrefix := strings.TrimSpace(os.Getenv(envRedisKeyPrefix))
	if addr == "" || keyPrefix == "" {
		return nil, fmt.Errorf("egressbroker: %s and %s must be set (upstream token store)",
			envRedisAddr, envRedisKeyPrefix)
	}
	password := os.Getenv(vmcpconfig.RedisPasswordEnvVar)
	if password == "" {
		return nil, fmt.Errorf("egressbroker: %s must be set (Redis ACL password for the upstream token store; "+
			"the clone wiring renders it from the auth-server storage ACL Secret)",
			vmcpconfig.RedisPasswordEnvVar)
	}

	opts, err := tokenEncOption()
	if err != nil {
		return nil, err
	}

	// The token-store connection is broker infrastructure, not credential
	// egress: it must NOT pass through the D7 destination allowlist (which is
	// derived from allowedHosts and excludes private IPs like the Redis
	// ClusterIP). Guarding it would brick the broker whenever the token store
	// is in-cluster. The D7 guard applies to upstream egress only.
	client := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: password,
	})
	stor := storage.NewRedisStorageWithClient(client, keyPrefix, opts...)
	return &tokenStore{
		TokenReader: upstreamtoken.NewInProcessService(stor, nil),
		ping:        func(ctx context.Context) error { return client.Ping(ctx).Err() },
	}, nil
}

// tokenEncOption builds the token-encryption option from the KEK env set.
// THV_EGRESSBROKER_KEK_<ID> entries supply the keyring (active + retired
// IDs); THV_EGRESSBROKER_KEK_ID names the active one (the auth server's
// activeKeyId, passed through verbatim by the clone wiring). Any malformed
// coordinate — no keys, a missing/unknown active ID, bad base64, a
// wrong-length key — is a startup error (fail closed); an entirely absent KEK
// env set means plaintext legacy rows only.
func tokenEncOption() ([]storage.RedisStorageOption, error) {
	keys := make(map[string][]byte)
	for _, kv := range os.Environ() {
		name, value, found := strings.Cut(kv, "=")
		if !found || !strings.HasPrefix(name, envKEKPrefix) {
			continue
		}
		id := strings.TrimPrefix(name, envKEKPrefix)
		if id == "ID" {
			// THV_EGRESSBROKER_KEK_ID is the active-ID coordinate, not a key.
			continue
		}
		raw := strings.TrimSpace(value)
		if raw == "" {
			return nil, fmt.Errorf("egressbroker: %s%s is empty", envKEKPrefix, id)
		}
		kek, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("egressbroker: %s%s is not valid base64", envKEKPrefix, id)
		}
		keys[id] = kek
	}
	if len(keys) == 0 {
		if activeID := strings.TrimSpace(os.Getenv(envKEKActiveID)); activeID != "" {
			return nil, fmt.Errorf("egressbroker: %s is set but no %s<key-id> key entries exist", envKEKActiveID, envKEKPrefix)
		}
		return nil, nil
	}
	activeID := strings.TrimSpace(os.Getenv(envKEKActiveID))
	keyring, err := tokenenc.NewStaticKeyring(activeID, keys)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: invalid token-encryption keys: %w", err)
	}
	return []storage.RedisStorageOption{storage.WithTokenEncryption(keyring)}, nil
}
