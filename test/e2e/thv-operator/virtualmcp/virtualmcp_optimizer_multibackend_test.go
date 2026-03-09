// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcp "github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("VirtualMCPServer Optimizer Multi-Backend", Ordered, func() {
	var (
		testNamespace       = "default"
		mcpGroupName        = "test-optmulti-group"
		vmcpServerName      = "test-vmcp-optmulti"
		embeddingName       = "test-optmulti-embedding"
		backend1Name        = "backend-optmulti-yardstick"
		backend2Name        = "backend-optmulti-fetch"
		backend3Name        = "backend-optmulti-osv"
		backend4Name        = "backend-optmulti-github"
		backend5Name        = "backend-optmulti-terraform"
		backend6Name        = "backend-optmulti-playwright"
		backend7Name        = "backend-optmulti-puppeteer"
		backend8Name        = "backend-optmulti-memory"
		backend9Name        = "backend-optmulti-everything"
		backend10Name       = "backend-optmulti-ida-pro-mcp"
		backend11Name       = "backend-optmulti-pagerduty"
		githubSecretName    = "optmulti-github-token"
		pagerdutySecretName = "optmulti-pagerduty-token"
		timeout             = 5 * time.Minute
		pollingInterval     = 1 * time.Second
		vmcpNodePort        int32
	)

	// allBackends defines all backend configurations used in the test.
	// These match the quickstart example: examples/operator/virtual-mcps/vmcp_optimizer_quickstart.yaml
	//
	//   Backend      | Description                        | Tools
	//   -------------|------------------------------------|---------
	//   yardstick    | Unit conversion                    |     1
	//   fetch        | URL content fetching               |     1
	//   github       | GitHub API                         |    41
	//   memory       | Knowledge graph persistent memory  |     9
	//   puppeteer    | Browser automation / web scraping  |     7
	//   osv          | OSV vulnerability database         |     3
	//   terraform    | Terraform registry & workspaces    |     9
	//   playwright   | Browser automation & testing       |    22
	//   everything   | MCP reference/test server          |     8
	//   ida-pro-mcp  | IDA Pro reverse engineering        |    47
	//   pagerduty    | PagerDuty incident management      |    64
	//   -------------|------------------------------------|---------
	//   Total        |                                    |   212
	allBackends := []BackendConfig{
		{
			Name: backend1Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.YardstickServerImage, // 1 tool
			Transport: "streamable-http",
		},
		{
			Name: backend2Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.GofetchServerImage, // 1 tool
			Transport: "streamable-http",
		},
		{
			Name: backend3Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.OSVMCPServerImage, // 3 tools
			Transport: "streamable-http",
		},
		{
			Name: backend4Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.GitHubMCPServerImage, // 41 tools
			Transport: "stdio",
			Secrets: []mcpv1alpha1.SecretRef{
				{Name: githubSecretName, Key: "token", TargetEnvName: "GITHUB_PERSONAL_ACCESS_TOKEN"},
			},
		},
		{
			Name: backend5Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.TerraformMCPServerImage, // 9 tools
			Transport: "streamable-http",
			Env: []mcpv1alpha1.EnvVar{
				{Name: "TRANSPORT_MODE", Value: "streamable-http"},
				{Name: "TRANSPORT_HOST", Value: "0.0.0.0"},
			},
		},
		{
			Name: backend6Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.PlaywrightMCPServerImage, // 22 tools
			Transport: "stdio",
		},
		{
			Name: backend7Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.PuppeteerMCPServerImage, // 7 tools
			Transport: "stdio",
		},
		{
			Name: backend8Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.MemoryMCPServerImage, // 9 tools
			Transport: "stdio",
		},
		{
			Name: backend9Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.EverythingMCPServerImage, // 8 tools
			Transport: "stdio",
		},
		{
			Name: backend10Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.IDAProMCPServerImage, // 47 tools
			Transport: "stdio",
		},
		{
			Name: backend11Name, Namespace: testNamespace, GroupRef: mcpGroupName,
			Image:     images.PagerDutyMCPServerImage, // 64 tools
			Transport: "stdio",
			Secrets: []mcpv1alpha1.SecretRef{
				{Name: pagerdutySecretName, Key: "token", TargetEnvName: "PAGERDUTY_USER_API_KEY"},
			},
		},
	}

	BeforeAll(func() {
		By("Creating MCPGroup for optimizer multi-backend test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for optimizer multi-backend E2E tests", timeout, pollingInterval)

		By("Creating Secret for GitHub MCP server token")
		githubSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      githubSecretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"token": "ghp_fake_token_for_testing",
			},
		}
		Expect(k8sClient.Create(ctx, githubSecret)).To(Succeed())

		By("Creating Secret for PagerDuty MCP server token")
		pagerdutySecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pagerdutySecretName,
				Namespace: testNamespace,
			},
			StringData: map[string]string{
				"token": "fake_pagerduty_token_for_testing",
			},
		}
		Expect(k8sClient.Create(ctx, pagerdutySecret)).To(Succeed())

		By("Creating all backend MCPServers in parallel")
		CreateMultipleMCPServersInParallel(ctx, k8sClient, allBackends, timeout, pollingInterval)

		By("Creating EmbeddingServer for optimizer multi-backend")
		embeddingServer := &mcpv1alpha1.EmbeddingServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      embeddingName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.EmbeddingServerSpec{
				Model: "BAAI/bge-small-en-v1.5",
				Image: images.TextEmbeddingsInferenceImage,
			},
		}
		Expect(k8sClient.Create(ctx, embeddingServer)).To(Succeed())

		By("Creating VirtualMCPServer with optimizer enabled and prefix aggregation")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				ServiceType: "NodePort",
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
					Source: "discovered",
				},
				EmbeddingServerRef: &mcpv1alpha1.EmbeddingServerRef{
					Name: embeddingName,
				},
				Config: vmcpconfig.Config{
					Group:     mcpGroupName,
					Optimizer: &vmcpconfig.OptimizerConfig{},
					Aggregation: &vmcpconfig.AggregationConfig{
						ConflictResolution: vmcp.ConflictStrategyPrefix,
						ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
							PrefixFormat: "{workload}_",
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting VirtualMCPServer NodePort")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)
		_, _ = fmt.Fprintf(GinkgoWriter, "VirtualMCPServer is accessible at NodePort: %d\n", vmcpNodePort)
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      vmcpServerName,
			Namespace: testNamespace,
		}, vmcpServer); err == nil {
			_ = k8sClient.Delete(ctx, vmcpServer)
		}

		By("Cleaning up EmbeddingServer")
		es := &mcpv1alpha1.EmbeddingServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      embeddingName,
			Namespace: testNamespace,
		}, es); err == nil {
			_ = k8sClient.Delete(ctx, es)
		}

		By("Cleaning up backend MCPServers")
		for _, backend := range allBackends {
			server := &mcpv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backend.Name,
				Namespace: testNamespace,
			}, server); err == nil {
				_ = k8sClient.Delete(ctx, server)
			}
		}

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      mcpGroupName,
			Namespace: testNamespace,
		}, mcpGroup); err == nil {
			_ = k8sClient.Delete(ctx, mcpGroup)
		}

		By("Cleaning up GitHub token Secret")
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: githubSecretName, Namespace: testNamespace},
		})

		By("Cleaning up PagerDuty token Secret")
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: pagerdutySecretName, Namespace: testNamespace},
		})
	})

	It("should only expose find_tool and call_tool", func() {
		By("Creating and initializing MCP client")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "optmulti-test-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		By("Listing tools from VirtualMCPServer")
		listRequest := mcp.ListToolsRequest{}
		tools, err := mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying only optimizer tools are exposed")
		Expect(tools.Tools).To(HaveLen(2), "Should only have find_tool and call_tool")

		toolNames := make([]string, len(tools.Tools))
		for i, tool := range tools.Tools {
			toolNames[i] = tool.Name
		}
		Expect(toolNames).To(ConsistOf("find_tool", "call_tool"))

		_, _ = fmt.Fprintf(GinkgoWriter, "✓ Optimizer mode correctly exposes only: %v\n", toolNames)
	})

	It("should complete cold-start find_tool request under 5 seconds", func() {
		By("Creating and initializing MCP client for cold-start latency test")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "optmulti-coldstart-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		// This is the first find_tool request after the vMCP server is ready.
		// No cached embeddings exist yet, so the optimizer must generate embeddings
		// for all tools on-demand and perform similarity search — a true cold start.
		By("Timing the first find_tool request (cold start, no cached embeddings)")
		start := time.Now()
		result, err := callFindTool(mcpClient, "echo back a message")
		elapsed := time.Since(start)
		Expect(err).ToNot(HaveOccurred())

		tools := getToolNames(result)
		Expect(tools).ToNot(BeEmpty(), "Cold-start find_tool should return results")
		_, _ = fmt.Fprintf(GinkgoWriter, "Cold-start find_tool latency: %s (tools returned: %v)\n", elapsed, tools)

		By("Asserting cold-start latency is under 5 seconds")
		Expect(elapsed).To(BeNumerically("<", 5*time.Second),
			"Cold-start find_tool request took %s, expected < 5s", elapsed)
	})

	It("should return semantically relevant results (search quality)", func() {
		By("Creating and initializing MCP client for search quality test")
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "optmulti-quality-client", 30*time.Second)
		Expect(err).ToNot(HaveOccurred())
		defer mcpClient.Close()

		// Each test case searches with a natural-language description and verifies
		// that the top results are semantically appropriate (not random tools).
		type qualityCase struct {
			query       string
			expectMatch string // substring expected in at least one returned tool name
			backend     string // which backend should contribute the match
		}

		cases := []qualityCase{
			{
				query:       "repeat or echo back a message",
				expectMatch: "echo",
				backend:     "yardstick",
			},
			{
				query:       "retrieve content from a web page or URL",
				expectMatch: "fetch",
				backend:     "gofetch",
			},
			{
				query:       "check security vulnerabilities in open source packages",
				expectMatch: "vulnerability",
				backend:     "osv",
			},
			{
				query:       "create a pull request on a code repository",
				expectMatch: "pull_request",
				backend:     "github",
			},
		}

		for _, tc := range cases {
			By(fmt.Sprintf("Searching for '%s' (expecting match from %s backend)", tc.query, tc.backend))
			result, err := callFindTool(mcpClient, tc.query)
			Expect(err).ToNot(HaveOccurred())

			tools := getToolNames(result)
			Expect(tools).ToNot(BeEmpty(), "find_tool should return results for query: %s", tc.query)

			hasMatch := false
			for _, name := range tools {
				if strings.Contains(strings.ToLower(name), tc.expectMatch) {
					hasMatch = true
					break
				}
			}
			Expect(hasMatch).To(BeTrue(),
				"Query '%s' should return a tool containing '%s' from %s backend, got: %v",
				tc.query, tc.expectMatch, tc.backend, tools)
			_, _ = fmt.Fprintf(GinkgoWriter, "✓ Quality check passed for '%s': found '%s' in %v\n",
				tc.query, tc.expectMatch, tools)
		}
	})
})
