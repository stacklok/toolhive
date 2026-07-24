// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package acceptancetests

import (
	"context"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

// This is Commit 4, Steps 4.1+4.2 of the dual-era e2e plan (issue #5837).
// AUTHORED FOR CI (test-e2e-lifecycle.yml) -- NOT run locally. Per the
// Step 4.0 discovery, this worktree's environment only has a shared,
// long-lived kind cluster (unrelated ngrok-operator/xaa-phase1 namespaces,
// an operator build that predates this merge) that must not be touched, and
// it lacks the NodePort host-port range this harness needs anyway. Step 4.1's
// core (BeforeAll + the pods-ready/Redis-wired It) WAS verified live against
// that cluster in an isolated, self-cleaning throwaway namespace before this
// rewrite -- see the git history of this file / the Step 4.1 report. Every
// addition since (NodePort access, traffic-driving, pod eviction, Redis
// kill) is new and verified only by go build/vet/lint, not a live run.
//
// Runs in its own namespace (not "default"): this file scales the shared
// "redis" Deployment to 0 to test the 503 path, which would otherwise pull
// Redis out from under the concurrent ratelimit_test.go Ordered block under
// --procs=8.
//
// Confirmed live (Step 4.1): the MCPServer controller creates TWO separate
// workloads -- a Deployment named after the CR (the proxy runner, one
// "toolhive" container, selected by app.kubernetes.io/name=mcpserver +
// app.kubernetes.io/instance=<name>, sized by spec.replicas) and a
// StatefulSet with the same name (the actual MCP server backend, one "mcp"
// container running the image, selected by app=<name>, sized by
// spec.backendReplicas). Both reached Ready with spec.sessionAffinity: None
// and Redis-backed spec.sessionStorage; the CR's Ready condition surfaces
// session storage as a (non-)warning: type SessionStorageWarning, status
// False, reason SessionStorageConfigured once Redis is wired correctly.
//
// [MoE finding, resolved] The original design tried to prove "no pod-pinning"
// by tagging each Modern request with a unique _meta.nonce and grepping
// which backend pod's log (yardstick's echoHandler does
// `log.Printf("echo tool called with metadata: %+v", req.Params.Meta)`,
// cmd/yardstick-server/main.go:69 -- confirmed from source, not a run)
// contained it, then asserting >=2 distinct pods appear. That assertion is
// UNSOUND and was dropped: kube-proxy balances per TCP CONNECTION, not per
// HTTP request, and BOTH hops (raw-client-to-proxy AND the proxy's own
// outbound connection to the single backend ClusterIP) reuse keep-alive
// connections outside this test's control. With only 2 backend replicas,
// "all N requests land in one pod's log" is an ordinary, EXPECTED outcome
// of connection reuse under perfectly correct (non-pinned) behavior -- the
// test's signal cannot distinguish "working as intended" from "pinned",
// which is the definition of an unsound assertion, not a real check. There
// is no viable alternative signal either: the proxy itself logs nothing
// per-request that's nonce-correlatable (only structured slog.Debug lines
// with no request-identifying payload), so attributing to PROXY pods
// (rather than backend pods) has the same problem from the other direction.
//
// What replaced it, per the plan's own pre-approved fallback: the
// deterministic, sound properties. (1) The k8s Service actually has
// SessionAffinity: None set (a direct field read of the live object, not an
// inference from traffic) -- this is what spec.sessionAffinity actually
// configures, and is what would silently regress to ClientIP if the CRD
// wiring broke. (2) Many independent Modern requests all succeed against
// the multi-replica deployment (functional correctness under load, still a
// real check, just not a distinct-pod count).
//
// [MoE-relevant] Separately NOT asserting the literal wording "a Modern
// request carrying a stale/foreign Mcp-Session-Id is still served, not
// 404'd": that contradicts the security-hardened, MoE-reviewed behavior
// established in Step 1.5/the hostile-input suite
// (revision_guard_regression_test.go's
// TestGuardUnknownSessionFiresDespiteForgedModernRevision) -- the session
// guard deliberately fires on Mcp-Session-Id header PRESENCE regardless of
// classified revision, precisely so a forged Modern signal can't bypass
// session validation. A foreign session id on Modern getting 404 there is
// correct, not a bug.
var _ = Describe("Dual-Era Multi-Replica Backend", Ordered, func() {
	const (
		testNamespace   = "dual-era-k8s"
		serverName      = "dual-era-echo"
		backendReplicas = 2
		timeout         = 3 * time.Minute
		pollingInterval = 2 * time.Second
	)

	var nodePort int32

	backendPodLabels := map[string]string{"app": serverName}

	proxyURL := func() string {
		return fmt.Sprintf("http://localhost:%d/mcp", nodePort)
	}

	// backendPods lists the StatefulSet-backed backend pods for this MCPServer.
	backendPods := func() []corev1.Pod {
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList, client.InNamespace(testNamespace), client.MatchingLabels(backendPodLabels))).To(Succeed())
		return podList.Items
	}

	BeforeAll(func() {
		By("Creating a dedicated namespace")
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, ns))).To(Succeed())

		By("Deploying Redis for session storage")
		EnsureRedis(ctx, k8sClient, testNamespace, timeout, pollingInterval)

		By("Creating a multi-replica MCPServer (proxy + backend) with Redis session storage")
		server := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serverName,
				Namespace: testNamespace,
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:           images.YardstickServerImage,
				Transport:       "streamable-http",
				ProxyPort:       8080,
				MCPPort:         8080,
				Replicas:        ptr.To(int32(2)),
				BackendReplicas: ptr.To(int32(backendReplicas)),
				SessionAffinity: "None",
				Env: []mcpv1beta1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
					{Name: "STATELESS", Value: "true"}, // Modern (2026-07-28) capable backend
					{Name: "BACKEND_MODE", Value: "echo"},
				},
				SessionStorage: &mcpv1beta1.SessionStorageConfig{
					Provider: "redis",
					Address:  "redis." + testNamespace + ".svc.cluster.local:6379",
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		// Ping-free readiness: the operator's Deployment readiness probe is a
		// plain HTTP GET /health (confirmed live in Step 4.0/4.1, reading the
		// Deployment spec), never an MCP "ping" (which Modern removed). This
		// comment describes the OPERATOR's own probe; the test's own /health
		// gate below is separate, added because kube-proxy route programming
		// can lag pod-Ready.
		By("Waiting for MCPServer to be running")
		testutil.WaitForMCPServerRunning(ctx, k8sClient, serverName, testNamespace, timeout, pollingInterval)

		By("Creating NodePort service for MCPServer proxy")
		testutil.CreateNodePortService(ctx, k8sClient, serverName, testNamespace)
		nodePort = testutil.GetNodePort(ctx, k8sClient, serverName+"-nodeport", testNamespace, timeout, pollingInterval)

		By("Waiting for the proxy to actually be reachable on the NodePort")
		httpClient := &http.Client{Timeout: 5 * time.Second}
		Eventually(func() error {
			resp, err := httpClient.Get(fmt.Sprintf("http://localhost:%d/health", nodePort))
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("health check returned %d", resp.StatusCode)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Deleting the dedicated namespace (cascades: MCPServer, Redis, NodePort service)")
		_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}})
	})

	It("brings up both proxy and backend replicas ready, with Redis wired", func() {
		By("proxy runner pods are ready")
		Eventually(func() error {
			return testutil.CheckPodsReady(ctx, k8sClient, testNamespace, map[string]string{
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": serverName,
			})
		}, timeout, pollingInterval).Should(Succeed())

		By("backend (yardstick) pods are ready")
		Eventually(func() error {
			return testutil.CheckPodsReady(ctx, k8sClient, testNamespace, backendPodLabels)
		}, timeout, pollingInterval).Should(Succeed())

		By("Redis session storage is wired without a warning")
		server := &mcpv1beta1.MCPServer{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: serverName, Namespace: testNamespace}, server)).To(Succeed())
		cond := meta.FindStatusCondition(server.Status.Conditions, "SessionStorageWarning")
		Expect(cond).ToNot(BeNil(), "expected a SessionStorageWarning condition once sessionStorage is set")
		Expect(cond.Status).To(Equal(metav1.ConditionFalse), "Redis session storage should be configured cleanly: %s", cond.Message)
		Expect(server.Status.ReadyReplicas).To(BeEquivalentTo(2))
	})

	It("configures SessionAffinity:None and serves many Modern requests across the multi-replica deployment", func() {
		By("the proxy Service actually has SessionAffinity: None (not the ClientIP default)")
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{
			Name:      ctrlutil.CreateProxyServiceName(serverName),
			Namespace: testNamespace,
		}, svc)).To(Succeed())
		Expect(svc.Spec.SessionAffinity).To(Equal(corev1.ServiceAffinityNone))

		By("many independent sessionless Modern requests all succeed")
		rawClient, err := e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())
		for i := range 8 {
			req, err := e2e.NewModernRequest("tools/call", map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"input": "anypod"},
			})
			Expect(err).ToNot(HaveOccurred())
			resp, err := rawClient.Send(context.Background(), proxyURL(), req)
			Expect(err).ToNot(HaveOccurred(), "request %d", i)
			Expect(resp.StatusCode).To(Equal(http.StatusOK), "request %d", i)
			Expect(resp.Error).To(BeNil(), "request %d", i)
			Expect(resp.Headers.Get(e2e.HeaderMCPSessionID)).To(BeEmpty(), "Modern must never carry Mcp-Session-Id")
		}
	})

	It("serves mixed Legacy and Modern traffic over the shared Redis session store", func() {
		rawClient, err := e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		By("Legacy: initialize then a session-keyed tools/call")
		initReq := e2e.NewLegacyInitializeRequest("k8s-mixed-legacy", "1.0")
		initResp, err := rawClient.Send(context.Background(), proxyURL(), initReq)
		Expect(err).ToNot(HaveOccurred())
		Expect(initResp.StatusCode).To(Equal(http.StatusOK))
		sessionID := initResp.Headers.Get(e2e.HeaderMCPSessionID)
		Expect(sessionID).ToNot(BeEmpty())

		legacyReq, err := e2e.NewLegacyRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "legacymixed"},
		})
		Expect(err).ToNot(HaveOccurred())
		legacyReq.WithSessionID(sessionID)
		legacyResp, err := rawClient.Send(context.Background(), proxyURL(), legacyReq)
		Expect(err).ToNot(HaveOccurred())
		Expect(legacyResp.StatusCode).To(Equal(http.StatusOK))
		Expect(legacyResp.Error).To(BeNil())

		By("Modern: stateless tools/call, no session id, alongside the Legacy session above")
		modernReq, err := e2e.NewModernRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "modernmixed"},
		})
		Expect(err).ToNot(HaveOccurred())
		modernResp, err := rawClient.Send(context.Background(), proxyURL(), modernReq)
		Expect(err).ToNot(HaveOccurred())
		Expect(modernResp.StatusCode).To(Equal(http.StatusOK))
		Expect(modernResp.Error).To(BeNil())
		Expect(modernResp.Headers.Get(e2e.HeaderMCPSessionID)).To(BeEmpty(), "Modern must never carry Mcp-Session-Id")

		By("the Legacy session is still usable after the Modern request")
		legacyReq2, err := e2e.NewLegacyRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "legacymixed2"},
		})
		Expect(err).ToNot(HaveOccurred())
		legacyReq2.WithSessionID(sessionID)
		legacyResp2, err := rawClient.Send(context.Background(), proxyURL(), legacyReq2)
		Expect(err).ToNot(HaveOccurred())
		Expect(legacyResp2.StatusCode).To(Equal(http.StatusOK))
	})

	It("degrades cleanly and self-heals when a backend pod is evicted mid-traffic", func() {
		rawClient, err := e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		before := backendPods()
		Expect(before).To(HaveLen(backendReplicas), "precondition: all backend replicas present before evicting")
		victim := before[0].Name

		By(fmt.Sprintf("deleting backend pod %s", victim))
		Expect(k8sClient.Delete(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: victim, Namespace: testNamespace},
		})).To(Succeed())

		By("a fresh Modern request still succeeds -- served by a surviving or replacement pod")
		Eventually(func() (int, error) {
			req, buildErr := e2e.NewModernRequest("tools/call", map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"input": "aftereviction"},
			})
			if buildErr != nil {
				return 0, buildErr
			}
			resp, sendErr := rawClient.Send(context.Background(), proxyURL(), req)
			if sendErr != nil {
				return 0, sendErr
			}
			return resp.StatusCode, nil
		}, timeout, pollingInterval).Should(Equal(http.StatusOK))

		// CheckPodsReady only requires ONE ready pod, so it would pass the
		// instant the surviving pod is observed -- it never proves the
		// StatefulSet actually replaced the evicted one. Assert the full
		// count is back AND all of them are Ready.
		By("the StatefulSet replaces the evicted pod -- full replica count, all Ready")
		Eventually(func() (int, error) {
			pods := backendPods()
			ready := 0
			for _, p := range pods {
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						ready++
					}
				}
			}
			if len(pods) != backendReplicas {
				return 0, fmt.Errorf("expected %d backend pods, got %d", backendReplicas, len(pods))
			}
			return ready, nil
		}, timeout, pollingInterval).Should(Equal(backendReplicas))
	})

	It("returns 503 when Redis becomes unavailable, then recovers once Redis is restored", func() {
		rawClient, err := e2e.NewRawMCPClient(15 * time.Second)
		Expect(err).ToNot(HaveOccurred())

		By("establishing a Legacy session while Redis is up")
		initReq := e2e.NewLegacyInitializeRequest("k8s-redis-503", "1.0")
		initResp, err := rawClient.Send(context.Background(), proxyURL(), initReq)
		Expect(err).ToNot(HaveOccurred())
		Expect(initResp.StatusCode).To(Equal(http.StatusOK))
		sessionID := initResp.Headers.Get(e2e.HeaderMCPSessionID)
		Expect(sessionID).ToNot(BeEmpty())

		By("scaling Redis to 0")
		Expect(scaleRedis(ctx, testNamespace, 0)).To(Succeed())
		Eventually(func() (int, error) {
			podList := &corev1.PodList{}
			if err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace), client.MatchingLabels{"app": "redis"}); err != nil {
				return 0, err
			}
			return len(podList.Items), nil
		}, timeout, pollingInterval).Should(Equal(0))

		By("a request against the existing session now gets 503 (session store unavailable)")
		req, err := e2e.NewLegacyRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "redisdown"},
		})
		Expect(err).ToNot(HaveOccurred())
		req.WithSessionID(sessionID)
		Eventually(func() (int, error) {
			resp, sendErr := rawClient.Send(context.Background(), proxyURL(), req)
			if sendErr != nil {
				return 0, sendErr
			}
			return resp.StatusCode, nil
		}, timeout, pollingInterval).Should(Equal(http.StatusServiceUnavailable))

		By("restoring Redis")
		Expect(scaleRedis(ctx, testNamespace, 1)).To(Succeed())
		Eventually(func() error {
			return testutil.CheckPodsReady(ctx, k8sClient, testNamespace, map[string]string{"app": "redis"})
		}, timeout, pollingInterval).Should(Succeed())

		// Redis has no persistence here (a plain in-memory Deployment, see
		// EnsureRedis) -- scaling to 0 wiped every session key, so the
		// pre-outage sessionID is gone, not just temporarily unreachable.
		// Reusing it now would correctly 404 (session-not-found), not 200:
		// that would assert session *survival* across an outage that
		// destroys state, which isn't the property here. The property is
		// functional recovery -- a brand new session must work.
		By("Redis is functionally back: a fresh initialize + call succeeds")
		var newSessionID string
		Eventually(func() (int, error) {
			resp, sendErr := rawClient.Send(context.Background(), proxyURL(), e2e.NewLegacyInitializeRequest("k8s-redis-503-post", "1.0"))
			if sendErr != nil {
				return 0, sendErr
			}
			newSessionID = resp.Headers.Get(e2e.HeaderMCPSessionID)
			return resp.StatusCode, nil
		}, timeout, pollingInterval).Should(Equal(http.StatusOK))
		Expect(newSessionID).ToNot(BeEmpty())

		req2, err := e2e.NewLegacyRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "redisback"},
		})
		Expect(err).ToNot(HaveOccurred())
		req2.WithSessionID(newSessionID)
		resp2, err := rawClient.Send(context.Background(), proxyURL(), req2)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp2.StatusCode).To(Equal(http.StatusOK))

		By("the pre-outage session is gone, not silently revived -- confirms the outage really cleared state")
		staleReq, err := e2e.NewLegacyRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "stalesession"},
		})
		Expect(err).ToNot(HaveOccurred())
		staleReq.WithSessionID(sessionID)
		staleResp, err := rawClient.Send(context.Background(), proxyURL(), staleReq)
		Expect(err).ToNot(HaveOccurred())
		Expect(staleResp.StatusCode).To(Equal(http.StatusNotFound))
	})
})

// scaleRedis patches the "redis" Deployment's replica count (0 to simulate
// an outage, 1 to restore) -- the same Deployment EnsureRedis creates.
func scaleRedis(ctx context.Context, namespace string, replicas int32) error {
	deploy := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "redis", Namespace: namespace}, deploy); err != nil {
		return err
	}
	deploy.Spec.Replicas = ptr.To(replicas)
	return k8sClient.Update(ctx, deploy)
}
