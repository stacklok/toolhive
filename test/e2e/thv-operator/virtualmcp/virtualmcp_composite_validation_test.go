// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// Regression Test: There was previously an issue where the validation code did not
// recognize built-in template functions like fromJson and index. This test ensures
// that the validation code recognizes these functions and that the VirtualMCPServer
// starts successfully.
var _ = Describe("VirtualMCPServer Composite Tool Template Functions", Ordered, func() {
	var (
		testNamespace        = "default"
		mcpGroupName         = "test-template-funcs-group"
		vmcpServerName       = "test-vmcp-fromjson"
		backendName          = "yardstick-template-funcs"
		compositeToolDefName = "fromjson-template-definition"
		timeout              = 3 * time.Minute
		pollingInterval      = 1 * time.Second
		vmcpNodePort         int32
	)

	BeforeAll(func() {
		By("Creating MCPGroup for template functions test")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, testNamespace,
			"Test MCP Group for template functions E2E tests", timeout, pollingInterval)

		By("Creating yardstick backend MCPServer")
		CreateMCPServerAndWait(ctx, k8sClient, backendName, testNamespace, mcpGroupName,
			images.YardstickServerImage, timeout, pollingInterval)

		By("Creating VirtualMCPCompositeToolDefinition with fromJson template function")
		compositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      compositeToolDefName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
				CompositeToolConfig: vmcpconfig.CompositeToolConfig{
					Name:        "parse_json_workflow",
					Description: "Workflow that parses JSON text responses using fromJson",
					Parameters: thvjson.NewMap(map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{
								"type":        "string",
								"description": "Search query",
							},
						},
						"required": []any{"query"},
					}),
					Steps: []vmcpconfig.WorkflowStepConfig{
						{
							ID:   "search",
							Type: "tool",
							Tool: fmt.Sprintf("%s.echo", backendName),
							Arguments: thvjson.NewMap(map[string]any{
								"input": "{{.params.query}}",
							}),
						},
						{
							ID:        "process",
							Type:      "tool",
							Tool:      fmt.Sprintf("%s.echo", backendName),
							DependsOn: []string{"search"},
							// This uses fromJson and index - template functions that must be
							// registered in the validator's templateFuncMap
							Arguments: thvjson.NewMap(map[string]any{
								"input": "{{(index (fromJson .steps.search.output.text).items 0).id}}",
							}),
						},
					},
					Timeout: vmcpconfig.Duration(30 * time.Second),
				},
			},
		}
		Expect(k8sClient.Create(ctx, compositeToolDef)).To(Succeed())

		By("Verifying VirtualMCPCompositeToolDefinition was created")
		Eventually(func() bool {
			def := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      compositeToolDefName,
				Namespace: testNamespace,
			}, def)
			return err == nil
		}, 30*time.Second, pollingInterval).Should(BeTrue(), "VirtualMCPCompositeToolDefinition should exist")

		By("Creating VirtualMCPServer with referenced composite tool")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				Config: vmcpconfig.Config{
					Group: mcpGroupName,
					Aggregation: &vmcpconfig.AggregationConfig{
						ConflictResolution: "prefix",
					},
					CompositeToolRefs: []vmcpconfig.CompositeToolRef{
						{
							Name: compositeToolDefName,
						},
					},
				},
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type: "anonymous",
				},
				ServiceType: "NodePort",
			},
		}
		Expect(k8sClient.Create(ctx, vmcpServer)).To(Succeed())

		By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By("Getting NodePort for VirtualMCPServer")
		vmcpNodePort = GetVMCPNodePort(ctx, k8sClient, vmcpServerName, testNamespace, timeout, pollingInterval)

		By(fmt.Sprintf("VirtualMCPServer accessible at http://localhost:%d", vmcpNodePort))
	})

	AfterAll(func() {
		By("Cleaning up VirtualMCPServer")
		vmcpServer := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, vmcpServer)

		By("Cleaning up VirtualMCPCompositeToolDefinition")
		compositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      compositeToolDefName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, compositeToolDef)

		By("Cleaning up backend MCPServer")
		backend := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, backend)

		By("Cleaning up MCPGroup")
		mcpGroup := &mcpv1alpha1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpGroupName,
				Namespace: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, mcpGroup)
	})

	Context("when composite tool uses fromJson template function", func() {
		It("should expose the composite tool in tool listing", func() {
			By("Creating and initializing MCP client for VirtualMCPServer")
			mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, "toolhive-fromjson-test", 30*time.Second)
			Expect(err).ToNot(HaveOccurred())
			defer mcpClient.Close()

			By("Listing tools from VirtualMCPServer")
			tools := TestToolListing(vmcpNodePort, "toolhive-fromjson-test")

			// Should find the composite tool that uses fromJson
			var foundComposite bool
			for _, tool := range tools {
				if tool.Name == "parse_json_workflow" {
					foundComposite = true
					Expect(tool.Description).To(Equal("Workflow that parses JSON text responses using fromJson"))
					break
				}
			}
			Expect(foundComposite).To(BeTrue(), "Should find composite tool: parse_json_workflow")
		})

		It("should have VirtualMCPServer in Ready phase", func() {
			vmcp := &mcpv1alpha1.VirtualMCPServer{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpServerName,
				Namespace: testNamespace,
			}, vmcp)
			Expect(err).ToNot(HaveOccurred())

			Expect(vmcp.Status.Phase).To(Equal(mcpv1alpha1.VirtualMCPServerPhaseReady),
				"VirtualMCPServer should be Ready - if not, the validator may not recognize fromJson")
		})
	})
})
