// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReaperConfig tunes the reaper goroutine. Zero values fall back to the
// documented defaults; the constructor validates bounds.
type ReaperConfig struct {
	// TickInterval is the sweep cadence. Default 60s.
	TickInterval time.Duration
	// IdleTTL is the pod liveness lease enforced by the idle rule.
	// Default 30m.
	IdleTTL time.Duration
	// HeartbeatInterval is how often this vMCP writes its liveness key.
	// Default 30s.
	HeartbeatInterval time.Duration
	// HeartbeatTTL is the expiry of the liveness key. Default 5m.
	HeartbeatTTL time.Duration
	// ReadinessTimeout is the failed-cold-start threshold: a pod older than
	// this and not Ready is deleted. Default 120s (DefaultReadyBudget).
	ReadinessTimeout time.Duration
}

const (
	defaultTickInterval      = 60 * time.Second
	defaultIdleTTL           = 30 * time.Minute
	defaultHeartbeatInterval = 30 * time.Second
	defaultHeartbeatTTL      = 5 * time.Minute
)

func (c ReaperConfig) resolved() ReaperConfig {
	if c.TickInterval <= 0 {
		c.TickInterval = defaultTickInterval
	}
	if c.IdleTTL <= 0 {
		c.IdleTTL = defaultIdleTTL
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = defaultHeartbeatInterval
	}
	if c.HeartbeatTTL <= 0 {
		c.HeartbeatTTL = defaultHeartbeatTTL
	}
	if c.ReadinessTimeout <= 0 {
		c.ReadinessTimeout = defaultReadinessTimeout
	}
	return c
}

// Reaper is the single owner of untrusted pod garbage collection. One
// goroutine per vMCP pod; every replica runs the same loop against Redis plus
// the pod list, so teardown survives any single vMCP crash (overlapping
// deletes are harmless — Delete is idempotent, NotFound ignored).
//
// Rules enforced each tick:
//   - readiness timeout: pod older than ReadinessTimeout (default 120s) and
//     not Ready → delete (failed cold start);
//   - idle TTL: podttl lease absent → delete (session gone);
//   - zombie: pod's vMCP heartbeat is absent and has been for the grace
//     period → delete (owning vMCP died, no replica adopted the pod);
//   - registry reconciliation: admission sets/counters rebuilt from the
//     authoritative pod LIST (self-heals drift from crashed creates/deletes);
//   - refresh: pods whose session metadata key still exists get their podttl
//     lease renewed (sliding window).
//
// Redis down at tick → the tick is skipped entirely (fail safe: no deletions
// without Redis evidence).
type Reaper struct {
	k8s     client.Client
	store   *redisStore
	cfg     ReaperConfig
	vmcpUID string
	ns      string
	now     func() time.Time

	// metrics, when non-nil, records the untrusted_backend_pods gauge from the
	// authoritative pod LIST each tick.
	metrics *untrustedMetrics

	mu      sync.Mutex
	zombies map[string]time.Time // pod name -> when its heartbeat absence was first observed
}

// NewReaper constructs the reaper. metrics may be nil (metrics disabled).
func NewReaper(
	k8sClient client.Client,
	store *redisStore,
	cfg ReaperConfig,
	vmcpUID string,
	metrics *untrustedMetrics,
) (*Reaper, error) {
	return &Reaper{
		k8s:     k8sClient,
		store:   store,
		cfg:     cfg.resolved(),
		vmcpUID: vmcpUID,
		ns:      store.namespace,
		now:     time.Now,
		metrics: metrics,
		zombies: make(map[string]time.Time),
	}, nil
}

// Run starts the heartbeat and sweep loops until ctx is cancelled. It returns
// when the context is done; both tickers are stopped. sessionExists reports
// whether a vMCP session's metadata key is alive in shared storage (supplied
// by the session manager, which owns the session store — the reaper never
// re-derives the session key shape); it must fail false on storage errors and
// must not be nil (fail loudly at startup, not on the first sweep).
func (r *Reaper) Run(ctx context.Context, sessionExists func(ctx context.Context, sessionID string) bool) {
	if sessionExists == nil {
		slog.Error("untrusted reaper: sessionExists probe is nil; refusing to run")
		return
	}
	heartbeat := time.NewTicker(r.cfg.HeartbeatInterval)
	sweep := time.NewTicker(r.cfg.TickInterval)
	defer heartbeat.Stop()
	defer sweep.Stop()

	r.writeHeartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			r.writeHeartbeat(ctx)
		case <-sweep.C:
			r.tick(ctx, sessionExists)
		}
	}
}

func (r *Reaper) writeHeartbeat(_ context.Context) {
	// Heartbeats must outlive request-scope cancellation: a cancelled sweep
	// context must not stop this vMCP from proving liveness, so the write uses
	// its own bounded background context.
	hbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.store.writeHeartbeat(hbCtx, r.vmcpUID, r.cfg.HeartbeatTTL); err != nil {
		slog.Warn("untrusted reaper: failed to write vMCP heartbeat", "error", err)
	}
}

// tick performs one sweep. Any Redis failure aborts the tick without any
// deletion (fail safe). sessionExists is the refresh-rule probe (see Run).
func (r *Reaper) tick(ctx context.Context, sessionExists func(ctx context.Context, sessionID string) bool) {
	tickCtx, cancel := context.WithTimeout(ctx, r.cfg.TickInterval/2)
	defer cancel()

	pods := &corev1.PodList{}
	if err := r.k8s.List(tickCtx, pods,
		client.InNamespace(r.ns),
		client.MatchingLabels{LabelUntrusted: "true"},
	); err != nil {
		slog.Warn("untrusted reaper: pod list failed; skipping tick", "error", err)
		return
	}

	live := make(map[string]*corev1.Pod, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		live[p.Name] = p
		if err := r.evaluatePod(tickCtx, p, sessionExists); err != nil {
			// Fail safe: Redis evidence is unavailable, so make no further
			// deletion decisions this tick.
			slog.Warn("untrusted reaper: Redis unavailable; skipping tick", "pod", p.Name, "error", err)
			return
		}
	}

	if err := r.reconcile(tickCtx, live); err != nil {
		slog.Warn("untrusted reaper: registry reconciliation failed; will retry next tick", "error", err)
	}
}

// evaluatePod applies the delete/refresh rules to one pod. A returned error
// means Redis evidence could not be read — the caller must abort the tick.
// Deletion failures are logged and swallowed (retried next tick).
// sessionExists is the refresh-rule probe (see Run).
func (r *Reaper) evaluatePod(
	ctx context.Context, pod *corev1.Pod, sessionExists func(ctx context.Context, sessionID string) bool,
) error {
	// Rule 1: readiness timeout (failed cold start). Pure-K8s evidence; no
	// Redis read needed.
	age := r.now().Sub(pod.CreationTimestamp.Time)
	if age > r.cfg.ReadinessTimeout && !isPodReady(pod) {
		r.delete(ctx, pod, "readiness timeout")
		return nil
	}

	// Rule 2: idle TTL. The podttl lease is the liveness contract.
	leaseAlive, err := r.store.podLeaseExists(ctx, pod.Name)
	if err != nil {
		return fmt.Errorf("failed to read pod lease: %w", err)
	}
	if !leaseAlive {
		r.delete(ctx, pod, "idle TTL expired")
		return nil
	}

	// Rule 3: zombie (owning vMCP dead, no replica adopted the pod). Two
	// consecutive observations grace-period apart, so a single missed
	// heartbeat window cannot reap a live vMCP's pods.
	ownerUID := pod.Annotations[AnnotationVMCPUid]
	if ownerUID != "" && ownerUID != r.vmcpUID {
		alive, err := r.store.heartbeatExists(ctx, ownerUID)
		if err != nil {
			return fmt.Errorf("failed to read vMCP heartbeat: %w", err)
		}
		if !alive {
			firstSeen := r.markZombie(pod.Name)
			if r.now().Sub(firstSeen) >= zombieHeartbeatGrace {
				r.delete(ctx, pod, "owning vMCP heartbeat gone (zombie)")
				return nil
			}
		} else {
			r.clearZombie(pod.Name)
		}
	} else {
		r.clearZombie(pod.Name)
	}

	// Rule 5 (refresh): renew the lease while the owning session is alive.
	sessionID := pod.Annotations[AnnotationSessionID]
	if sessionID != "" && sessionExists(ctx, sessionID) {
		if err := r.store.touchPodLease(ctx, pod.Name, r.cfg.IdleTTL); err != nil {
			return fmt.Errorf("failed to refresh pod lease: %w", err)
		}
	}
	return nil
}

// reconcile rebuilds admission state from the authoritative pod list and
// prunes orphaned quota counters.
func (r *Reaper) reconcile(ctx context.Context, live map[string]*corev1.Pod) error {
	names := make([]string, 0, len(live))
	userCounts := make(map[string]int)
	serverUIDs := make(map[string]struct{})
	serverCounts := make(map[string]int)
	for _, p := range live {
		names = append(names, p.Name)
		if h := p.Labels[LabelUntrustedUser]; h != "" {
			userCounts[h]++
		}
		if uid := p.Labels[LabelMCPServerUID]; uid != "" {
			serverUIDs[uid] = struct{}{}
			serverCounts[uid]++
		}
	}

	// Refresh the per-server pod gauge from the authoritative list. The label
	// is the MCPServer name (low-cardinality, one per untrusted CR); pods
	// missing the name annotation are grouped under "unknown" rather than
	// dropped so the gauge accounts for every live pod.
	if r.metrics != nil {
		nameCounts := make(map[string]int)
		for _, p := range live {
			name := p.Annotations[AnnotationMCPServerName]
			if name == "" {
				name = "unknown"
			}
			nameCounts[name]++
		}
		for name, count := range nameCounts {
			r.metrics.recordPodCounts(ctx, name, count)
		}
	}

	// Per-user live counts for the quota-counter correction. Pairs are parsed
	// positionally inside the script (from the end), so no separator can
	// collide with a hash.
	joined := "|"
	for h, count := range userCounts {
		joined += fmt.Sprintf("%s=%d|", h, count)
	}

	// Rebuild the global set and every per-server set present in the
	// authoritative list. Per-server sets for servers with zero live pods
	// empty out naturally via DEL.
	for uid := range serverUIDs {
		members := make([]any, 0, len(names))
		for _, p := range live {
			if p.Labels[LabelMCPServerUID] == uid {
				members = append(members, p.Name)
			}
		}
		if err := rebuildServerSetScript.Run(ctx, r.store.client,
			[]string{r.store.serverPodsSetKey(uid)}, members...).Err(); err != nil {
			return fmt.Errorf("failed to rebuild per-server pod set: %w", err)
		}
	}

	// Rebuild the global set and correct the per-user quota counters.
	args := make([]any, 0, len(names)+1)
	args = append(args, joined)
	for _, n := range names {
		args = append(args, n)
	}
	if err := reconcileScript.Run(ctx, r.store.client, []string{
		r.store.podsSetKey(),
		r.store.quotaKeysRegistryKey(),
	}, args...).Err(); err != nil {
		return fmt.Errorf("failed to rebuild pod registry: %w", err)
	}

	return nil
}

func (r *Reaper) delete(ctx context.Context, pod *corev1.Pod, reason string) {
	if err := r.k8s.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		slog.Warn("untrusted reaper: pod delete failed; will retry next tick",
			"pod", pod.Name, "reason", reason, "error", err)
		return
	}
	if err := r.store.recordPodDelete(ctx, pod.Name, pod.Labels); err != nil {
		slog.Warn("untrusted reaper: bookkeeping cleanup failed; reconciliation will heal",
			"pod", pod.Name, "error", err)
	}
	slog.Info("untrusted reaper: pod deleted", "pod", pod.Name, "reason", reason)
	r.clearZombie(pod.Name)
}

func (r *Reaper) markZombie(podName string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.zombies[podName]; ok {
		return t
	}
	r.zombies[podName] = r.now()
	return r.zombies[podName]
}

func (r *Reaper) clearZombie(podName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.zombies, podName)
}
