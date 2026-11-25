// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	coremcp "github.com/stacklok/toolhive/pkg/mcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This spec closes #5979: unlike virtualmcp_discovered_mode_test.go (Legacy
// backends only, gofetch/osv), it mixes a Legacy backend (gofetch) with a
// Modern (2026-07-28, stateless) backend -- yardstick-server run with
// TRANSPORT=streamable-http, STATELESS=true, BACKEND_MODE=echo, the env
// combination confirmed at acceptance_tests/dual_era_k8s_test.go:151-155 --
// in the same discovered-mode aggregation. That's what actually exercises
// probeRevision/modernEnumerate/dispatch resolving BOTH eras behind one vMCP;
// the existing spec never resolves a Modern revision at all.
//
// OUT OF SCOPE (deliberate): reclassify / mid-run revision-flip coverage.
// #5979 lists it as a stretch goal; it's hard to trigger deterministically
// against a live backend, and pkg/vmcp/client/reclassify_test.go already
// covers it deterministically as a unit test.
//
// mcpRevision is only observable through the LIVE health monitor state
// (pkg/vmcp/health/status.go), on both surfaces this spec checks
// (VirtualMCPServer.status.discoveredBackends[] and the /status endpoint).
// Health monitoring is NOT the operator's default -- it's gated on
// Operational.FailureHandling.HealthCheckInterval > 0 (pkg/vmcp/cli/serve.go)
// -- so the VirtualMCPServer config below sets FailureHandling explicitly;
// without it both mcpRevision assertions would time out empty.
var _ = Describe("VirtualMCPServer Dual-Era Backends", Ordered, func() {
	var (
		testNamespace   = "default"
		mcpGroupName    = "test-dual-era-group"
		vmcpServerName  = "test-vmcp-dual-era"
		legacyBackend   = "backend-dual-era-legacy"
		modernBackend   = "backend-dual-era-modern"
		echoToolName    = "echo"
		timeout         = 3 * time.Minute
		pollingInterval = 2 * time.Second
		vmcpNodePort    int32
	)

	// assertRevisions checks that got[modernBackend] and got[legacyBackend] hold
	// the expected MCP revision, returning a descriptive error otherwise. Shared
	// by the CRD-status and /status assertions below, which differ only in how
	// they build the map.
	assertRevisions := func(got map[string]string) error {
		if rev := got[modernBackend]; rev != coremcp.MCPVersionModern {
			return fmt.Errorf("modern backend %s: want revision %q, got %q (all: %v)",
				modernBackend, coremcp.MCPVersionModern, rev, got)
		}
		if rev := got[legacyBackend]; rev != coremcp.MCPVersionLegacy {
			return fmt.Errorf("legacy backend %s: want revision %q, got %q (all: %v)",
				legacyBackend, coremcp.MCPVersionLegacy, rev, got)
		}
		return nil
	}

	BeforeAll(func() {
		By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for VirtualMCP dual-era backend E2E tests", timeout, pollingInterval)

		By("Creating Legacy (gofetch) and Modern (yardstick, stateless echo) backends in parallel")
		CreateMultipleMCPServersInParallel(ctx, k8sClient, []BackendConfig{
			{
				Name:      legacyBackend,
				Namespace: testNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.GofetchServerImage,
			},
			{
				Name:      modernBackend,
				Namespace: testNamespace,
				GroupRef:  mcpGroupName,
				// Same image as the Legacy backends above: since #6004 it is a single
				// go-sdk v1.7 build serving both eras, and STATELESS is what decides
				// which. In session mode it negotiates down and vMCP classifies it
				// Legacy from server/discover's supportedVersions, so a forgotten
				// STATELESS here would silently produce an all-Legacy backend set --
				// which is why the mcpRevision assertions below check both eras.
				Image: images.YardstickServerImage,
				Env: []mcpv1beta1.EnvVar{
					{Name: "STATELESS", Value: "true"}, // Modern (2026-07-28) capable backend
					{Name: "BACKEND_MODE", Value: "echo"},
				},
			},
		}, timeout, pollingInterval)

		By("Creating VirtualMCPServer in discovered mode over the mixed backend set")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, testNamespace,
			v1beta1test.WithVMCPGroupRef(mcpGroupName),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{
				Group: mcpGroupName,
				Aggregation: &vmcpconfig.AggregationConfig{
					ConflictResolution: "prefix", // avoid tool-name collisions across backends
				},
				// Health monitoring is what resolves and caches MCPRevision; it is
				// opt-in (see the header comment), so it must be turned on explicitly
				// for the mcpRevision assertions below to ever see a non-empty value.
				Operational: &vmcpconfig.OperationalConfig{
					FailureHandling: &vmcpconfig.FailureHandlingConfig{
						HealthCheckInterval: vmcpconfig.Duration(10 * time.Second),
						// Must be set explicitly and be strictly LESS than the interval.
						// Supplying the failureHandling object at all makes kubebuilder
						// apply its siblings' CRD defaults, and healthCheckTimeout
						// defaults to 10s -- equal to the interval above, which
						// pkg/vmcp/config/validator.go rejects ("must be less than
						// healthCheckInterval to prevent checks from queuing up"). The
						// vmcp pod then crash-loops on config validation and the
						// VirtualMCPServer never leaves Pending, so every assertion here
						// fails on a readiness timeout with no hint of the real cause.
						HealthCheckTimeout:      vmcpconfig.Duration(2 * time.Second),
						UnhealthyThreshold:      3,
						StatusReportingInterval: vmcpconfig.Duration(5 * time.Second),
					},
				},
			}),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
			}),
			v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
				v.Spec.ServiceType = "NodePort"
			}),
		)
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Waiting for VirtualMCPServer to discover both backends")
		WaitForCondition(ctx, k8sClient, vmcpServerName, testNamespace, "BackendsDiscovered", "True", timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, testNamespace)
		if err := k8sClient.Delete(ctx, vmcpServer); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete VirtualMCPServer: %v\n", err)
		}

		By("Cleaning up backend MCPServers")
		for _, name := range []string{legacyBackend, modernBackend} {
			backend := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			}
			if err := k8sClient.Delete(ctx, backend); err != nil {
				GinkgoWriter.Printf("Warning: failed to delete backend %s: %v\n", name, err)
			}
		}

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: testNamespace},
		}
		if err := k8sClient.Delete(ctx, mcpGroup); err != nil {
			GinkgoWriter.Printf("Warning: failed to delete MCPGroup: %v\n", err)
		}
	})

	Context("when aggregating a mixed Legacy+Modern backend set", func() {
		It("aggregates tools from both the Legacy and Modern backends", func() {
			// This is the real coverage gap the discovered-mode test leaves open:
			// it only exercises Legacy backends, so it never resolves a Modern
			// revision or calls modernEnumerate. Retrying with fresh sessions
			// (WaitForExpectedTools) avoids flaking on first-probe races.
			WaitForExpectedTools(vmcpNodePort, "toolhive-e2e-dual-era-list", func(tools []mcp.Tool) error {
				var hasFetch, hasEcho bool
				for _, t := range tools {
					if strings.Contains(t.Name, fetchToolName) {
						hasFetch = true
					}
					if strings.Contains(t.Name, echoToolName) {
						hasEcho = true
					}
				}
				if !hasFetch {
					return fmt.Errorf("missing Legacy backend's %q tool in aggregated list: %v", fetchToolName, toolNames(tools))
				}
				if !hasEcho {
					return fmt.Errorf("missing Modern backend's %q tool in aggregated list: %v", echoToolName, toolNames(tools))
				}
				return nil
			}, timeout)
		})

		It("routes a tool call to the Modern backend and it succeeds", func() {
			// Covers dispatch's Modern call path with real traffic, not just a
			// readiness/count check (per .claude/rules/testing.md's E2E Test
			// Coverage rule).
			TestToolListingAndCall(vmcpNodePort, "toolhive-e2e-dual-era-call", echoToolName, "dual-era-modern-check")
		})
	})

	Context("when surfacing per-backend MCP revision", func() {
		It("surfaces the correct mcpRevision in VirtualMCPServer.status.discoveredBackends for each backend", func() {
			Eventually(func() error {
				status, err := GetVirtualMCPServerStatus(ctx, k8sClient, vmcpServerName, testNamespace)
				if err != nil {
					return fmt.Errorf("failed to get VirtualMCPServer status: %w", err)
				}

				got := make(map[string]string, len(status.DiscoveredBackends))
				for _, b := range status.DiscoveredBackends {
					got[b.Name] = b.MCPRevision
				}
				return assertRevisions(got)
			}, timeout, pollingInterval).Should(Succeed())
		})

		It("surfaces the correct mcp_revision in the /status endpoint's backends[] for each backend", func() {
			Eventually(func() error {
				status, err := GetVMCPStatus(vmcpNodePort)
				if err != nil {
					return fmt.Errorf("failed to get /status: %w", err)
				}

				got := make(map[string]string, len(status.Backends))
				for _, b := range status.Backends {
					got[b.Name] = b.MCPRevision
				}
				return assertRevisions(got)
			}, timeout, pollingInterval).Should(Succeed())
		})
	})
})
