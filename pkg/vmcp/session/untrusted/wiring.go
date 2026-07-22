// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/metric"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Stack bundles the untrusted-mode runtime components wired at the vMCP
// composition root (session manager / CLI): the address resolver installed on
// the session factory, the pod lifecycle (DeletePod on session Terminate), and
// the reaper owning pod GC.
type Stack struct {
	Resolver  BackendAddressResolver
	Lifecycle PodLifecycle
	Reaper    *Reaper
}

// WiringConfig carries the fully-resolved untrusted-mode parameters.
// RedisClient must be the same client (and KeyPrefix the same per-tenant
// ':'-terminated prefix) as the session metadata store — untrusted pod
// admission reuses the session-storage Redis.
type WiringConfig struct {
	// K8sClient is vMCP's in-cluster controller-runtime client. Required.
	K8sClient client.Client
	// RedisClient backs admission counters and pod leases. Required; Redis
	// unavailability fails admission closed.
	RedisClient redis.UniversalClient
	// KeyPrefix is the session-storage key prefix (e.g. "thv:vmcp:session:").
	// Required, must end with ':'.
	KeyPrefix string
	// Namespace is vMCP's own namespace. Required.
	Namespace string
	// VMCPUId identifies this vMCP instance for heartbeats/zombie detection.
	// Required.
	VMCPUId string
	// Admission tunes the DoS controls.
	Admission AdmissionConfig
	// Reaper tunes the GC loop.
	Reaper ReaperConfig
	// ReadyBudget bounds per-pod cold start. Default DefaultReadyBudget.
	ReadyBudget time.Duration
	// AdoptTTL is the bounded lease written when adopting a pre-existing pod.
	// Default 2 minutes.
	AdoptTTL time.Duration
	// TokenStore, when non-nil, wires the egress-broker sidecar's auth-server
	// token-store coordinates into every provisioned pod. Nil leaves the broker
	// without a token store (it fails closed at startup).
	TokenStore *TokenStoreConfig
	// Images overrides the egress data-plane images (supply-chain pinning,
	// Wave-5). Nil = the pinned defaults in egress.go.
	Images *SidecarImages
	// SidecarResources overrides the envoy/broker sidecar resource
	// multipliers. Nil = defaults. Values must be positive (fail loudly).
	SidecarResources *SidecarResourceOverride
	// MeterProvider, when non-nil, registers the untrusted-mode OTel instruments
	// (untrusted_backend_pods gauge, untrusted_pod_admissions_total counter).
	// Nil disables metrics.
	MeterProvider metric.MeterProvider
}

// defaultAdoptTTL bounds the lease written on pod adoption.
const defaultAdoptTTL = 2 * time.Minute

// NewK8sPodLifecycleFromParts builds a PodLifecycle directly from the raw
// client/prefix parts, bypassing admission and the reaper. Exported for
// envtest/integration tests that exercise the lifecycle in isolation;
// production wiring goes through NewStack. perUserQuota is the lifecycle's
// authoritative per-user cap (same value the admission gate pre-flights).
func NewK8sPodLifecycleFromParts(
	k8sClient client.Client,
	redisClient redis.UniversalClient,
	keyPrefix, namespace, vmcpUID string,
	idleTTL, adoptTTL time.Duration,
	perUserQuota int,
) (PodLifecycle, error) {
	store, err := newRedisStore(redisClient, keyPrefix, namespace)
	if err != nil {
		return nil, err
	}
	return NewK8sPodLifecycle(k8sClient, store, vmcpUID, idleTTL, adoptTTL, perUserQuota)
}

// NewStack wires the full untrusted-mode stack. The reaper's session-liveness
// probe is NOT taken here: it comes from the session manager (the owner of
// session storage), which does not exist until the server is built — the
// composition root hands it to Reaper.Run.
func NewStack(cfg WiringConfig) (*Stack, error) {
	store, err := newRedisStore(cfg.RedisClient, cfg.KeyPrefix, cfg.Namespace)
	if err != nil {
		return nil, err
	}
	if cfg.K8sClient == nil {
		return nil, fmt.Errorf("untrusted wiring: K8sClient must not be nil")
	}
	if cfg.VMCPUId == "" {
		return nil, fmt.Errorf("untrusted wiring: VMCPUId must not be empty")
	}
	adoptTTL := cfg.AdoptTTL
	if adoptTTL <= 0 {
		adoptTTL = defaultAdoptTTL
	}
	idleTTL := cfg.Reaper.resolved().IdleTTL

	if cfg.TokenStore != nil {
		if err := cfg.TokenStore.validate(); err != nil {
			return nil, err
		}
	}
	if cfg.SidecarResources != nil &&
		(cfg.SidecarResources.CPUMultiplier <= 0 || cfg.SidecarResources.MemoryMultiplier <= 0) {
		return nil, fmt.Errorf("untrusted wiring: sidecar resource multipliers must be positive")
	}
	var metrics *untrustedMetrics
	if cfg.MeterProvider != nil {
		metrics, err = newUntrustedMetrics(cfg.MeterProvider)
		if err != nil {
			return nil, err
		}
	}
	adm, err := newAdmission(store, cfg.Admission, time.Now)
	if err != nil {
		return nil, err
	}
	// The lifecycle enforces the same per-user quota the admission gate
	// pre-flights; resolved() has already applied the default.
	quota := adm.cfg.PerUserPodQuota
	lifecycle, err := NewK8sPodLifecycle(cfg.K8sClient, store, cfg.VMCPUId, idleTTL, adoptTTL, quota)
	if err != nil {
		return nil, err
	}
	resolver := podAddressResolverWithTokenStore(
		lifecycle, adm, cfg.ReadyBudget, cfg.TokenStore, metrics, cfg.Images, cfg.SidecarResources)
	reaper, err := NewReaper(cfg.K8sClient, store, cfg.Reaper, cfg.VMCPUId, metrics)
	if err != nil {
		return nil, err
	}
	return &Stack{Resolver: resolver, Lifecycle: lifecycle, Reaper: reaper}, nil
}
