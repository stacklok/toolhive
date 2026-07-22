// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func reaperPod(name string, age time.Duration, ready bool, annotations map[string]string) *corev1.Pod {
	status := corev1.PodStatus{}
	if ready {
		status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	labels := map[string]string{
		LabelUntrusted:     "true",
		LabelUntrustedUser: userHash("iss\x00sub"),
		LabelMCPServerUID:  "uid-abc",
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "toolhive",
			Labels:            labels,
			Annotations:       annotations,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Status: status,
	}
}

func newReaper(
	t *testing.T, sessionExists bool, objs ...client.Object,
) (*Reaper, func(context.Context, string) bool, *redisStore, *miniredis.Miniredis, client.Client) {
	t.Helper()
	scheme := newScheme(t)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	mr := miniredis.RunT(t)
	store, err := newRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "thv:vmcp:session:", "toolhive")
	require.NoError(t, err)
	r, err := NewReaper(k8sClient, store, ReaperConfig{IdleTTL: time.Minute}, "vmcp-self", nil)
	require.NoError(t, err)
	exists := func(context.Context, string) bool { return sessionExists }
	return r, exists, store, mr, k8sClient
}

func podExists(t *testing.T, c client.Client, name string) bool {
	t.Helper()
	p := &corev1.Pod{}
	err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "toolhive"}, p)
	return err == nil
}

func TestReaperTick(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("idle pod (no TTL lease) is deleted", func(t *testing.T) {
		t.Parallel()
		p := reaperPod("pod-idle", 10*time.Minute, true, map[string]string{AnnotationVMCPUid: "vmcp-self"})
		r, exists, _, _, c := newReaper(t, false, p)

		r.tick(ctx, exists)
		assert.False(t, podExists(t, c, "pod-idle"))
	})

	t.Run("pod with live lease and live session is kept and lease refreshed", func(t *testing.T) {
		t.Parallel()
		ann := map[string]string{AnnotationVMCPUid: "vmcp-self", AnnotationSessionID: "sess-1"}
		p := reaperPod("pod-live", 10*time.Minute, true, ann)
		r, exists, store, _, c := newReaper(t, true, p)
		require.NoError(t, store.client.Set(ctx, store.podTTLKey("pod-live"), "1", 10*time.Second).Err())

		r.tick(ctx, exists)
		assert.True(t, podExists(t, c, "pod-live"))
		ttl := store.client.PTTL(ctx, store.podTTLKey("pod-live")).Val()
		assert.Greater(t, ttl, 30*time.Second, "lease must be refreshed to idle TTL")
	})

	t.Run("pod with live lease but dead session keeps lease until it lapses", func(t *testing.T) {
		t.Parallel()
		ann := map[string]string{AnnotationVMCPUid: "vmcp-self", AnnotationSessionID: "sess-gone"}
		p := reaperPod("pod-dying", time.Minute, true, ann)
		r, exists, store, _, c := newReaper(t, false, p)
		require.NoError(t, store.client.Set(ctx, store.podTTLKey("pod-dying"), "1", time.Minute).Err())

		r.tick(ctx, exists)
		assert.True(t, podExists(t, c, "pod-dying"), "lease still alive: refresh simply stops; deletion happens at lapse")
	})

	t.Run("readiness timeout deletes old not-ready pod even with lease", func(t *testing.T) {
		t.Parallel()
		p := reaperPod("pod-stuck", 5*time.Minute, false, map[string]string{AnnotationVMCPUid: "vmcp-self"})
		r, exists, store, _, c := newReaper(t, false, p)
		require.NoError(t, store.client.Set(ctx, store.podTTLKey("pod-stuck"), "1", time.Hour).Err())

		r.tick(ctx, exists)
		assert.False(t, podExists(t, c, "pod-stuck"))
	})

	t.Run("young not-ready pod is kept", func(t *testing.T) {
		t.Parallel()
		p := reaperPod("pod-cold", 30*time.Second, false, map[string]string{AnnotationVMCPUid: "vmcp-self"})
		r, exists, store, _, c := newReaper(t, false, p)
		require.NoError(t, store.client.Set(ctx, store.podTTLKey("pod-cold"), "1", time.Hour).Err())

		r.tick(ctx, exists)
		assert.True(t, podExists(t, c, "pod-cold"))
	})

	t.Run("zombie pod deleted only after heartbeat grace", func(t *testing.T) {
		t.Parallel()
		ann := map[string]string{AnnotationVMCPUid: "vmcp-dead", AnnotationSessionID: "sess-1"}
		p := reaperPod("pod-zombie", 10*time.Minute, true, ann)
		r, exists, store, _, c := newReaper(t, true, p)
		require.NoError(t, store.client.Set(ctx, store.podTTLKey("pod-zombie"), "1", time.Hour).Err())

		// First tick: absence observed, grace starts — pod kept.
		r.tick(ctx, exists)
		assert.True(t, podExists(t, c, "pod-zombie"))

		// Simulate the grace period having elapsed since first observation.
		r.mu.Lock()
		r.zombies["pod-zombie"] = r.now().Add(-zombieHeartbeatGrace - time.Second)
		r.mu.Unlock()
		r.tick(ctx, exists)
		assert.False(t, podExists(t, c, "pod-zombie"))
	})

	t.Run("zombie clears when heartbeat returns", func(t *testing.T) {
		t.Parallel()
		ann := map[string]string{AnnotationVMCPUid: "vmcp-back", AnnotationSessionID: "sess-1"}
		p := reaperPod("pod-revived", 10*time.Minute, true, ann)
		r, exists, store, _, c := newReaper(t, true, p)
		require.NoError(t, store.client.Set(ctx, store.podTTLKey("pod-revived"), "1", time.Hour).Err())
		require.NoError(t, store.writeHeartbeat(ctx, "vmcp-back", 5*time.Minute))

		r.tick(ctx, exists)
		assert.True(t, podExists(t, c, "pod-revived"))
		r.mu.Lock()
		_, marked := r.zombies["pod-revived"]
		r.mu.Unlock()
		assert.False(t, marked)
	})

	t.Run("Redis down: tick skipped, no deletions", func(t *testing.T) {
		t.Parallel()
		p := reaperPod("pod-idle", 10*time.Minute, true, map[string]string{AnnotationVMCPUid: "vmcp-self"})
		r, exists, _, mr, c := newReaper(t, false, p)
		mr.Close()

		r.tick(ctx, exists)
		assert.True(t, podExists(t, c, "pod-idle"), "fail safe: no deletions without Redis evidence")
	})

	t.Run("reconciliation heals set drift and prunes orphan quota keys", func(t *testing.T) {
		t.Parallel()
		ann := map[string]string{AnnotationVMCPUid: "vmcp-self", AnnotationSessionID: "sess-1"}
		p := reaperPod("pod-live", time.Minute, true, ann)
		r, exists, store, _, _ := newReaper(t, true, p)
		require.NoError(t, store.client.Set(ctx, store.podTTLKey("pod-live"), "1", time.Hour).Err())

		// Inject drift: a stale pod in the sets and an orphaned quota key.
		require.NoError(t, store.client.SAdd(ctx, store.podsSetKey(), "pod-stale").Err())
		require.NoError(t, store.client.SAdd(ctx, store.serverPodsSetKey("uid-abc"), "pod-stale").Err())
		orphanQuotaKey := store.quotaKeyForUserHash("deaduserhash")
		require.NoError(t, store.client.Set(ctx, orphanQuotaKey, 1, time.Hour).Err())
		require.NoError(t, store.client.SAdd(ctx, store.quotaKeysRegistryKey(), orphanQuotaKey).Err())

		r.tick(ctx, exists)

		assert.False(t, store.client.SIsMember(ctx, store.podsSetKey(), "pod-stale").Val())
		assert.True(t, store.client.SIsMember(ctx, store.podsSetKey(), "pod-live").Val())
		assert.False(t, store.client.SIsMember(ctx, store.serverPodsSetKey("uid-abc"), "pod-stale").Val())
		assert.Equal(t, int64(0), store.client.Exists(ctx, orphanQuotaKey).Val())
	})

	t.Run("reconciliation corrects a drifted quota counter to the live pod count", func(t *testing.T) {
		t.Parallel()
		ann := map[string]string{AnnotationVMCPUid: "vmcp-self", AnnotationSessionID: "sess-1"}
		p := reaperPod("pod-live", time.Minute, true, ann)
		r, exists, store, _, _ := newReaper(t, true, p)
		require.NoError(t, store.client.Set(ctx, store.podTTLKey("pod-live"), "1", time.Hour).Err())

		// Drift: the user's counter claims 9 pods, only 1 is live.
		quotaKey := store.userQuotaKey("iss\x00sub")
		require.NoError(t, store.client.Set(ctx, quotaKey, 9, time.Hour).Err())
		require.NoError(t, store.client.SAdd(ctx, store.quotaKeysRegistryKey(), quotaKey).Err())

		r.tick(ctx, exists)

		assert.Equal(t, "1", store.client.Get(ctx, quotaKey).Val(),
			"reaper must correct the counter to the authoritative pod LIST count")

		// Second tick keeps it corrected (idempotent).
		r.tick(ctx, exists)
		assert.Equal(t, "1", store.client.Get(ctx, quotaKey).Val())
	})

	t.Run("reconciliation deletes the counter when the user has no live pods", func(t *testing.T) {
		t.Parallel()
		r, exists, store, _, _ := newReaper(t, false)

		quotaKey := store.userQuotaKey("iss\x00sub")
		require.NoError(t, store.client.Set(ctx, quotaKey, 4, time.Hour).Err())
		require.NoError(t, store.client.SAdd(ctx, store.quotaKeysRegistryKey(), quotaKey).Err())

		r.tick(ctx, exists)
		assert.Equal(t, int64(0), store.client.Exists(ctx, quotaKey).Val())
	})
}

func TestNewReaperValidation(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	r, err := NewReaper(k8sClient, store, ReaperConfig{}, "vmcp-1", nil)
	require.NoError(t, err)
	require.NotNil(t, r)
}
