// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

const (
	// Faster health checking so backend flips propagate within the spec timeout.
	ohrHealthCheckInterval = 5 * time.Second
	ohrHealthCheckTimeout  = 2 * time.Second // must be < interval to prevent queuing
	ohrUnhealthyThreshold  = 2
)

// This suite covers #5786 PR2 (optimizer mode). Its passthrough sibling
// (virtualmcp_health_list_changed_test.go) asserts that a connected session is
// NOTIFIED when a backend recovers. Optimizer mode is the opposite contract:
// the advertised set is only find_tool/call_tool, whose names never change on a
// health flip, so no notification is due — what must change is the index those
// meta-tools search and dispatch through.
//
// So this spec pins both halves on ONE session that never reconnects:
//   - find_tool starts blind to the broken backend's tool, and sees it after
//     recovery (pre-PR2 this required a new session — the existing
//     virtualmcp_optimizer_circuit_breaker_test.go recovery case deliberately
//     opens a fresh client, which is exactly the gap PR2 closes).
//   - call_tool can then invoke it on that same session.
//   - and NO notifications/tools/list_changed is delivered, because the
//     advertised meta-tools are unchanged.
//
// The spec pins the Legacy (2025-11-25, session-based) protocol with the raw
// primitives from legacy_session_helpers_test.go, matching the #6051
// convention: the mcpcompat client negotiates Modern, which has no sessions,
// and this spec is precisely about behavior on a long-lived session.
var _ = Describe("VirtualMCPServer Optimizer Health-Driven Reindex", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-opt-reindex-group"
		vmcpServerName  = "test-vmcp-opt-reindex"
		embeddingName   = "test-opt-reindex-embedding"
		stableBackend   = "backend-ohr-stable"
		unstableBackend = "backend-ohr-unstable"
		timeout         = 5 * time.Minute
		pollingInterval = 2 * time.Second

		vmcpNodePort int32
		stableTool   = stableBackend + "_echo"
		unstableTool = unstableBackend + "_echo"
	)

	BeforeAll(func() {
		By("Creating MCPGroup for optimizer reindex tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for optimizer health-reindex E2E tests", timeout, pollingInterval)

		By("Creating stable and unstable backend MCPServers")
		CreateMCPServerAndWait(ctx, k8sClient, stableBackend, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)
		CreateMCPServerAndWait(ctx, k8sClient, unstableBackend, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating EmbeddingServer for the optimizer")
		embeddingServer := v1beta1test.NewEmbeddingServer(embeddingName, testNamespace,
			v1beta1test.WithEmbeddingModel("BAAI/bge-small-en-v1.5"),
			v1beta1test.WithEmbeddingImage(images.TextEmbeddingsInferenceImage),
		)
		Expect(k8sClient.Create(ctx, embeddingServer)).To(Succeed())

		By("Creating VirtualMCPServer in optimizer mode with fast health checks")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, testNamespace,
			v1beta1test.WithVMCPGroupRef(mcpGroupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
			}),
			v1beta1test.WithVMCPOutgoingAuth(&mcpv1beta1.OutgoingAuthConfig{
				Source: "discovered",
			}),
			v1beta1test.WithVMCPEmbeddingServerRef(embeddingName),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{
				Name:      vmcpServerName,
				Group:     mcpGroupName,
				Optimizer: &vmcpconfig.OptimizerConfig{},
				Aggregation: &vmcpconfig.AggregationConfig{
					ConflictResolution: "prefix",
				},
				Operational: &vmcpconfig.OperationalConfig{
					FailureHandling: &vmcpconfig.FailureHandlingConfig{
						HealthCheckInterval: vmcpconfig.Duration(ohrHealthCheckInterval),
						HealthCheckTimeout:  vmcpconfig.Duration(ohrHealthCheckTimeout),
						UnhealthyThreshold:  ohrUnhealthyThreshold,
					},
				},
			}),
			v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
				v.Spec.ServiceType = "NodePort"
			}),
		)
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to become ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		for _, obj := range []client.Object{
			&mcpv1beta1.VirtualMCPServer{ObjectMeta: metav1.ObjectMeta{Name: vmcpServerName, Namespace: testNamespace}},
			&mcpv1beta1.EmbeddingServer{ObjectMeta: metav1.ObjectMeta{Name: embeddingName, Namespace: testNamespace}},
			&mcpv1beta1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: stableBackend, Namespace: testNamespace}},
			&mcpv1beta1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: unstableBackend, Namespace: testNamespace}},
			&mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace}},
		} {
			if err := k8sClient.Delete(ctx, obj); err != nil {
				GinkgoWriter.Printf("cleanup: failed to delete %T %s: %v\n", obj, obj.GetName(), err)
			}
		}
	})

	It("reindexes a connected session's optimizer when a backend recovers, without reconnect", func() {
		By("Breaking the unstable backend with a non-existent image")
		backend := &mcpv1beta1.MCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: unstableBackend, Namespace: testNamespace,
		}, backend)).To(Succeed())
		backend.Spec.Image = "nonexistent/image:doesnotexist"
		Expect(k8sClient.Update(ctx, backend)).To(Succeed())

		By("Deleting the backend pod so health checks start failing")
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList,
			client.InNamespace(testNamespace),
			client.MatchingLabels{"app": unstableBackend},
		)).To(Succeed())
		for i := range podList.Items {
			Expect(k8sClient.Delete(ctx, &podList.Items[i])).To(Succeed())
		}

		By("Waiting for the vMCP health monitor to mark the backend non-routable")
		Eventually(func() error {
			vmcpServer := &mcpv1beta1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name: vmcpServerName, Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}
			for i := range vmcpServer.Status.DiscoveredBackends {
				b := &vmcpServer.Status.DiscoveredBackends[i]
				if b.Name != unstableBackend {
					continue
				}
				if b.Status == mcpv1beta1.BackendStatusReady || b.Status == mcpv1beta1.BackendStatusDegraded {
					return fmt.Errorf("backend %s still routable: %s", unstableBackend, b.Status)
				}
				return nil
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Initializing a Legacy session while the backend is down")
		rawClient, err := e2e.NewRawMCPClient(30 * time.Second)
		Expect(err).ToNot(HaveOccurred())
		vmcpURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

		var sessionID string
		// Session initialization races with the health flip settling into the
		// aggregated view; retry until this session's optimizer index reflects
		// the broken backend's absence. Each failed attempt may leave a session
		// behind on the server; those expire via the server's session TTL.
		Eventually(func() error {
			sessionID, err = legacySessionInit(rawClient, vmcpURL, "opt-reindex-e2e", nil)
			if err != nil {
				return err
			}
			names, err := legacySessionListTools(rawClient, vmcpURL, sessionID, nil)
			if err != nil {
				return err
			}
			if !slices.Contains(names, "find_tool") || !slices.Contains(names, "call_tool") {
				return fmt.Errorf("optimizer meta-tools missing from tools/list: %v", names)
			}
			found, err := optimizerFindToolNames(rawClient, vmcpURL, sessionID, "echo back a message")
			if err != nil {
				return err
			}
			if slices.Contains(found, unstableTool) {
				return fmt.Errorf("broken backend's tool %s unexpectedly indexed: %v", unstableTool, found)
			}
			if !slices.Contains(found, stableTool) {
				return fmt.Errorf("stable tool %s missing from the index: %v", stableTool, found)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())
		GinkgoWriter.Printf("✓ Session %s initialized with %s absent from the optimizer index\n",
			sessionID, unstableTool)

		By("Opening the session's standalone SSE stream to observe notifications")
		sseCtx, sseCancel := context.WithCancel(context.Background())
		DeferCleanup(sseCancel)
		notified, err := watchSSEForNotification(sseCtx, vmcpURL, sessionID, "notifications/tools/list_changed")
		Expect(err).ToNot(HaveOccurred())

		By("Restoring the unstable backend image")
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: unstableBackend, Namespace: testNamespace,
		}, backend)).To(Succeed())
		backend.Spec.Image = images.YardstickServerImage
		Expect(k8sClient.Update(ctx, backend)).To(Succeed())

		By("Waiting for the backend StatefulSet template to use the fixed image")
		Eventually(func() error {
			sts := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name: unstableBackend, Namespace: testNamespace,
			}, sts); err != nil {
				return err
			}
			for _, container := range sts.Spec.Template.Spec.Containers {
				if container.Name == "mcp" {
					if container.Image != images.YardstickServerImage {
						return fmt.Errorf("statefulset still has image %q", container.Image)
					}
					return nil
				}
			}
			return fmt.Errorf("mcp container not found in statefulset template")
		}, timeout, pollingInterval).Should(Succeed())

		By("Deleting stuck pods so they recreate with the fixed image")
		podList = &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList,
			client.InNamespace(testNamespace),
			client.MatchingLabels{"app": unstableBackend},
		)).To(Succeed())
		for i := range podList.Items {
			if podList.Items[i].Status.Phase == corev1.PodPending {
				Expect(k8sClient.Delete(ctx, &podList.Items[i])).To(Succeed())
			}
		}

		By("Waiting for the backend to become ready again")
		Eventually(func() error {
			server := &mcpv1beta1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name: unstableBackend, Namespace: testNamespace,
			}, server); err != nil {
				return err
			}
			if server.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
				return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Asserting find_tool on the SAME session now surfaces the recovered backend's tool")
		Eventually(func() error {
			found, err := optimizerFindToolNames(rawClient, vmcpURL, sessionID, "echo back a message")
			if err != nil {
				return err
			}
			if !slices.Contains(found, unstableTool) {
				return fmt.Errorf("recovered tool %s not yet indexed: %v", unstableTool, found)
			}
			if !slices.Contains(found, stableTool) {
				return fmt.Errorf("stable tool %s missing after reindex: %v", stableTool, found)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())
		GinkgoWriter.Printf("✓ Session %s reindexed to include %s without reconnecting\n", sessionID, unstableTool)

		By("Calling the recovered backend's tool through call_tool on the same session")
		Eventually(func() error {
			resp, err := legacySessionCallTool(rawClient, vmcpURL, sessionID, "call_tool", map[string]any{
				"tool_name":  unstableTool,
				"parameters": map[string]any{"input": "reindexedhello123"},
			}, nil)
			if err != nil {
				return err
			}
			// Empty resultType is what a Legacy client's envelope carries.
			return dualEraEchoErr(resp, "reindexedhello123", "")
		}, timeout, pollingInterval).Should(Succeed())
		GinkgoWriter.Printf("✓ call_tool invoked %s on session %s without reconnecting\n", unstableTool, sessionID)

		By("Asserting no tools/list_changed was emitted (the advertised meta-tools never changed)")
		// The re-index is already proven to have happened by the assertions
		// above, so an empty channel here is a real absence, not a race: in
		// optimizer mode the session's advertised set is find_tool/call_tool
		// both before and after, and rewriting it purely to trigger a
		// notification would tell the client nothing.
		Consistently(notified, 5*time.Second, pollingInterval).ShouldNot(Receive(),
			"optimizer mode must not emit tools/list_changed for an unchanged advertised set")
	})
})

// optimizerFindToolNames calls find_tool on sessionID and returns the tool names
// it surfaced, which is the observable projection of that session's optimizer
// index.
func optimizerFindToolNames(
	rawClient *e2e.RawMCPClient, url, sessionID, description string,
) ([]string, error) {
	resp, err := legacySessionCallTool(rawClient, url, sessionID, "find_tool",
		map[string]any{"tool_description": description}, nil)
	if err != nil {
		return nil, fmt.Errorf("find_tool: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("find_tool: JSON-RPC error: %+v", resp.Error)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("find_tool: status %d, body: %s", resp.StatusCode, resp.Body)
	}

	var result struct {
		IsError           bool `json:"isError"`
		StructuredContent struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("find_tool: unmarshal result: %w, raw: %s", err, resp.Result)
	}
	if result.IsError {
		return nil, fmt.Errorf("find_tool returned an error result: %s", resp.Result)
	}

	names := make([]string, 0, len(result.StructuredContent.Tools))
	for _, t := range result.StructuredContent.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}
