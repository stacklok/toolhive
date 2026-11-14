// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	coremcp "github.com/stacklok/toolhive/pkg/mcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

const (
	// Faster health checking so backend flips propagate within the spec timeout.
	hlcHealthCheckInterval = 5 * time.Second
	hlcHealthCheckTimeout  = 2 * time.Second // must be < interval to prevent queuing
	hlcUnhealthyThreshold  = 2
)

// This suite covers #5786 (PR1, passthrough mode): a client session that
// initialized while a backend was unhealthy must, WITHOUT reconnecting,
// receive notifications/tools/list_changed when that backend recovers, and be
// able to list and call the recovered backend's tools on the same session.
//
// The spec pins the Legacy (2025-11-25, session-based) protocol with the raw
// primitives from legacy_session_helpers_test.go — the mcpcompat client
// negotiates Modern, which has no sessions, and this spec is precisely about
// session behavior (#6051 convention). The list_changed notification is
// observed on the session's standalone GET SSE stream.
var _ = Describe("VirtualMCPServer Health-Driven tools/list_changed", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-health-listchanged-group"
		vmcpServerName  = "test-vmcp-health-listchanged"
		stableBackend   = "backend-hlc-stable"
		unstableBackend = "backend-hlc-unstable"
		timeout         = 3 * time.Minute
		pollingInterval = 2 * time.Second

		vmcpNodePort int32
		stableTool   = stableBackend + "_echo"
		unstableTool = unstableBackend + "_echo"
	)

	BeforeAll(func() {
		By("Creating MCPGroup for health list_changed tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for health-driven list_changed E2E tests", timeout, pollingInterval)

		By("Creating stable and unstable backend MCPServers")
		CreateMCPServerAndWait(ctx, k8sClient, stableBackend, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)
		CreateMCPServerAndWait(ctx, k8sClient, unstableBackend, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPServer in passthrough mode with fast health checks")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, testNamespace,
			v1beta1test.WithVMCPGroupRef(mcpGroupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
			}),
			v1beta1test.WithVMCPOutgoingAuth(&mcpv1beta1.OutgoingAuthConfig{
				Source: "discovered",
			}),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{
				Name:  vmcpServerName,
				Group: mcpGroupName,
				Aggregation: &vmcpconfig.AggregationConfig{
					ConflictResolution: "prefix",
				},
				Operational: &vmcpconfig.OperationalConfig{
					FailureHandling: &vmcpconfig.FailureHandlingConfig{
						HealthCheckInterval: vmcpconfig.Duration(hlcHealthCheckInterval),
						HealthCheckTimeout:  vmcpconfig.Duration(hlcHealthCheckTimeout),
						UnhealthyThreshold:  hlcUnhealthyThreshold,
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

		By("Verifying both backends' echo tools aggregate while both are healthy")
		WaitForExpectedTools(vmcpNodePort, "health-listchanged-setup", func(tools []mcp.Tool) error {
			return ToolsContainAll(tools, stableTool, unstableTool)
		})
	})

	AfterAll(func() {
		By("Cleaning up test resources")
		for _, obj := range []client.Object{
			&mcpv1beta1.VirtualMCPServer{ObjectMeta: metav1.ObjectMeta{Name: vmcpServerName, Namespace: testNamespace}},
			&mcpv1beta1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: stableBackend, Namespace: testNamespace}},
			&mcpv1beta1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: unstableBackend, Namespace: testNamespace}},
			&mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace}},
		} {
			if err := k8sClient.Delete(ctx, obj); err != nil {
				GinkgoWriter.Printf("cleanup: failed to delete %T %s: %v\n", obj, obj.GetName(), err)
			}
		}
	})

	It("notifies a connected session when a backend recovers, without reconnect", func() {
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
			// The backend disappearing from discovery entirely also means it is
			// out of the advertised catalog.
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Initializing a Legacy session while the backend is down")
		rawClient, err := e2e.NewRawMCPClient(30 * time.Second)
		Expect(err).ToNot(HaveOccurred())
		vmcpURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

		var sessionID string
		// Session initialization races with the health flip settling into the
		// aggregated view; retry until the session's initial tool list reflects
		// the broken backend's absence. Each failed attempt may leave a session
		// behind on the server; those expire via the server's session TTL.
		Eventually(func() error {
			sessionID, err = legacySessionInit(rawClient, vmcpURL, "health-listchanged-e2e", nil)
			if err != nil {
				return err
			}
			names, err := legacySessionListTools(rawClient, vmcpURL, sessionID, nil)
			if err != nil {
				return err
			}
			if !slices.Contains(names, stableTool) {
				return fmt.Errorf("stable tool %s missing from initial list: %v", stableTool, names)
			}
			if slices.Contains(names, unstableTool) {
				return fmt.Errorf("unstable tool %s unexpectedly present in initial list: %v", unstableTool, names)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())
		GinkgoWriter.Printf("✓ Session %s initialized without %s\n", sessionID, unstableTool)

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

		By("Asserting the connected session receives notifications/tools/list_changed")
		Eventually(notified, timeout).Should(Receive(),
			"the already-connected session must be notified when the backend recovers")
		GinkgoWriter.Printf("✓ Session %s received tools/list_changed without reconnecting\n", sessionID)

		By("Asserting the same session now lists the recovered backend's tool")
		Eventually(func() error {
			names, err := legacySessionListTools(rawClient, vmcpURL, sessionID, nil)
			if err != nil {
				return err
			}
			if !slices.Contains(names, unstableTool) {
				return fmt.Errorf("recovered tool %s not yet in list: %v", unstableTool, names)
			}
			if !slices.Contains(names, stableTool) {
				return fmt.Errorf("stable tool %s missing after resync: %v", stableTool, names)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())

		By("Calling the recovered backend's tool on the same session")
		Eventually(func() error {
			resp, err := legacySessionCallTool(rawClient, vmcpURL, sessionID, unstableTool,
				map[string]any{"input": "recoveredhello123"}, nil)
			if err != nil {
				return err
			}
			// Empty resultType is what a Legacy client's envelope carries.
			return dualEraEchoErr(resp, "recoveredhello123", "")
		}, timeout, pollingInterval).Should(Succeed())
		GinkgoWriter.Printf("✓ Called %s on session %s without reconnecting\n", unstableTool, sessionID)
	})
})

// watchSSEForNotification opens the Legacy session's standalone GET SSE stream
// and forwards a signal for every SSE line mentioning method. The stream (and
// its reader goroutine) lives until ctx is cancelled or the server closes it;
// the returned channel is buffered so a burst of notifications never blocks
// the reader.
func watchSSEForNotification(
	ctx context.Context, url, sessionID, method string,
) (<-chan struct{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(e2e.HeaderMCPSessionID, sessionID)
	req.Header.Set(e2e.HeaderMCPProtocolVersion, coremcp.MCPVersionLegacy)

	// No client timeout: the stream is long-lived and bounded by ctx.
	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("open SSE stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("SSE stream: unexpected status %d", resp.StatusCode)
	}

	ch := make(chan struct{}, 16)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, method) {
				GinkgoWriter.Printf("SSE stream (session %s): %s\n", sessionID, line)
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()
	return ch, nil
}
