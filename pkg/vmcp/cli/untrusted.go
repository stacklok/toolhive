// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tcredis "github.com/stacklok/toolhive-core/redis"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	authstorage "github.com/stacklok/toolhive/pkg/authserver/storage"
	thvk8s "github.com/stacklok/toolhive/pkg/k8s"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
)

// untrustedTokenStoreAddrEnvVar mirrors the operator-side env var injected on
// the vMCP Deployment carrying the auth-server Redis address (see
// cmd/thv-operator/controllers/virtualmcpserver_deployment.go). The two
// packages must not import each other; the contract is pinned by tests.
const untrustedTokenStoreAddrEnvVar = "THV_UNTRUSTED_TOKEN_STORE_REDIS_ADDR" // #nosec G101 -- env var name, not a credential

// untrustedTokenStoreKEK*EnvVars mirror the operator-side env vars carrying
// the token-encryption KEK coordinates (see
// cmd/thv-operator/controllers/virtualmcpserver_deployment.go
// buildUntrustedTokenStoreEnvVars).
const (
	untrustedTokenStoreKEKSecretEnvVar = "THV_UNTRUSTED_TOKEN_STORE_KEK_SECRET" // #nosec G101 -- env var name, not a credential
	untrustedTokenStoreKEKKeyEnvVar    = "THV_UNTRUSTED_TOKEN_STORE_KEK_KEY"    // #nosec G101 -- env var name, not a credential
	untrustedTokenStoreKEKIDsEnvVar    = "THV_UNTRUSTED_TOKEN_STORE_KEK_IDS"    // #nosec G101 -- env var name, not a credential
)

// untrustedTokenStorePassword*EnvVars mirror the operator-side env vars
// carrying the auth-server Redis ACL password Secret COORDINATES (name + key,
// never the value — see buildUntrustedTokenStoreEnvVars). The sidecar reads
// the password itself from THV_SESSION_REDIS_PASSWORD, rendered as a
// SecretKeyRef env at clone time.
const (
	// #nosec G101 -- env var names, not credentials.
	untrustedTokenStorePasswordSecretEnvVar = "THV_UNTRUSTED_TOKEN_STORE_PASSWORD_SECRET"
	untrustedTokenStorePasswordKeyEnvVar    = "THV_UNTRUSTED_TOKEN_STORE_PASSWORD_KEY" // #nosec G101
)

// Platform-operator tunables (Wave-5 spec §3.1/§3.2/§4), resolved ONCE here at
// the composition root — never hot-reloaded. Every one fails startup on an
// unparsable/zero/negative value (fail loud).
const (
	// envUntrustedEnvoyImage overrides the pinned Envoy data-plane image
	// (air-gapped mirrors).
	envUntrustedEnvoyImage = "THV_UNTRUSTED_ENVOY_IMAGE"
	// envUntrustedBrokerImage overrides the pinned broker sidecar image.
	envUntrustedBrokerImage = "THV_UNTRUSTED_BROKER_IMAGE"
	// envUntrustedSidecarCPU scales the envoy/broker sidecar CPU
	// requests/limits (multiplier, 1.0 = defaults).
	envUntrustedSidecarCPU = "THV_UNTRUSTED_SIDECAR_CPU"
	// envUntrustedSidecarMem scales the envoy/broker sidecar memory
	// requests/limits (multiplier, 1.0 = defaults).
	envUntrustedSidecarMem = "THV_UNTRUSTED_SIDECAR_MEM"

	// envUntrustedIdleTTL is the pod liveness lease the reaper enforces.
	// Default 30m.
	envUntrustedIdleTTL = "THV_UNTRUSTED_IDLE_TTL"
	// envUntrustedPerUserQuota caps concurrent untrusted pods per user.
	// Default 10.
	envUntrustedPerUserQuota = "THV_UNTRUSTED_PER_USER_QUOTA"
	// envUntrustedPerServerCap caps concurrent untrusted pods per MCPServer.
	// Default 200.
	envUntrustedPerServerCap = "THV_UNTRUSTED_PER_SERVER_CAP"
	// envUntrustedGlobalCapRatio is the fraction of the session cache
	// capacity bounding total untrusted pods. Default 0.8.
	envUntrustedGlobalCapRatio = "THV_UNTRUSTED_GLOBAL_CAP_RATIO"
	// envUntrustedReadinessTimeout is the failed-cold-start threshold
	// (resolver wait budget AND reaper sweep rule). Default 120s.
	envUntrustedReadinessTimeout = "THV_UNTRUSTED_READINESS_TIMEOUT"
)

// defaultSessionKeyPrefix mirrors the fallback in server.buildSessionDataStorage.
const defaultSessionKeyPrefix = "thv:vmcp:session:"

// untrustedBundle carries the wired untrusted-mode components for the vMCP
// serve path: the resolver installed on the session factory, the pod lifecycle
// (DeletePod on session Terminate), and the reaper owning pod GC, plus the
// Redis client they share (closed on shutdown).
type untrustedBundle struct {
	resolver    untrusted.BackendAddressResolver
	lifecycle   untrusted.PodLifecycle
	reaper      *untrusted.Reaper
	redisClient redis.UniversalClient
}

// groupHasUntrustedBackend reports whether any backend in the discovered group
// is marked untrusted by the K8s workload discoverer
// (pkg/vmcp/workloads/k8s.go). This is the production feature gate: the
// untrusted stack (pod lifecycle + reaper) starts only when the group actually
// contains an untrusted MCPServer, so trusted-only deployments pay nothing.
func groupHasUntrustedBackend(backends []vmcp.Backend) bool {
	for i := range backends {
		if backends[i].Metadata[untrusted.MetadataKeyUntrusted] == "true" {
			return true
		}
	}
	return false
}

// buildUntrustedStack wires the untrusted-mode stack for the vMCP serve path.
//
// It returns (nil, nil) — untrusted mode off — when the mode is disabled for
// this process (untrusted.ModeEnabled, the TOOLHIVE_ENABLE_UNTRUSTED_MODE env
// gate) or when the group contains no untrusted backend (the feature gate).
// When the mode is disabled, untrusted backends are served through the
// trusted multi-tenant path — their untrusted metadata stamp is already
// suppressed at discovery (untrusted.MarkBackend), so groupHasUntrustedBackend
// never fires for them either; the env gate here is defense-in-depth.
// When the gate is on it
// requires Redis-backed session storage (multi-pod admission counters and pod
// leases are Redis state) and a resolvable vMCP namespace (untrusted mode is
// Kubernetes-only); both are hard startup errors rather than silent
// degradation, so a misconfigured untrusted deployment fails loudly.
//
// The Redis client uses the same coordinates as the session store
// (cfg.SessionStorage address/db/prefix + THV_SESSION_REDIS_PASSWORD) so
// admission and the reaper operate on the same keys as session metadata.
//
// The token-store coordinates for the egress-broker sidecar are resolved from
// the vMCP's own identity: the key prefix is derived via
// storage.DeriveKeyPrefix(namespace, vmcpName) (the auth server the vMCP
// embeds owns the token rows) and the Redis address from the operator-injected
// THV_UNTRUSTED_TOKEN_STORE_REDIS_ADDR env var. When the address is absent the
// TokenStore is left nil and the broker fails closed at startup.
//
//nolint:gocyclo // startup wiring with explicit fail-loud gates is clearest linear.
func buildUntrustedStack(
	ctx context.Context,
	cfg *config.Config,
	backends []vmcp.Backend,
	namespace string,
	vmcpName string,
	meterProvider metric.MeterProvider,
) (*untrustedBundle, error) {
	if !untrusted.ModeEnabled() {
		return nil, nil
	}
	if !groupHasUntrustedBackend(backends) {
		return nil, nil
	}

	if cfg.SessionStorage == nil || cfg.SessionStorage.Provider != "redis" {
		return nil, fmt.Errorf(
			"untrusted backend present in group %q but session storage is not Redis-backed; "+
				"untrusted mode requires sessionStorage.provider=redis for cross-replica admission", cfg.Group)
	}
	if namespace == "" || namespace == "local" {
		return nil, fmt.Errorf(
			"untrusted backend present in group %q but the vMCP namespace could not be resolved; "+
				"untrusted mode is Kubernetes-only", cfg.Group)
	}

	keyPrefix := cfg.SessionStorage.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = defaultSessionKeyPrefix
	}

	// Resolve the platform tunables once, before any dependency construction,
	// so a malformed knob fails startup with a clean error (no half-built
	// stack).
	tunables, err := resolveUntrustedTunables()
	if err != nil {
		return nil, err
	}

	redisClient, err := tcredis.NewClient(ctx, &tcredis.Config{
		Addr:     cfg.SessionStorage.Address,
		Password: os.Getenv(config.RedisPasswordEnvVar),
		DB:       int(cfg.SessionStorage.DB),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Redis for untrusted mode: %w", err)
	}

	k8sClient, err := untrustedK8sClient()
	if err != nil {
		_ = redisClient.Close()
		return nil, fmt.Errorf("failed to build K8s client for untrusted mode: %w", err)
	}

	stack, err := untrusted.NewStack(untrusted.WiringConfig{
		K8sClient:   k8sClient,
		RedisClient: redisClient,
		KeyPrefix:   keyPrefix,
		Namespace:   namespace,
		VMCPUId:     untrustedVMCPUId(),
		Admission: untrusted.AdmissionConfig{
			PerUserPodQuota: tunables.perUserQuota,
			PerMCPServerCap: tunables.perServerCap,
			GlobalCapFactor: tunables.globalCapRatio,
			CacheCapacity:   untrustedCacheCapacity(),
		},
		Reaper: untrusted.ReaperConfig{
			IdleTTL:          tunables.idleTTL,
			ReadinessTimeout: tunables.readinessTimeout,
		},
		ReadyBudget:      tunables.readinessTimeout,
		TokenStore:       resolveTokenStoreConfig(namespace, vmcpName),
		Images:           tunables.images,
		SidecarResources: tunables.sidecarResources,
		MeterProvider:    meterProvider,
	})
	if err != nil {
		_ = redisClient.Close()
		return nil, fmt.Errorf("failed to wire untrusted stack: %w", err)
	}

	slog.Info("untrusted backend mode enabled", "group", cfg.Group, "namespace", namespace)
	return &untrustedBundle{
		resolver: stack.Resolver, lifecycle: stack.Lifecycle, reaper: stack.Reaper, redisClient: redisClient,
	}, nil
}

// resolveTokenStoreConfig builds the egress-broker token-store coordinates from
// the vMCP's identity and the operator-injected Redis address. Returns nil
// (broker fails closed) when the address is absent. When the operator wired
// token encryption (THV_UNTRUSTED_TOKEN_STORE_KEK_SECRET/_KEY/_IDS), the KEK
// Secret name, the ACTIVE key ID, and the full key-ID set are carried so every
// cloned sidecar reads the keys from the Secret — never literals — and its
// keyring knows retired IDs too (rotation never orphans ciphertext).
func resolveTokenStoreConfig(namespace, vmcpName string) *untrusted.TokenStoreConfig {
	addr := os.Getenv(untrustedTokenStoreAddrEnvVar)
	if addr == "" {
		slog.Warn("untrusted mode active but the auth-server Redis address is not configured; "+
			"egress-broker sidecars will fail closed (no upstream credential injection)",
			"env_var", untrustedTokenStoreAddrEnvVar)
		return nil
	}
	ts := &untrusted.TokenStoreConfig{
		RedisAddr: addr,
		KeyPrefix: authstorage.DeriveKeyPrefix(namespace, vmcpName),
	}
	// The token-store Redis password: Secret coordinates only (KEK pattern —
	// the value never transits an env literal). Missing coordinates mean the
	// sidecar cannot AUTH and every injection denies: warn loudly so the
	// operator sees the misconfiguration before the first credentialed call.
	passwordSecret := os.Getenv(untrustedTokenStorePasswordSecretEnvVar)
	passwordKey := os.Getenv(untrustedTokenStorePasswordKeyEnvVar)
	if passwordSecret != "" && passwordKey != "" {
		ts.RedisPasswordSecret = passwordSecret
		ts.RedisPasswordKey = passwordKey
	} else {
		slog.Warn("untrusted token-store Redis password coordinates are not configured; "+
			"egress-broker sidecars cannot authenticate against the token store "+
			"(every upstream credential injection will deny)",
			"secret_env_var", untrustedTokenStorePasswordSecretEnvVar,
			"key_env_var", untrustedTokenStorePasswordKeyEnvVar,
			"password_env_var", config.RedisPasswordEnvVar)
	}
	secretName := os.Getenv(untrustedTokenStoreKEKSecretEnvVar)
	activeID := os.Getenv(untrustedTokenStoreKEKKeyEnvVar)
	idsRaw := os.Getenv(untrustedTokenStoreKEKIDsEnvVar)
	// All-or-nothing: partial KEK coordinates (a hand-edited Deployment or an
	// operator/vMCP version skew) leave the sidecar keyring under-specified —
	// warn loudly and render no KEK config (encryption off on the sidecar; the
	// broker fails closed on any encrypted row).
	if secretName == "" && activeID == "" && idsRaw == "" {
		return ts
	}
	ids := splitNonEmpty(idsRaw, ",")
	if secretName == "" || activeID == "" || len(ids) == 0 || !slices.Contains(ids, activeID) {
		slog.Warn("untrusted token-store KEK coordinates are incomplete "+
			"(secret name, active key ID, and the key-ID set are required together); "+
			"egress-broker sidecars run without token decryption",
			"secret_set", secretName != "",
			"active_id_set", activeID != "",
			"ids_set", len(ids) > 0)
		return ts
	}
	ts.KEKSecret = secretName
	ts.KEKActiveID = activeID
	ts.KEKIDs = ids
	return ts
}

// splitNonEmpty splits s on sep and drops empty elements (so a trailing or
// leading separator in the operator-rendered list cannot produce an empty ID).
func splitNonEmpty(s, sep string) []string {
	var out []string
	for _, part := range strings.Split(s, sep) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// untrustedTunables carries the resolved Wave-5 platform knobs into
// buildUntrustedStack. All values come from env vars (see the const block
// above) or their documented defaults.
type untrustedTunables struct {
	images           *untrusted.SidecarImages
	sidecarResources *untrusted.SidecarResourceOverride
	idleTTL          time.Duration
	readinessTimeout time.Duration
	perUserQuota     int
	perServerCap     int
	globalCapRatio   float64
}

// resolveUntrustedTunables reads the THV_UNTRUSTED_* env vars once. Absent
// vars take defaults; an unparsable or non-positive value is a startup-fatal
// error (fail loud — a silently-defaulted quota or timeout is a misconfig the
// operator cannot see).
func resolveUntrustedTunables() (*untrustedTunables, error) {
	t := &untrustedTunables{}

	var err error
	if t.idleTTL, err = parseEnvDuration(envUntrustedIdleTTL, 30*time.Minute); err != nil {
		return nil, err
	}
	if t.readinessTimeout, err = parseEnvDuration(envUntrustedReadinessTimeout, untrusted.DefaultReadyBudget); err != nil {
		return nil, err
	}
	if t.perUserQuota, err = parseEnvPositiveInt(envUntrustedPerUserQuota, 10); err != nil {
		return nil, err
	}
	if t.perServerCap, err = parseEnvPositiveInt(envUntrustedPerServerCap, 200); err != nil {
		return nil, err
	}
	if t.globalCapRatio, err = parseEnvPositiveFloat(envUntrustedGlobalCapRatio, 0.8); err != nil {
		return nil, err
	}

	// Image overrides (supply-chain): absent = the pinned defaults. Overrides
	// that are not digest-pinned are honored (air-gapped mirrors may retag)
	// but warned about: a floating tag can silently re-point the untrusted
	// data plane at a different image on the next pull. A ":latest" tag is
	// rejected outright — it re-points on EVERY pull and bypasses the
	// release-pinned broker binary contract.
	t.images = &untrusted.SidecarImages{
		EnvoyProxy:   os.Getenv(envUntrustedEnvoyImage),
		EgressBroker: os.Getenv(envUntrustedBrokerImage),
	}
	for _, override := range []struct {
		envVar string
		image  string
	}{
		{envUntrustedEnvoyImage, t.images.EnvoyProxy},
		{envUntrustedBrokerImage, t.images.EgressBroker},
	} {
		if override.image == "" {
			continue
		}
		if strings.HasSuffix(override.image, ":latest") {
			return nil, fmt.Errorf("%s value %q must not use the :latest tag; "+
				"pin the untrusted sidecar image by digest (preferred) or an immutable tag",
				override.envVar, override.image)
		}
		if !strings.Contains(override.image, "@sha256:") {
			slog.Warn("untrusted sidecar image override is not digest-pinned; "+
				"a floating tag can silently re-point the untrusted data plane on the next pull",
				"env_var", override.envVar, "image", override.image)
		}
	}

	cpuMult, err := parseEnvMultiplier(envUntrustedSidecarCPU, 1.0)
	if err != nil {
		return nil, err
	}
	memMult, err := parseEnvMultiplier(envUntrustedSidecarMem, 1.0)
	if err != nil {
		return nil, err
	}
	t.sidecarResources = &untrusted.SidecarResourceOverride{
		CPUMultiplier:    cpuMult,
		MemoryMultiplier: memMult,
	}
	return t, nil
}

// parseEnvDuration resolves a duration env var: absent = def, otherwise the
// value must parse to a positive duration.
func parseEnvDuration(envVar string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s value %q must be a positive Go duration (e.g. %q)", envVar, raw, def.String())
	}
	return d, nil
}

// parseEnvPositiveInt resolves an integer env var: absent = def, otherwise
// the value must be a positive integer.
func parseEnvPositiveInt(envVar string, def int) (int, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s value %q must be a positive integer (default %d)", envVar, raw, def)
	}
	return n, nil
}

// parseEnvPositiveFloat resolves a float env var: absent = def, otherwise the
// value must be a finite positive number (NaN/Inf are not config).
func parseEnvPositiveFloat(envVar string, def float64) (float64, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%s value %q must be a finite positive number (default %v)", envVar, raw, def)
	}
	return f, nil
}

// maxSidecarMultiplier bounds the sidecar CPU/memory multipliers: a larger
// factor is a misconfiguration (a stray zero or a units mix-up), never an
// intent — fail loud instead of requesting thousands of CPUs.
const maxSidecarMultiplier = 100.0

// parseEnvMultiplier resolves a resource-multiplier env var: absent = def,
// otherwise the value must be a finite number in (0, maxSidecarMultiplier].
// NaN/Inf and absurd factors are startup-fatal (a silently-clamped multiplier
// is a misconfig the operator cannot see).
func parseEnvMultiplier(envVar string, def float64) (float64, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) || f > maxSidecarMultiplier {
		return 0, fmt.Errorf("%s value %q must be a finite number in (0, %v] (default %v)",
			envVar, raw, maxSidecarMultiplier, def)
	}
	return f, nil
}

// untrustedK8sClient builds the in-cluster controller-runtime client the pod
// lifecycle uses. The scheme needs core pods + apps statefulsets (via
// clientgoscheme) + the MCPServer CRD (for owner references).
func untrustedK8sClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add client-go scheme: %w", err)
	}
	if err := mcpv1beta1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add MCP v1beta1 scheme: %w", err)
	}
	return thvk8s.NewControllerRuntimeClient(scheme)
}

// untrustedVMCPUId returns a stable identity for this vMCP instance used for
// reaper heartbeats and zombie detection. The pod UID (downward API via
// VMCP_POD_UID) is preferred; a random UUID is the local-dev fallback.
func untrustedVMCPUId() string {
	if podUID := os.Getenv("VMCP_POD_UID"); podUID != "" {
		return podUID
	}
	return uuid.NewString()
}

// untrustedCacheCapacity resolves the session cache capacity the admission
// global-cap factor applies to. The vMCP serve path uses the session manager
// default (sessionmanager.defaultCacheCapacity = 1000); there is no config
// knob today, so this mirrors that default.
func untrustedCacheCapacity() int {
	return 1000
}

// runUntrustedReaper starts the reaper goroutine bound to ctx and returns a
// shutdown function that closes the shared Redis client after the reaper's Run
// loop returns (ctx cancellation). sessionExists is the session manager's
// storage-backed liveness probe (the storage seam — this package never
// re-derives the session key shape). The reaper stops itself when ctx is done;
// the returned func only releases the connection.
func runUntrustedReaper(
	ctx context.Context,
	b *untrustedBundle,
	sessionExists func(ctx context.Context, sessionID string) bool,
) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.reaper.Run(ctx, sessionExists)
	}()
	return func() {
		<-done
		if err := b.redisClient.Close(); err != nil {
			slog.Warn("failed to close untrusted-mode Redis client", "error", err)
		}
	}
}
