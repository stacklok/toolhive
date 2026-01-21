// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer Elicitation Integration Tests", func() {
	const (
		timeout          = time.Second * 30
		interval         = time.Millisecond * 250
		defaultNamespace = "default"
	)

	Context("When a VirtualMCPServer has composite tools with elicitation steps", Ordered, func() {
		var (
			namespace            string
			vmcpName             string
			mcpGroupName         string
			compositeToolDefName string
			vmcp                 *mcpv1alpha1.VirtualMCPServer
			mcpGroup             *mcpv1alpha1.MCPGroup
			compositeToolDef     *mcpv1alpha1.VirtualMCPCompositeToolDefinition
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			vmcpName = "test-vmcp-elicitation"
			mcpGroupName = "test-group-elicitation"
			compositeToolDefName = "test-elicitation-tool"

			// Create MCPGroup first (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for elicitation integration",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for MCPGroup to be ready
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Create VirtualMCPCompositeToolDefinition with elicitation steps
			compositeToolDef = &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: vmcpconfig.CompositeToolConfig{
						Name:        "interactive_workflow",
						Description: "Workflow with user interactions via elicitations",
						Timeout:     vmcpconfig.Duration(15 * time.Minute),
						Steps: []vmcpconfig.WorkflowStepConfig{
							// Step 1: Tool call
							{
								ID:      "prepare",
								Type:    mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool:    "echo",
								Timeout: vmcpconfig.Duration(1 * time.Minute),
							},
							// Step 2: Elicitation with OnDecline and OnCancel handlers
							{
								ID:        "confirm_deploy",
								Type:      mcpv1alpha1.WorkflowStepTypeElicitation,
								Message:   "Proceed with deployment?",
								Schema:    thvjson.NewMap(map[string]any{"type": "object", "properties": map[string]any{"proceed": map[string]any{"type": "boolean"}}}),
								DependsOn: []string{"prepare"},
								Timeout:   vmcpconfig.Duration(5 * time.Minute),
								OnDecline: &vmcpconfig.ElicitationResponseConfig{
									Action: "skip_remaining",
								},
								OnCancel: &vmcpconfig.ElicitationResponseConfig{
									Action: "abort",
								},
							},
							// Step 3: Another elicitation with different handlers
							{
								ID:        "select_env",
								Type:      mcpv1alpha1.WorkflowStepTypeElicitation,
								Message:   "Select target environment",
								Schema:    thvjson.NewMap(map[string]any{"type": "object", "properties": map[string]any{"environment": map[string]any{"type": "string", "enum": []any{"staging", "production"}}}}),
								DependsOn: []string{"confirm_deploy"},
								Timeout:   vmcpconfig.Duration(5 * time.Minute),
								OnDecline: &vmcpconfig.ElicitationResponseConfig{
									Action: "continue",
								},
								OnCancel: &vmcpconfig.ElicitationResponseConfig{
									Action: "abort",
								},
							},
							// Step 4: Final tool call
							{
								ID:        "deploy",
								Type:      mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool:      "deploy_app",
								DependsOn: []string{"select_env"},
								Timeout:   vmcpconfig.Duration(2 * time.Minute),
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, compositeToolDef)).Should(Succeed())

			// Create VirtualMCPServer that references the composite tool definition
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: compositeToolDefName},
						},
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for VirtualMCPServer to reconcile
			Eventually(func() bool {
				updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				if err != nil {
					return false
				}

				// Check for CompositeToolRefsValidated condition to be True
				for _, cond := range updatedVMCP.Status.Conditions {
					if cond.Type == mcpv1alpha1.ConditionTypeCompositeToolRefsValidated {
						return cond.Status == metav1.ConditionTrue &&
							cond.Reason == mcpv1alpha1.ConditionReasonCompositeToolRefsValid
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			// Clean up
			_ = k8sClient.Delete(ctx, compositeToolDef)
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should successfully validate composite tool with elicitation steps", func() {
			updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, updatedVMCP)).Should(Succeed())

			// Verify VirtualMCPServer is in valid state
			Expect(updatedVMCP.Status.ObservedGeneration).To(Equal(updatedVMCP.Generation))
			Expect(updatedVMCP.Status.Phase).To(Or(
				Equal(mcpv1alpha1.VirtualMCPServerPhaseReady),
				Equal(mcpv1alpha1.VirtualMCPServerPhasePending),
			))

			// Verify CompositeToolRefsValidated condition is True
			foundValidatedCondition := false
			for _, cond := range updatedVMCP.Status.Conditions {
				if cond.Type == mcpv1alpha1.ConditionTypeCompositeToolRefsValidated {
					foundValidatedCondition = true
					Expect(cond.Status).To(Equal(metav1.ConditionTrue))
					Expect(cond.Reason).To(Equal(mcpv1alpha1.ConditionReasonCompositeToolRefsValid))
				}
			}
			Expect(foundValidatedCondition).To(BeTrue(), "CompositeToolRefsValidated condition should exist")
		})

		It("Should have composite tool definition with valid elicitation steps", func() {
			updatedCompositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      compositeToolDefName,
				Namespace: namespace,
			}, updatedCompositeToolDef)).Should(Succeed())

			// Verify elicitation steps exist and have correct configuration
			Expect(updatedCompositeToolDef.Spec.Steps).To(HaveLen(4))

			// Verify first elicitation step (confirm_deploy)
			confirmStep := updatedCompositeToolDef.Spec.Steps[1]
			Expect(confirmStep.ID).To(Equal("confirm_deploy"))
			Expect(confirmStep.Type).To(Equal(mcpv1alpha1.WorkflowStepTypeElicitation))
			Expect(confirmStep.Message).To(Equal("Proceed with deployment?"))
			Expect(confirmStep.OnDecline).NotTo(BeNil())
			Expect(confirmStep.OnDecline.Action).To(Equal("skip_remaining"))
			Expect(confirmStep.OnCancel).NotTo(BeNil())
			Expect(confirmStep.OnCancel.Action).To(Equal("abort"))
			Expect(confirmStep.Schema).NotTo(BeNil())

			// Verify second elicitation step (select_env)
			selectStep := updatedCompositeToolDef.Spec.Steps[2]
			Expect(selectStep.ID).To(Equal("select_env"))
			Expect(selectStep.Type).To(Equal(mcpv1alpha1.WorkflowStepTypeElicitation))
			Expect(selectStep.Message).To(Equal("Select target environment"))
			Expect(selectStep.OnDecline).NotTo(BeNil())
			Expect(selectStep.OnDecline.Action).To(Equal("continue"))
			Expect(selectStep.OnCancel).NotTo(BeNil())
			Expect(selectStep.OnCancel.Action).To(Equal("abort"))
		})
	})

	Context("When testing all valid elicitation handler actions", Ordered, func() {
		var (
			namespace            string
			vmcpName             string
			mcpGroupName         string
			compositeToolDefName string
			vmcp                 *mcpv1alpha1.VirtualMCPServer
			mcpGroup             *mcpv1alpha1.MCPGroup
			compositeToolDef     *mcpv1alpha1.VirtualMCPCompositeToolDefinition
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			vmcpName = "test-vmcp-all-handlers"
			mcpGroupName = "test-group-all-handlers"
			compositeToolDefName = "test-all-handlers-tool"

			// Create MCPGroup
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for all elicitation handlers",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for MCPGroup to be ready
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Create VirtualMCPCompositeToolDefinition with all handler combinations
			compositeToolDef = &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: vmcpconfig.CompositeToolConfig{
						Name:        "all_handlers_workflow",
						Description: "Test all valid elicitation handler actions",
						Steps: []vmcpconfig.WorkflowStepConfig{
							// Test skip_remaining
							{
								ID:      "elicit_skip",
								Type:    mcpv1alpha1.WorkflowStepTypeElicitation,
								Message: "Test skip_remaining",
								Schema:  thvjson.NewMap(map[string]any{"type": "object"}),
								OnDecline: &vmcpconfig.ElicitationResponseConfig{
									Action: "skip_remaining",
								},
								OnCancel: &vmcpconfig.ElicitationResponseConfig{
									Action: "skip_remaining",
								},
							},
							// Test abort
							{
								ID:      "elicit_abort",
								Type:    mcpv1alpha1.WorkflowStepTypeElicitation,
								Message: "Test abort",
								Schema:  thvjson.NewMap(map[string]any{"type": "object"}),
								OnDecline: &vmcpconfig.ElicitationResponseConfig{
									Action: "abort",
								},
								OnCancel: &vmcpconfig.ElicitationResponseConfig{
									Action: "abort",
								},
							},
							// Test continue
							{
								ID:      "elicit_continue",
								Type:    mcpv1alpha1.WorkflowStepTypeElicitation,
								Message: "Test continue",
								Schema:  thvjson.NewMap(map[string]any{"type": "object"}),
								OnDecline: &vmcpconfig.ElicitationResponseConfig{
									Action: "continue",
								},
								OnCancel: &vmcpconfig.ElicitationResponseConfig{
									Action: "continue",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, compositeToolDef)).Should(Succeed())

			// Create VirtualMCPServer
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: compositeToolDefName},
						},
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for reconciliation
			Eventually(func() bool {
				updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				return err == nil && updatedVMCP.Status.ObservedGeneration > 0
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, compositeToolDef)
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should accept all valid elicitation handler actions", func() {
			updatedCompositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      compositeToolDefName,
				Namespace: namespace,
			}, updatedCompositeToolDef)).Should(Succeed())

			// Verify all three steps exist with their respective handlers
			Expect(updatedCompositeToolDef.Spec.Steps).To(HaveLen(3))

			// Verify skip_remaining handler
			skipStep := updatedCompositeToolDef.Spec.Steps[0]
			Expect(skipStep.OnDecline.Action).To(Equal("skip_remaining"))
			Expect(skipStep.OnCancel.Action).To(Equal("skip_remaining"))

			// Verify abort handler
			abortStep := updatedCompositeToolDef.Spec.Steps[1]
			Expect(abortStep.OnDecline.Action).To(Equal("abort"))
			Expect(abortStep.OnCancel.Action).To(Equal("abort"))

			// Verify continue handler
			continueStep := updatedCompositeToolDef.Spec.Steps[2]
			Expect(continueStep.OnDecline.Action).To(Equal("continue"))
			Expect(continueStep.OnCancel.Action).To(Equal("continue"))
		})

		It("Should have VirtualMCPServer in valid state with all handler types", func() {
			updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, updatedVMCP)).Should(Succeed())

			// Verify VirtualMCPServer successfully validated the composite tool
			Expect(updatedVMCP.Status.Phase).To(Or(
				Equal(mcpv1alpha1.VirtualMCPServerPhaseReady),
				Equal(mcpv1alpha1.VirtualMCPServerPhasePending),
			))

			// Verify CompositeToolRefsValidated condition
			foundCondition := false
			for _, cond := range updatedVMCP.Status.Conditions {
				if cond.Type == mcpv1alpha1.ConditionTypeCompositeToolRefsValidated {
					foundCondition = true
					Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				}
			}
			Expect(foundCondition).To(BeTrue())
		})
	})

	Context("When creating composite tool with mixed tool and elicitation steps", Ordered, func() {
		var (
			namespace            string
			vmcpName             string
			mcpGroupName         string
			compositeToolDefName string
			vmcp                 *mcpv1alpha1.VirtualMCPServer
			mcpGroup             *mcpv1alpha1.MCPGroup
			compositeToolDef     *mcpv1alpha1.VirtualMCPCompositeToolDefinition
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			vmcpName = "test-vmcp-mixed-steps"
			mcpGroupName = "test-group-mixed-steps"
			compositeToolDefName = "test-mixed-steps-tool"

			// Create MCPGroup
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for mixed steps",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for MCPGroup to be ready
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Create composite tool with alternating tool calls and elicitations
			compositeToolDef = &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: vmcpconfig.CompositeToolConfig{
						Name:        "mixed_steps_workflow",
						Description: "Workflow with alternating tool calls and elicitations",
						Steps: []vmcpconfig.WorkflowStepConfig{
							// Tool call
							{
								ID:   "tool1",
								Type: mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool: "prepare",
							},
							// Elicitation
							{
								ID:        "elicit1",
								Type:      mcpv1alpha1.WorkflowStepTypeElicitation,
								Message:   "Confirm step 1?",
								Schema:    thvjson.NewMap(map[string]any{"type": "object"}),
								DependsOn: []string{"tool1"},
								OnDecline: &vmcpconfig.ElicitationResponseConfig{
									Action: "abort",
								},
							},
							// Tool call
							{
								ID:        "tool2",
								Type:      mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool:      "execute",
								DependsOn: []string{"elicit1"},
							},
							// Elicitation
							{
								ID:        "elicit2",
								Type:      mcpv1alpha1.WorkflowStepTypeElicitation,
								Message:   "Confirm step 2?",
								Schema:    thvjson.NewMap(map[string]any{"type": "object"}),
								DependsOn: []string{"tool2"},
								OnCancel: &vmcpconfig.ElicitationResponseConfig{
									Action: "abort",
								},
							},
							// Final tool call
							{
								ID:        "tool3",
								Type:      mcpv1alpha1.WorkflowStepTypeToolCall,
								Tool:      "finalize",
								DependsOn: []string{"elicit2"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, compositeToolDef)).Should(Succeed())

			// Create VirtualMCPServer
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: compositeToolDefName},
						},
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for reconciliation
			Eventually(func() bool {
				updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				return err == nil && updatedVMCP.Status.ObservedGeneration > 0
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, compositeToolDef)
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should successfully create workflow with mixed tool and elicitation steps", func() {
			updatedCompositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      compositeToolDefName,
				Namespace: namespace,
			}, updatedCompositeToolDef)).Should(Succeed())

			// Verify all steps exist
			Expect(updatedCompositeToolDef.Spec.Steps).To(HaveLen(5))

			// Verify alternating pattern
			Expect(updatedCompositeToolDef.Spec.Steps[0].Type).To(Equal(mcpv1alpha1.WorkflowStepTypeToolCall))
			Expect(updatedCompositeToolDef.Spec.Steps[1].Type).To(Equal(mcpv1alpha1.WorkflowStepTypeElicitation))
			Expect(updatedCompositeToolDef.Spec.Steps[2].Type).To(Equal(mcpv1alpha1.WorkflowStepTypeToolCall))
			Expect(updatedCompositeToolDef.Spec.Steps[3].Type).To(Equal(mcpv1alpha1.WorkflowStepTypeElicitation))
			Expect(updatedCompositeToolDef.Spec.Steps[4].Type).To(Equal(mcpv1alpha1.WorkflowStepTypeToolCall))

			// Verify dependencies are preserved
			Expect(updatedCompositeToolDef.Spec.Steps[1].DependsOn).To(ContainElement("tool1"))
			Expect(updatedCompositeToolDef.Spec.Steps[2].DependsOn).To(ContainElement("elicit1"))
			Expect(updatedCompositeToolDef.Spec.Steps[3].DependsOn).To(ContainElement("tool2"))
			Expect(updatedCompositeToolDef.Spec.Steps[4].DependsOn).To(ContainElement("elicit2"))
		})

		It("Should have valid VirtualMCPServer status for mixed step workflow", func() {
			updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, updatedVMCP)).Should(Succeed())

			Expect(updatedVMCP.Status.ObservedGeneration).To(Equal(updatedVMCP.Generation))
			Expect(updatedVMCP.Status.Phase).To(Or(
				Equal(mcpv1alpha1.VirtualMCPServerPhaseReady),
				Equal(mcpv1alpha1.VirtualMCPServerPhasePending),
			))
		})
	})
})
