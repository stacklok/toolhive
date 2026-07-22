// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:generate mockgen -destination=mocks/mock_lifecycle.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/session/untrusted PodLifecycle,BackendAddressResolver

package untrusted

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodLifecycle owns creation, readiness, and deletion of per-session untrusted
// backend pods. Implementations must be idempotent: EnsurePod returns the
// existing pod when called twice with the same request, and DeletePod tolerates
// NotFound.
type PodLifecycle interface {
	// EnsurePod returns the live pod for the request, creating it from the
	// backend StatefulSet's pod template if absent. On a fresh create the
	// admission bookkeeping (sets, quota counter, TTL lease) is recorded and
	// req.OnNewPod is invoked with the deterministic pod name.
	EnsurePod(ctx context.Context, req EnsurePodRequest) (*corev1.Pod, error)
	// WaitReady blocks until the pod has an IP and a Ready condition, or the
	// budget expires. The pod's UID is re-checked before its IP is trusted
	// (guards against IP reuse by an unrelated replacement pod).
	WaitReady(ctx context.Context, pod *corev1.Pod, budget time.Duration) error
	// DeletePod deletes the named pod and its admission bookkeeping. NotFound
	// is not an error.
	DeletePod(ctx context.Context, name string) error
}

// EnsurePodRequest is the identity tuple a pod is provisioned for.
type EnsurePodRequest struct {
	Session       SessionRef
	MCPServerUID  string
	MCPServerName string
	Port          int32
	// OwnerRefUID is the MCPServer CR UID, set as a non-controller
	// ownerReference so session pods cascade-delete with the MCPServer.
	OwnerRefUID types.UID
	// TokenStore, when non-nil, wires the egress-broker sidecar's auth-server
	// token-store coordinates into the cloned pod (Wave-3). Nil leaves the
	// broker without a token store (it fails closed at startup — only valid
	// where the broker is not expected to inject credentials).
	TokenStore *TokenStoreConfig
	// Images overrides the egress data-plane images (Wave-5 supply-chain
	// pinning). Nil = the pinned defaults in egress.go.
	Images *SidecarImages
	// SidecarResources overrides the envoy/broker sidecar resource
	// multipliers (Wave-5). Nil = defaults.
	SidecarResources *SidecarResourceOverride
	// OnNewPod, when non-nil, is invoked exactly once per fresh pod create
	// with the deterministic pod name (used by the resolver to drop stale
	// session hints). Not invoked when an existing pod is reused.
	OnNewPod func(podName string)
}

const (
	// DefaultReadyBudget is the per-pod cold-start hard cap.
	DefaultReadyBudget = 120 * time.Second

	// waitReadyInitialPoll is the first poll interval for WaitReady; it
	// doubles each poll up to waitReadyMaxPoll.
	waitReadyInitialPoll = 500 * time.Millisecond
	waitReadyMaxPoll     = 5 * time.Second

	// defaultReadinessTimeout is the reaper's failed-cold-start threshold
	// (overridable via ReaperConfig.ReadinessTimeout).
	defaultReadinessTimeout = DefaultReadyBudget

	// zombieHeartbeatGrace is how long a missing vMCP heartbeat is tolerated
	// before the zombie rule deletes a pod (2x the 5-minute heartbeat TTL).
	zombieHeartbeatGrace = 10 * time.Minute
)

// k8sPodLifecycle is the production PodLifecycle: bare pods cloned from the
// operator-built backend StatefulSet's pod template, in the vMCP's namespace.
//
// Quota ownership: the lifecycle (not the admission gate) owns the per-user
// quota counters. recordPodCreate INCRs the user's counter atomically with the
// pod registration (Lua), rolls it back if the cap is already reached, and the
// caller compensates by deleting the pod it just created — so the counter can
// never exceed the cap, and no over-quota pod survives. Admission.Check
// remains a read-only pre-flight (avoids a wasted pod create in the common
// case); the lifecycle write is the enforcement point. The reaper recomputes
// the counters from the authoritative pod LIST each tick (self-healing on
// crashed creates/deletes).
type k8sPodLifecycle struct {
	k8s      client.Client
	store    *redisStore
	vmcpUID  string
	idleTTL  time.Duration
	adoptTTL time.Duration
	quota    int // per-user concurrent pod cap enforced inside recordPodCreate

	mu      sync.Mutex
	stsUIDs map[string]types.UID // mcpserver UID -> StatefulSet UID (process-lifetime cache)
}

// NewK8sPodLifecycle creates a PodLifecycle backed by the given
// controller-runtime client and Redis admission store. idleTTL is the pod
// liveness lease the reaper enforces; adoptTTL is the short lease written when
// a pre-existing pod is adopted, bounded so an abandoned adoption lapses
// quickly (the reaper refreshes the full idle TTL while the owning session
// lives). perUserQuota is the same cap admission checks read-only; it must be
// positive (fail loudly on misconfiguration).
func NewK8sPodLifecycle(
	k8sClient client.Client,
	store *redisStore,
	vmcpUID string,
	idleTTL, adoptTTL time.Duration,
	perUserQuota int,
) (PodLifecycle, error) {
	if perUserQuota <= 0 {
		return nil, fmt.Errorf("untrusted pod lifecycle: per-user quota must be positive, got %d", perUserQuota)
	}
	return &k8sPodLifecycle{
		k8s:      k8sClient,
		store:    store,
		vmcpUID:  vmcpUID,
		idleTTL:  idleTTL,
		adoptTTL: adoptTTL,
		quota:    perUserQuota,
		stsUIDs:  make(map[string]types.UID),
	}, nil
}

// EnsurePod implements PodLifecycle.
func (l *k8sPodLifecycle) EnsurePod(ctx context.Context, req EnsurePodRequest) (*corev1.Pod, error) {
	name := PodNameFor(req.MCPServerUID, req.Session.UserKey(), req.Session.SessionID)

	existing, err := l.getVerifiedPod(ctx, name, req)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// Adopting a pre-existing pod: write a bounded lease so the pod is not
		// reaped mid-session even if the session metadata never lands (e.g.
		// EnsurePod succeeds but session creation then fails). The reaper
		// refreshes the full idle TTL while the owning session exists.
		if err := l.store.writePodLease(ctx, existing, l.adoptTTL); err != nil {
			return nil, fmt.Errorf("failed to write pod lease for %q: %w", name, err)
		}
		return existing, nil
	}

	template, appLabel, err := l.resolveTemplate(ctx, req.MCPServerUID, req.Session.Namespace)
	if err != nil {
		return nil, err
	}

	// The operator publishes the current bump-CA generation on the template;
	// the clone mounts exactly that generation's Secret/bundle (consistent
	// cert/key pair mid-rotation).
	req.Session.CAGeneration = template.Annotations[AnnotationCAGeneration]

	pod, err := clonePodFromTemplate(template, appLabel, req, l.vmcpUID)
	if err != nil {
		return nil, err
	}

	if err := l.k8s.Create(ctx, pod); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create untrusted pod %q: %w", name, err)
		}
		// Lost a create race with another vMCP replica: adopt the winner's pod.
		existing, err = l.getVerifiedPod(ctx, name, req)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, fmt.Errorf("untrusted pod %q reported created but not retrievable", name)
		}
		if err := l.store.writePodLease(ctx, existing, l.adoptTTL); err != nil {
			return nil, fmt.Errorf("failed to write pod lease for %q: %w", name, err)
		}
		return existing, nil
	}

	if err := l.store.recordPodCreate(ctx, pod, l.idleTTL, l.quota); err != nil {
		// Compensate: do not leave an untracked pod behind on Redis failure.
		deleteCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if delErr := l.deletePodObject(deleteCtx, name); delErr != nil {
			slog.Warn("untrusted pod bookkeeping failed and compensating delete failed; reaper will collect the pod",
				"pod", name, "error", delErr)
		}
		if errors.Is(err, ErrQuotaExceeded) {
			cancel()
			return nil, err
		}
		cancel()
		return nil, fmt.Errorf("failed to record untrusted pod %q: %w", name, err)
	}

	if req.OnNewPod != nil {
		req.OnNewPod(name)
	}
	return pod, nil
}

// WaitReady implements PodLifecycle. Polls (request-scoped one-shot, not an
// informer) with exponential backoff until podIP is assigned and the pod is
// Ready, hard-capped by budget.
func (l *k8sPodLifecycle) WaitReady(ctx context.Context, pod *corev1.Pod, budget time.Duration) error {
	if pod == nil {
		return fmt.Errorf("WaitReady: pod must not be nil")
	}
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	interval := waitReadyInitialPoll
	for {
		latest := &corev1.Pod{}
		err := l.k8s.Get(waitCtx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, latest)
		switch {
		case apierrors.IsNotFound(err):
			return fmt.Errorf("untrusted pod %q disappeared while waiting for readiness", pod.Name)
		case err != nil:
			if waitCtx.Err() != nil {
				return fmt.Errorf("untrusted pod %q not ready within %s", pod.Name, budget)
			}
			// Transient API error: keep polling until the budget expires.
		default:
			if latest.UID == pod.UID && latest.Status.PodIP != "" && isPodReady(latest) {
				return nil
			}
		}

		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("untrusted pod %q not ready within %s", pod.Name, budget)
		case <-timer.C:
		}
		if interval < waitReadyMaxPoll {
			interval *= 2
			if interval > waitReadyMaxPoll {
				interval = waitReadyMaxPoll
			}
		}
	}
}

// DeletePod implements PodLifecycle.
func (l *k8sPodLifecycle) DeletePod(ctx context.Context, name string) error {
	pod := &corev1.Pod{}
	key := types.NamespacedName{Name: name, Namespace: l.namespace()}
	var labels map[string]string
	if err := l.k8s.Get(ctx, key, pod); err == nil {
		labels = pod.Labels
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get untrusted pod %q for deletion: %w", name, err)
	}

	if err := l.deletePodObject(ctx, name); err != nil {
		return err
	}

	// Bookkeeping is best-effort after the authoritative delete succeeded;
	// the reaper's reconciliation step heals any residual drift.
	if err := l.store.recordPodDelete(ctx, name, labels); err != nil {
		slog.Warn("failed to clean untrusted pod bookkeeping; reaper will reconcile",
			"pod", name, "error", err)
	}
	return nil
}

// namespace returns the namespace this lifecycle operates in. Untrusted mode
// is single-namespace by construction; reaper and DeletePod paths derive it
// from the store's configured namespace.
func (l *k8sPodLifecycle) namespace() string {
	return l.store.namespace
}

// getVerifiedPod fetches the named pod and validates it is a live member of
// this (session, server) tuple: untrusted label set, same mcpserver UID, same
// session ID annotation, and not terminating. Returns (nil, nil) when no
// usable pod exists. Any mismatch is a hard failure — attaching to a pod
// whose annotations do not name this session could cross user bindings.
func (l *k8sPodLifecycle) getVerifiedPod(ctx context.Context, name string, req EnsurePodRequest) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := l.k8s.Get(ctx, types.NamespacedName{Name: name, Namespace: req.Session.Namespace}, pod)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get untrusted pod %q: %w", name, err)
	}
	if pod.Labels[LabelUntrusted] != "true" || pod.Labels[LabelMCPServerUID] != req.MCPServerUID {
		return nil, fmt.Errorf("untrusted pod %q exists with mismatched identity labels; refusing to attach", name)
	}
	if pod.Annotations[AnnotationSessionID] != req.Session.SessionID {
		return nil, fmt.Errorf("untrusted pod %q is bound to a different session; refusing to attach", name)
	}
	if pod.DeletionTimestamp != nil {
		// Terminating pods are not reused; a concurrent create gets
		// AlreadyExists until the apiserver finishes the delete, at which
		// point the next session attempt recreates cleanly.
		return nil, fmt.Errorf("untrusted pod %q is terminating; cannot provision session backend", name)
	}
	return pod, nil
}

// resolveTemplate finds the backend StatefulSet for the MCPServer. The
// StatefulSet UID is cached per MCPServer UID for the process lifetime (the
// cache key changes when the MCPServer is recreated, invalidating the entry);
// the template itself is always re-read so image bumps roll forward naturally.
// The resolved StatefulSet must carry the app label (cloned onto session pods
// for NetworkPolicy selector parity) — a missing label is a hard error, never
// a silent empty string.
func (l *k8sPodLifecycle) resolveTemplate(
	ctx context.Context,
	mcpserverUID string,
	namespace string,
) (*corev1.PodTemplateSpec, string, error) {
	l.mu.Lock()
	cachedUID, cached := l.stsUIDs[mcpserverUID]
	l.mu.Unlock()

	sts := &appsv1.StatefulSet{}
	if cached {
		uidKey := types.NamespacedName{Name: string(cachedUID), Namespace: namespace}
		if err := l.k8s.Get(ctx, uidKey, sts); err == nil {
			return validateTemplateSTS(sts, mcpserverUID)
		}
		// Deleted/renamed underneath us: fall through to the list path.
	}

	list := &appsv1.StatefulSetList{}
	if err := l.k8s.List(ctx, list,
		client.InNamespace(namespace),
		client.MatchingLabels{LabelMCPServerUID: mcpserverUID, "toolhive": "true"},
	); err != nil {
		return nil, "", fmt.Errorf("failed to list backend StatefulSets for untrusted MCPServer: %w", err)
	}
	if len(list.Items) != 1 {
		return nil, "", fmt.Errorf(
			"expected exactly one backend StatefulSet for untrusted MCPServer (uid %s), found %d",
			mcpserverUID, len(list.Items))
	}
	sts = &list.Items[0]

	template, appLabel, err := validateTemplateSTS(sts, mcpserverUID)
	if err != nil {
		return nil, "", err
	}
	l.mu.Lock()
	l.stsUIDs[mcpserverUID] = types.UID(sts.Name)
	l.mu.Unlock()
	return template, appLabel, nil
}

// validateTemplateSTS enforces the resolved-STS invariants before any clone:
// the app label must be present (it is cloned onto session pods so they match
// the shared NetworkPolicy selectors; an empty label would silently strand
// them outside every selector).
func validateTemplateSTS(sts *appsv1.StatefulSet, mcpserverUID string) (*corev1.PodTemplateSpec, string, error) {
	appLabel := sts.Labels[LabelApp]
	if appLabel == "" {
		return nil, "", fmt.Errorf(
			"backend StatefulSet %q for untrusted MCPServer (uid %s) has no %q label; refusing to clone",
			sts.Name, mcpserverUID, LabelApp)
	}
	return &sts.Spec.Template, appLabel, nil
}

// deletePodObject issues the K8s delete only; NotFound is not an error.
func (l *k8sPodLifecycle) deletePodObject(ctx context.Context, name string) error {
	pod := &corev1.Pod{}
	key := types.NamespacedName{Name: name, Namespace: l.namespace()}
	if err := l.k8s.Get(ctx, key, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get untrusted pod %q: %w", name, err)
	}
	if err := l.k8s.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete untrusted pod %q: %w", name, err)
	}
	return nil
}

// isPodReady reports whether the pod has a Ready condition with status True.
func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Redis Lua scripts. All single-key-per-statement, consistent with the
// session-storage posture.
var (
	// recordCreateScript registers a fresh pod atomically: INCR the per-user
	// quota counter (rolled back → return 0 when the cap ARGV[4] is already
	// reached), set its TTL (refreshed each create so the counter outlives a
	// burst of activity), register the quota key in the reaper's GC registry,
	// add global/server set membership, and write the idle TTL lease.
	// ARGV: [1]=idleTTL ms, [2]=pod name, [3]=user hash, [4]=per-user cap.
	recordCreateScript = redis.NewScript(`
		local n = redis.call('INCR', KEYS[1])
		if n > tonumber(ARGV[4]) then
			redis.call('DECR', KEYS[1])
			return 0
		end
		redis.call('PEXPIRE', KEYS[1], ARGV[1])
		redis.call('SADD', KEYS[5], KEYS[1])
		redis.call('SADD', KEYS[2], ARGV[2])
		redis.call('SADD', KEYS[3], ARGV[2])
		redis.call('SET', KEYS[4], ARGV[3], 'PX', ARGV[1])
		return 1
	`)

	// recordDeleteScript releases a pod: DECR the user's quota counter (floor
	// 0; the key disappears at 0 so a missing counter and a zero counter are
	// the same state), removes set membership, and drops the TTL lease.
	recordDeleteScript = redis.NewScript(`
		if redis.call('SISMEMBER', KEYS[2], ARGV[1]) == 1 then
			redis.call('SREM', KEYS[2], ARGV[1])
			redis.call('SREM', KEYS[3], ARGV[1])
			local n = redis.call('DECR', KEYS[1])
			if n <= 0 then
				redis.call('DEL', KEYS[1])
			end
		end
		redis.call('DEL', KEYS[4])
		return 1
	`)

	// writeLeaseScript refreshes (or writes) a pod's TTL lease without
	// touching quota or set membership (used when adopting existing pods).
	writeLeaseScript = redis.NewScript(`
		redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
		redis.call('SADD', KEYS[2], ARGV[3])
		redis.call('SADD', KEYS[3], ARGV[3])
		return 1
	`)

	// reconcileScript rebuilds the global pod set from the authoritative pod
	// list and corrects every registered per-user quota counter to the live
	// count (keys whose user owns no live pod are deleted). ARGV[1] is a
	// '|'-joined list of per-user live counts as "<userHash>=<count>" pairs;
	// ARGV[2..] are live pod names. The script parses pairs positionally
	// (find from the end) so hashes can never collide with the separator.
	reconcileScript = redis.NewScript(`
		redis.call('DEL', KEYS[1])
		if #ARGV > 1 then
			local pods = {}
			for i = 2, #ARGV do pods[#pods + 1] = ARGV[i] end
			redis.call('SADD', KEYS[1], unpack(pods))
		end
		local counts = {}
		for pair in string.gmatch(ARGV[1], '([^|]+)') do
			local pos = string.find(pair, '=[^=]*$')
			if pos then
				counts[string.sub(pair, 1, pos - 1)] = tonumber(string.sub(pair, pos + 1))
			end
		end
		for _, qk in ipairs(redis.call('SMEMBERS', KEYS[2])) do
			local user = string.match(qk, 'userquota:(.+)$')
			if user then
				local live = counts[user]
				if live and live > 0 then
					redis.call('SET', qk, live, 'KEEPTTL')
				else
					redis.call('DEL', qk)
				end
			end
		end
		return 1
	`)

	// rebuildServerSetScript swaps one per-server pod set with the
	// authoritative members from the pod list.
	rebuildServerSetScript = redis.NewScript(`
		redis.call('DEL', KEYS[1])
		if #ARGV > 0 then
			redis.call('SADD', KEYS[1], unpack(ARGV))
		end
		return 1
	`)
)

// redisStore owns all untrusted-mode Redis state: pod TTL leases, admission
// sets, quota counters, and vMCP heartbeats. Redis holds only counters and
// TTLs — the pod→user mapping lives in pod labels/annotations.
type redisStore struct {
	client    redis.UniversalClient
	prefix    string
	namespace string
}

// newRedisStore builds the store. prefix must be the tenant's ':'-terminated
// session-storage prefix (same validation as RedisStorage).
func newRedisStore(redisClient redis.UniversalClient, prefix, namespace string) (*redisStore, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("untrusted redis store: client must not be nil")
	}
	if prefix == "" || !strings.HasSuffix(prefix, ":") {
		return nil, fmt.Errorf("untrusted redis store: key prefix must be non-empty and end with ':'")
	}
	if namespace == "" {
		return nil, fmt.Errorf("untrusted redis store: namespace must not be empty")
	}
	return &redisStore{client: redisClient, prefix: prefix, namespace: namespace}, nil
}

func (s *redisStore) quotaKeyForUserHash(hash string) string {
	return s.prefix + "untrusted:userquota:" + hash
}

// quotaKeysRegistryKey is a Redis set of all per-user quota key names,
// maintained so the reaper can find and garbage-collect orphaned counters
// without a SCAN.
func (s *redisStore) quotaKeysRegistryKey() string {
	return s.prefix + "untrusted:quotakeys"
}

// recordPodCreate atomically registers a freshly created pod and takes one of
// the user's quota slots. When the user's counter is already at perUserQuota
// the script rolls its own INCR back and the call returns ErrQuotaExceeded —
// the caller must compensate by deleting the pod it just created.
func (s *redisStore) recordPodCreate(ctx context.Context, pod *corev1.Pod, idleTTL time.Duration, perUserQuota int) error {
	userKeyHash := pod.Labels[LabelUntrustedUser]
	admitted, err := recordCreateScript.Run(ctx, s.client, []string{
		s.quotaKeyForUserHash(userKeyHash),
		s.podsSetKey(),
		s.serverPodsSetKey(pod.Labels[LabelMCPServerUID]),
		s.podTTLKey(pod.Name),
		s.quotaKeysRegistryKey(),
	}, idleTTL.Milliseconds(), pod.Name, userKeyHash, perUserQuota).Int()
	if err != nil {
		return err
	}
	if admitted == 0 {
		return fmt.Errorf("%w: per-user pod quota (%d) reached", ErrQuotaExceeded, perUserQuota)
	}
	return nil
}

func (s *redisStore) recordPodDelete(ctx context.Context, name string, labels map[string]string) error {
	return recordDeleteScript.Run(ctx, s.client, []string{
		s.quotaKeyForUserHash(labels[LabelUntrustedUser]),
		s.podsSetKey(),
		s.serverPodsSetKey(labels[LabelMCPServerUID]),
		s.podTTLKey(name),
	}, name).Err()
}

func (s *redisStore) writePodLease(ctx context.Context, pod *corev1.Pod, ttl time.Duration) error {
	return writeLeaseScript.Run(ctx, s.client, []string{
		s.podTTLKey(pod.Name),
		s.podsSetKey(),
		s.serverPodsSetKey(pod.Labels[LabelMCPServerUID]),
	}, pod.Labels[LabelUntrustedUser], ttl.Milliseconds(), pod.Name).Err()
}

// touchPodLeaseScript refreshes the idle TTL on an existing lease (sliding
// window). No-op when the lease is absent — the reaper decides creation vs
// deletion.
var touchPodLeaseScript = redis.NewScript(`
	if redis.call('EXISTS', KEYS[1]) == 1 then
		redis.call('PEXPIRE', KEYS[1], ARGV[1])
		return 1
	end
	return 0
`)

func (s *redisStore) touchPodLease(ctx context.Context, podName string, idleTTL time.Duration) error {
	return touchPodLeaseScript.Run(ctx, s.client, []string{s.podTTLKey(podName)}, idleTTL.Milliseconds()).Err()
}

func (s *redisStore) podLeaseExists(ctx context.Context, podName string) (bool, error) {
	n, err := s.client.Exists(ctx, s.podTTLKey(podName)).Result()
	return n > 0, err
}

func (s *redisStore) heartbeatExists(ctx context.Context, vmcpUID string) (bool, error) {
	n, err := s.client.Exists(ctx, s.heartbeatKey(vmcpUID)).Result()
	return n > 0, err
}

func (s *redisStore) writeHeartbeat(ctx context.Context, vmcpUID string, ttl time.Duration) error {
	return s.client.Set(ctx, s.heartbeatKey(vmcpUID), "1", ttl).Err()
}
