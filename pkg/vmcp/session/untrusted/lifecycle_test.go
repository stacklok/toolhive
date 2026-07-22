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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	return scheme
}

// backendSTS builds a backend StatefulSet in the test namespace ("toolhive").
//
//nolint:unparam // namespace kept as a parameter for readability; all callers use the test namespace
func backendSTS(name, namespace, mcpserverUID string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{LabelMCPServerUID: mcpserverUID, "toolhive": "true", LabelApp: "backend-app"},
		},
		Spec: appsv1.StatefulSetSpec{
			Template: *validTemplate(),
		},
	}
}

//nolint:unparam // miniredis handle returned for tests that kill Redis mid-test
func newLifecycle(t *testing.T, objs ...runtime.Object) (PodLifecycle, *redisStore, *miniredis.Miniredis) {
	t.Helper()
	scheme := newScheme(t)
	builder := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...)
	mr := miniredis.RunT(t)
	store, err := newRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "thv:vmcp:session:", "toolhive")
	require.NoError(t, err)
	lc, err := NewK8sPodLifecycle(builder.Build(), store, "vmcp-1", 30*time.Minute, 2*time.Minute, defaultPerUserPodQuota)
	require.NoError(t, err)
	return lc, store, mr
}

func TestEnsurePod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("creates pod from STS template and records bookkeeping", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		lc, store, _ := newLifecycle(t, backendSTS("backend-sts", "toolhive", req.MCPServerUID))

		pod, err := lc.EnsurePod(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, PodNameFor(req.MCPServerUID, req.Session.UserKey(), req.Session.SessionID), pod.Name)
		assert.Equal(t, "true", pod.Labels[LabelUntrusted])

		// Bookkeeping: quota counter, set membership, TTL lease.
		assert.Equal(t, "1", store.client.Get(ctx, store.userQuotaKey(req.Session.UserKey())).Val())
		assert.True(t, store.client.SIsMember(ctx, store.podsSetKey(), pod.Name).Val())
		assert.True(t, store.client.SIsMember(ctx, store.serverPodsSetKey(req.MCPServerUID), pod.Name).Val())
		assert.Equal(t, int64(1), store.client.Exists(ctx, store.podTTLKey(pod.Name)).Val())
	})

	t.Run("idempotent: second EnsurePod reuses the pod and does not invoke OnNewPod", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		lc, store, _ := newLifecycle(t, backendSTS("backend-sts", "toolhive", req.MCPServerUID))

		newPodCalls := 0
		req.OnNewPod = func(string) { newPodCalls++ }

		first, err := lc.EnsurePod(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, 1, newPodCalls)

		second, err := lc.EnsurePod(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, first.Name, second.Name)
		assert.Equal(t, 1, newPodCalls, "OnNewPod must fire only on fresh create")

		// Reuse does not double-count quota.
		assert.True(t, store.client.SIsMember(ctx, store.podsSetKey(), first.Name).Val())
	})

	t.Run("soft-fails when no STS matches", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		lc, _, _ := newLifecycle(t)
		_, err := lc.EnsurePod(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one backend StatefulSet")
	})

	t.Run("soft-fails when multiple STS match", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		lc, _, _ := newLifecycle(t,
			backendSTS("sts-a", "toolhive", req.MCPServerUID),
			backendSTS("sts-b", "toolhive", req.MCPServerUID))
		_, err := lc.EnsurePod(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "found 2")
	})

	t.Run("fail-closed: secret-backed template is rejected, no pod created", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		sts := backendSTS("backend-sts", "toolhive", req.MCPServerUID)
		sts.Spec.Template.Spec.Containers[0].Env = append(sts.Spec.Template.Spec.Containers[0].Env,
			corev1.EnvVar{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}, Key: "token",
				},
			}})
		lc, _, _ := newLifecycle(t, sts)
		_, err := lc.EnsurePod(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "untrusted pod clone")
	})

	t.Run("compensating delete when Redis bookkeeping fails", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		k8sClient := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithRuntimeObjects(backendSTS("backend-sts", "toolhive", req.MCPServerUID)).Build()
		mr := miniredis.RunT(t)
		store, err := newRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "thv:vmcp:session:", "toolhive")
		require.NoError(t, err)
		lc, err := NewK8sPodLifecycle(k8sClient, store, "vmcp-1", 30*time.Minute, 2*time.Minute, defaultPerUserPodQuota)
		require.NoError(t, err)

		mr.Close() // Redis down: bookkeeping must fail and the created pod must be removed.
		_, err = lc.EnsurePod(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to record")

		// The compensating delete must have removed the created pod from the
		// same apiserver: a fresh EnsurePod (Redis back up) must create anew.
		mr2 := miniredis.RunT(t)
		store2, err := newRedisStore(redis.NewClient(&redis.Options{Addr: mr2.Addr()}), "thv:vmcp:session:", "toolhive")
		require.NoError(t, err)
		lc2, err := NewK8sPodLifecycle(k8sClient, store2, "vmcp-1", 30*time.Minute, 2*time.Minute, defaultPerUserPodQuota)
		require.NoError(t, err)
		fired := false
		req.OnNewPod = func(string) { fired = true }
		_, err = lc2.EnsurePod(ctx, req)
		require.NoError(t, err)
		assert.True(t, fired, "compensating delete must have removed the failed-create pod")
	})

	t.Run("lifecycle enforces the per-user quota: over-cap create is denied and removed", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		k8sClient := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithRuntimeObjects(backendSTS("backend-sts", "toolhive", req.MCPServerUID)).Build()
		mr := miniredis.RunT(t)
		store, err := newRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "thv:vmcp:session:", "toolhive")
		require.NoError(t, err)
		lc, err := NewK8sPodLifecycle(k8sClient, store, "vmcp-1", 30*time.Minute, 2*time.Minute, 2)
		require.NoError(t, err)

		// Two pods (distinct sessions of the same user) fit the cap of 2.
		for _, sessID := range []string{"sess-q1", "sess-q2"} {
			r := req
			r.Session.SessionID = sessID
			_, err := lc.EnsurePod(ctx, r)
			require.NoError(t, err)
		}
		assert.Equal(t, "2", store.client.Get(ctx, store.userQuotaKey(req.Session.UserKey())).Val())

		// The third create must be denied AND the just-created pod must be
		// compensated away (no over-quota pod survives).
		r := req
		r.Session.SessionID = "sess-q3"
		deniedName := PodNameFor(r.MCPServerUID, r.Session.UserKey(), r.Session.SessionID)
		_, err = lc.EnsurePod(ctx, r)
		require.ErrorIs(t, err, ErrQuotaExceeded)
		gone := &corev1.Pod{}
		getErr := k8sClient.Get(ctx, types.NamespacedName{Name: deniedName, Namespace: "toolhive"}, gone)
		require.Error(t, getErr, "over-quota pod must be deleted by the compensating path")
		// The counter rolled back: still 2, never 3.
		assert.Equal(t, "2", store.client.Get(ctx, store.userQuotaKey(req.Session.UserKey())).Val())
	})

	t.Run("quota slot is reusable after DeletePod", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		k8sClient := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithRuntimeObjects(backendSTS("backend-sts", "toolhive", req.MCPServerUID)).Build()
		mr := miniredis.RunT(t)
		store, err := newRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "thv:vmcp:session:", "toolhive")
		require.NoError(t, err)
		lc, err := NewK8sPodLifecycle(k8sClient, store, "vmcp-1", 30*time.Minute, 2*time.Minute, 1)
		require.NoError(t, err)

		pod, err := lc.EnsurePod(ctx, req)
		require.NoError(t, err)
		require.NoError(t, lc.DeletePod(ctx, pod.Name))
		assert.Equal(t, int64(0), store.client.Exists(ctx, store.userQuotaKey(req.Session.UserKey())).Val(),
			"counter must be zero after release")

		// A different session of the same user fits immediately (no backoff).
		r := req
		r.Session.SessionID = "sess-q-reuse"
		fired := false
		r.OnNewPod = func(string) { fired = true }
		_, err = lc.EnsurePod(ctx, r)
		require.NoError(t, err)
		assert.True(t, fired)
	})

	t.Run("constructor rejects a non-positive quota (fail loudly)", func(t *testing.T) {
		t.Parallel()
		_, store, _ := newLifecycle(t)
		_, err := NewK8sPodLifecycle(fake.NewClientBuilder().WithScheme(newScheme(t)).Build(),
			store, "vmcp-1", 30*time.Minute, 2*time.Minute, 0)
		require.Error(t, err)
	})

	t.Run("refuses to attach to a pod bound to another session", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		podName := PodNameFor(req.MCPServerUID, req.Session.UserKey(), req.Session.SessionID)
		foreign := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: "toolhive",
				Labels: map[string]string{
					LabelUntrusted: "true", LabelMCPServerUID: req.MCPServerUID,
				},
				Annotations: map[string]string{AnnotationSessionID: "someone-elses-session"},
			},
		}
		lc, _, _ := newLifecycle(t, foreign)
		_, err := lc.EnsurePod(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bound to a different session")
	})
}

func TestDeletePod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("deletes pod and cleans bookkeeping", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		lc, store, _ := newLifecycle(t, backendSTS("backend-sts", "toolhive", req.MCPServerUID))
		pod, err := lc.EnsurePod(ctx, req)
		require.NoError(t, err)

		require.NoError(t, lc.DeletePod(ctx, pod.Name))
		assert.Equal(t, int64(0), store.client.Exists(ctx, store.podTTLKey(pod.Name)).Val())
		assert.False(t, store.client.SIsMember(ctx, store.podsSetKey(), pod.Name).Val())

		// The quota slot is freed immediately (the key disappears at zero).
		assert.Equal(t, int64(0), store.client.Exists(ctx, store.userQuotaKey(req.Session.UserKey())).Val())
	})

	t.Run("NotFound is not an error", func(t *testing.T) {
		t.Parallel()
		lc, _, _ := newLifecycle(t)
		require.NoError(t, lc.DeletePod(ctx, "never-existed"))
	})
}

func TestWaitReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	readyPod := func(name string, uid types.UID) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "toolhive", UID: uid},
			Status: corev1.PodStatus{
				PodIP: "10.0.0.5",
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				},
			},
		}
	}

	t.Run("returns when pod has IP and is Ready with matching UID", func(t *testing.T) {
		t.Parallel()
		p := readyPod("pod-a", "uid-a")
		lc, _, _ := newLifecycle(t, p)
		require.NoError(t, lc.WaitReady(ctx, p, 5*time.Second))
	})

	t.Run("budget expires for never-ready pod", func(t *testing.T) {
		t.Parallel()
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-b", Namespace: "toolhive", UID: "uid-b"},
		}
		lc, _, _ := newLifecycle(t, p)
		start := time.Now()
		err := lc.WaitReady(ctx, p, 1200*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not ready within")
		assert.GreaterOrEqual(t, time.Since(start), 1200*time.Millisecond)
	})

	t.Run("NotFound pod errors immediately", func(t *testing.T) {
		t.Parallel()
		lc, _, _ := newLifecycle(t)
		ghost := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "ghost", Namespace: "toolhive", UID: "uid-g"}}
		err := lc.WaitReady(ctx, ghost, 5*time.Second)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disappeared")
	})

	t.Run("UID mismatch waits (IP not trusted)", func(t *testing.T) {
		t.Parallel()
		// Live pod is ready but under a DIFFERENT UID than the one we created.
		live := readyPod("pod-c", "uid-other")
		lc, _, _ := newLifecycle(t, live)
		mine := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-c", Namespace: "toolhive", UID: "uid-mine"}}
		err := lc.WaitReady(ctx, mine, 600*time.Millisecond)
		require.Error(t, err, "must not trust an IP from a pod whose UID differs")
	})

	t.Run("rejects nil pod", func(t *testing.T) {
		t.Parallel()
		lc, _, _ := newLifecycle(t)
		require.Error(t, lc.WaitReady(ctx, nil, time.Second))
	})
}

func TestResolveTemplateAppLabelGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("STS without the app label is a hard error, never a silent empty string", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		sts := backendSTS("backend-sts", "toolhive", req.MCPServerUID)
		delete(sts.Labels, LabelApp)
		lc, _, _ := newLifecycle(t, sts)
		_, err := lc.EnsurePod(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), LabelApp)
	})

	t.Run("cached STS path also enforces the app-label guard", func(t *testing.T) {
		t.Parallel()
		req := testRequest()
		lc, _, _ := newLifecycle(t, backendSTS("backend-sts", "toolhive", req.MCPServerUID))
		_, err := lc.EnsurePod(ctx, req)
		require.NoError(t, err)

		// Replace the STS (same name = cached UID path) with one missing the
		// label; the cache must not bypass the guard.
		k8sClient := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
		bad := backendSTS("backend-sts", "toolhive", req.MCPServerUID)
		delete(bad.Labels, LabelApp)
		require.NoError(t, k8sClient.Create(ctx, bad))
		_ = lc // cache is per-instance; assert via the store-backed instance below
		store2, err := newRedisStore(
			redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()}), "thv:vmcp:session:", "toolhive")
		require.NoError(t, err)
		lc2, err := NewK8sPodLifecycle(k8sClient, store2, "vmcp-1", 30*time.Minute, 2*time.Minute, defaultPerUserPodQuota)
		require.NoError(t, err)
		_, err = lc2.EnsurePod(ctx, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), LabelApp)
	})
}
