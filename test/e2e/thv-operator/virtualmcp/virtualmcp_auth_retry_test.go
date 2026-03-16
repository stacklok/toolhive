// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// persistent401BackendScript is an inline Python HTTP server that returns
// HTTP 401 Unauthorized for every request on port 8080.
// It simulates a backend whose credentials are permanently invalid, letting us
// verify that retryingBackendClient exhausts maxAuthRetries (3) and surfaces
// ErrAuthenticationFailed → BackendUnauthenticated → BackendStatusUnavailable.
//
// ThreadingMixIn + HTTPServer is used instead of bare TCPServer so that:
//   - Concurrent connections from the ToolHive proxy are handled in separate threads
//   - BrokenPipeError from abruptly-closed connections does not crash the process
//   - allow_reuse_address avoids "Address already in use" on pod restart
const persistent401BackendScript = `import http.server,socketserver
class H(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  try:self.send_response(401);self.end_headers()
  except Exception:pass
 do_POST=do_PUT=do_DELETE=do_PATCH=do_HEAD=do_OPTIONS=do_GET
 def log_message(self,*a):pass
 def handle_error(self,r,a):pass
class S(socketserver.ThreadingMixIn,http.server.HTTPServer):
 allow_reuse_address=True
 daemon_threads=True
S(('',8080),H).serve_forever()`

// build401PodTemplateSpec returns a PodTemplateSpec patch that replaces the
// default HTTP readiness probe on the "mcp" container with a TCP socket probe.
// Without this, the runner's HTTP GET /health probe would receive 401 and the
// container would never become Ready.
func build401PodTemplateSpec() *runtime.RawExtension {
	podTemplateSpec := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "mcp",
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{
								Port: intstr.FromInt(8080),
							},
						},
						InitialDelaySeconds: 2,
						PeriodSeconds:       2,
						TimeoutSeconds:      5,
						FailureThreshold:    10,
					},
				},
			},
		},
	}
	raw, err := json.Marshal(podTemplateSpec)
	Expect(err).ToNot(HaveOccurred(), "should marshal PodTemplateSpec to JSON")
	return &runtime.RawExtension{Raw: raw}
}

// TestAuthRetry_PersistentUnauthorized_BackendMarkedUnauthenticated verifies the
// end-to-end auth-retry pipeline in a live Kubernetes cluster:
//
//  1. A backend MCPServer runs a Python HTTP server returning 401 for every request.
//  2. retryingBackendClient intercepts the 401, retries up to maxAuthRetries (3)
//     times with exponential back-off, then returns ErrAuthenticationFailed.
//  3. The health monitor maps this to BackendUnauthenticated → BackendStatusUnavailable.
//  4. A co-located healthy backend (yardstick) stays Ready throughout.
var _ = Describe("VirtualMCPServer Auth Retry Exhaustion", Ordered, func() {
	var (
		testNamespace  = "default"
		mcpGroupName   = "test-auth-retry-group"
		vmcpServerName = "test-vmcp-auth-retry"
		stableBackend  = "backend-auth-stable"
		failingBackend = "backend-auth-failing-401"
		timeout        = 3 * time.Minute
		pollInterval   = 2 * time.Second
	)

	BeforeAll(func() {
		By("Creating MCPGroup for auth retry tests")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for auth retry E2E tests", timeout, pollInterval)

		By("Creating stable and persistent-401 backend MCPServers")
		CreateMultipleMCPServersInParallel(ctx, k8sClient, []BackendConfig{
			{
				Name:      stableBackend,
				Namespace: testNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.YardstickServerImage,
			},
			{
				Name:      failingBackend,
				Namespace: testNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.PythonImage,
				// Pass the inline 401 server script to the Python interpreter.
				Args: []string{"-c", persistent401BackendScript},
				// Replace the default HTTP readiness probe with a TCP one so
				// the container becomes Ready as soon as port 8080 is open,
				// regardless of the HTTP 401 responses it serves.
				PodTemplateSpec: build401PodTemplateSpec(),
				// The operator will mark this MCPServer as Failed because every
				// MCP request returns 401. Skip the readiness gate so BeforeAll
				// does not stop-trying on the expected Failed phase.
				SkipReadinessWait: true,
			},
		}, timeout, pollInterval)

		By("Creating VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
				},
				ServiceType: "NodePort",
				Config: vmcpconfig.Config{
					Name:  vmcpServerName,
					Group: mcpGroupName,
					Aggregation: &vmcpconfig.AggregationConfig{
						ConflictResolution: "prefix",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer pod to be running")
		// The VirtualMCPServer will be Degraded (not Ready) because one backend always
		// returns 401. Wait for the pod itself to be up so health-checking can proceed.
		WaitForVirtualMCPServerPod(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollInterval)
	})

	AfterAll(func() {
		By("Cleaning up auth retry test resources")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer); err == nil {
			Expect(k8sClient.Delete(ctx, vmcpServer)).To(Succeed())
		}

		for _, name := range []string{stableBackend, failingBackend} {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: testNamespace,
			}, server); err == nil {
				Expect(k8sClient.Delete(ctx, server)).To(Succeed())
			}
		}

		group := &mcpv1alpha1.MCPGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      mcpGroupName,
			Namespace: testNamespace,
		}, group); err == nil {
			Expect(k8sClient.Delete(ctx, group)).To(Succeed())
		}
	})

	It("should mark the 401 backend as unavailable after auth retries are exhausted", func() {
		// retryingBackendClient retries ListCapabilities up to maxAuthRetries (3)
		// times. After exhaustion it returns ErrAuthenticationFailed, which the
		// health monitor maps to BackendUnauthenticated → "unavailable" in the CRD.
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}

			var failingB *mcpv1alpha1.DiscoveredBackend
			for i := range vmcpServer.Status.DiscoveredBackends {
				if vmcpServer.Status.DiscoveredBackends[i].Name == failingBackend {
					failingB = &vmcpServer.Status.DiscoveredBackends[i]
					break
				}
			}
			if failingB == nil {
				return fmt.Errorf("401 backend %q not yet in discovered backends", failingBackend)
			}
			if failingB.Status != mcpv1alpha1.BackendStatusUnavailable {
				return fmt.Errorf("expected status %q, got %q (message: %s)",
					mcpv1alpha1.BackendStatusUnavailable, failingB.Status, failingB.Message)
			}

			GinkgoWriter.Printf("✓ 401 backend unavailable (status: %s, message: %s)\n",
				failingB.Status, failingB.Message)
			return nil
		}, timeout, pollInterval).Should(Succeed())
	})

	It("should transition VirtualMCPServer to Degraded when a backend is unavailable", func() {
		Eventually(func() error {
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcpServer); err != nil {
				return err
			}
			if vmcpServer.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseDegraded &&
				vmcpServer.Status.Phase != mcpv1alpha1.VirtualMCPServerPhaseFailed {
				return fmt.Errorf("expected phase Degraded or Failed, got: %s",
					vmcpServer.Status.Phase)
			}
			GinkgoWriter.Printf("✓ VirtualMCPServer phase: %s\n", vmcpServer.Status.Phase)
			return nil
		}, timeout, pollInterval).Should(Succeed())
	})

	It("should keep the stable backend ready throughout the auth failure", func() {
		// Auth failures are isolated per-backend via the per-backend circuit breaker.
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer)).To(Succeed())

		var stableB *mcpv1alpha1.DiscoveredBackend
		for i := range vmcpServer.Status.DiscoveredBackends {
			if vmcpServer.Status.DiscoveredBackends[i].Name == stableBackend {
				stableB = &vmcpServer.Status.DiscoveredBackends[i]
				break
			}
		}
		Expect(stableB).NotTo(BeNil(), "stable backend should be in discovered backends list")
		Expect(stableB.Status).To(Or(
			Equal(mcpv1alpha1.BackendStatusReady),
			Equal(mcpv1alpha1.BackendStatusDegraded)),
			"stable backend should remain healthy; got status=%s message=%s",
			stableB.Status, stableB.Message)

		GinkgoWriter.Printf("✓ Stable backend remained healthy: status=%s\n", stableB.Status)
	})

	It("should report the 401 backend as unavailable in /api/backends/health", func() {
		nodePort := GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollInterval)

		Eventually(func() error {
			bh, err := GetVMCPBackendsHealth(nodePort)
			if err != nil {
				return fmt.Errorf("GET /api/backends/health: %w", err)
			}
			if !bh.MonitoringEnabled {
				return fmt.Errorf("monitoring not enabled")
			}
			state, found := bh.Backends[failingBackend]
			if !found {
				return fmt.Errorf("401 backend %q not found in /api/backends/health", failingBackend)
			}
			if state.Status == backendHealthStatusHealthy {
				return fmt.Errorf("401 backend still reported as healthy")
			}
			GinkgoWriter.Printf("✓ /api/backends/health: %s → %s\n", failingBackend, state.Status)
			return nil
		}, timeout, pollInterval).Should(Succeed())
	})
})
