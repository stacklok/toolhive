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

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer CompositeToolDefinition Watch Integration Tests", func() {
	const (
		timeout          = time.Second * 30
		interval         = time.Millisecond * 250
		defaultNamespace = "default"
		conditionReady   = "Ready"
	)

	Context("When a VirtualMCPCompositeToolDefinition is created after VirtualMCPServer", Ordered, func() {
		var (
			namespace            string
			vmcpName             string
			mcpGroupName         string
			compositeToolDefName string
			vmcp                 *mcpv1beta1.VirtualMCPServer
			mcpGroup             *mcpv1beta1.MCPGroup
			compositeToolDef     *mcpv1beta1.VirtualMCPCompositeToolDefinition
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			vmcpName = "test-vmcp-composite"
			mcpGroupName = "test-group-composite"
			compositeToolDefName = "test-composite-tool"

			// Create MCPGroup first (required by VirtualMCPServer)
			mcpGroup = &mcpv1beta1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPGroupSpec{
					Description: "Test group for composite tool watch",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for MCPGroup to be ready
			Eventually(func() bool {
				updatedGroup := &mcpv1beta1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1beta1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Create VirtualMCPServer that references the composite tool definition
			// (even though the composite tool doesn't exist yet)
			vmcp = &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: compositeToolDefName},
						},
					},
					IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for initial VirtualMCPServer reconciliation
			// Check that the CompositeToolRefsValidated condition is set (even if False)
			// This indicates reconciliation was attempted, similar to how GroupRef validation is tested
			Eventually(func() bool {
				updatedVMCP := &mcpv1beta1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				if err != nil {
					return false
				}

				// Check for CompositeToolRefsValidated condition
				for _, cond := range updatedVMCP.Status.Conditions {
					if cond.Type == mcpv1beta1.ConditionTypeCompositeToolRefsValidated {
						return cond.Status == metav1.ConditionFalse &&
							cond.Reason == mcpv1beta1.ConditionReasonCompositeToolRefNotFound
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			// Clean up
			if compositeToolDef != nil {
				_ = k8sClient.Delete(ctx, compositeToolDef)
			}
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should trigger VirtualMCPServer reconciliation when composite tool definition is created", func() {
			// Create the VirtualMCPCompositeToolDefinition with Output spec
			compositeToolDef = &mcpv1beta1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: vmcpconfig.CompositeToolConfig{
						Name:        "test-workflow",
						Description: "Test workflow for integration test",
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "step1",
								Tool: "tool1",
							},
						},
						Output: &vmcpconfig.OutputConfig{
							Properties: map[string]vmcpconfig.OutputProperty{
								"result": {
									Type:        "string",
									Description: "The workflow result",
									Value:       "{{.steps.step1.output.data}}",
								},
								"status": {
									Type:        "string",
									Description: "Status of operation",
									Value:       "{{.steps.step1.output.status}}",
									Default:     thvjson.NewAny("success"),
								},
							},
							Required: []string{"result"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, compositeToolDef)).Should(Succeed())

			// Wait for VirtualMCPServer to reach a stable successful state after the composite
			// tool definition is created. All conditions are checked atomically in a single
			// Eventually to avoid races where the controller passes through a transient state
			// (CompositeToolRefsValidated=True but Phase still=Failed from a prior reconcile)
			// that satisfies each check individually but not all at once.
			Eventually(func() bool {
				updatedVMCP := &mcpv1beta1.VirtualMCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP); err != nil {
					return false
				}

				conditionValid := false
				for _, cond := range updatedVMCP.Status.Conditions {
					if cond.Type == mcpv1beta1.ConditionTypeCompositeToolRefsValidated {
						conditionValid = cond.Status == metav1.ConditionTrue &&
							cond.Reason == mcpv1beta1.ConditionReasonCompositeToolRefsValid
						break
					}
				}

				phaseOK := updatedVMCP.Status.Phase == mcpv1beta1.VirtualMCPServerPhaseReady ||
					updatedVMCP.Status.Phase == mcpv1beta1.VirtualMCPServerPhasePending

				return conditionValid &&
					updatedVMCP.Status.ObservedGeneration > 0 &&
					updatedVMCP.Status.ObservedGeneration == updatedVMCP.Generation &&
					phaseOK
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When a VirtualMCPCompositeToolDefinition is updated", Ordered, func() {
		var (
			namespace            string
			vmcpName             string
			mcpGroupName         string
			compositeToolDefName string
			vmcp                 *mcpv1beta1.VirtualMCPServer
			mcpGroup             *mcpv1beta1.MCPGroup
			compositeToolDef     *mcpv1beta1.VirtualMCPCompositeToolDefinition
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			vmcpName = "test-vmcp-update"
			mcpGroupName = "test-group-update"
			compositeToolDefName = "test-composite-tool-update"

			// Create MCPGroup
			mcpGroup = &mcpv1beta1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPGroupSpec{
					Description: "Test group for composite tool update",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for MCPGroup to be ready
			Eventually(func() bool {
				updatedGroup := &mcpv1beta1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1beta1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Create VirtualMCPCompositeToolDefinition first
			compositeToolDef = &mcpv1beta1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: vmcpconfig.CompositeToolConfig{
						Name:        "test-workflow-update",
						Description: "Initial description",
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "step1",
								Tool: "tool1",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, compositeToolDef)).Should(Succeed())

			// Create VirtualMCPServer that references the composite tool definition
			vmcp = &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
					Config: vmcpconfig.Config{
						Group: mcpGroupName,
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: compositeToolDefName},
						},
					},
					IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for initial reconciliation
			Eventually(func() bool {
				updatedVMCP := &mcpv1beta1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				return err == nil && updatedVMCP.Status.ObservedGeneration > 0
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			// Clean up
			_ = k8sClient.Delete(ctx, compositeToolDef)
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should trigger VirtualMCPServer reconciliation when composite tool definition is updated", func() {
			// Update the VirtualMCPCompositeToolDefinition
			Eventually(func() error {
				freshCompositeToolDef := &mcpv1beta1.VirtualMCPCompositeToolDefinition{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      compositeToolDefName,
					Namespace: namespace,
				}, freshCompositeToolDef); err != nil {
					return err
				}
				freshCompositeToolDef.Spec.Description = "Updated description"
				return k8sClient.Update(ctx, freshCompositeToolDef)
			}, timeout, interval).Should(Succeed())

			// The VirtualMCPServer should remain reconciled after the update
			// We verify this by checking that ObservedGeneration stays current
			Consistently(func() bool {
				updatedVMCP := &mcpv1beta1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				if err != nil {
					return false
				}

				// Check that ObservedGeneration stays current (indicating successful reconciliation)
				return updatedVMCP.Status.ObservedGeneration == updatedVMCP.Generation
			}, time.Second*5, interval).Should(BeTrue())

			// Verify the VirtualMCPServer is still in a valid state
			updatedVMCP := &mcpv1beta1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, updatedVMCP)).Should(Succeed())

			Expect(updatedVMCP.Status.ObservedGeneration).To(Equal(updatedVMCP.Generation))
			Expect(updatedVMCP.Status.Phase).To(Or(
				Equal(mcpv1beta1.VirtualMCPServerPhaseReady),
				Equal(mcpv1beta1.VirtualMCPServerPhasePending),
			))
		})
	})

	Context("When VirtualMCPServer does not reference composite tool definition", Ordered, func() {
		var (
			namespace            string
			vmcpName             string
			mcpGroupName         string
			compositeToolDefName string
			vmcp                 *mcpv1beta1.VirtualMCPServer
			mcpGroup             *mcpv1beta1.MCPGroup
			compositeToolDef     *mcpv1beta1.VirtualMCPCompositeToolDefinition
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			vmcpName = "test-vmcp-noref"
			mcpGroupName = "test-group-noref"
			compositeToolDefName = "test-composite-tool-noref"

			// Create MCPGroup
			mcpGroup = &mcpv1beta1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.MCPGroupSpec{
					Description: "Test group without composite tool ref",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for MCPGroup to be ready
			Eventually(func() bool {
				updatedGroup := &mcpv1beta1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1beta1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Create VirtualMCPServer WITHOUT referencing the composite tool definition
			vmcp = &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
					Config:   vmcpconfig.Config{Group: mcpGroupName},
					IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
						Type: "anonymous",
					},
					// No CompositeToolRefs
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for initial reconciliation
			Eventually(func() bool {
				updatedVMCP := &mcpv1beta1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				return err == nil && updatedVMCP.Status.ObservedGeneration > 0
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			// Clean up
			_ = k8sClient.Delete(ctx, compositeToolDef)
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should NOT trigger VirtualMCPServer reconciliation when unrelated composite tool definition is created", func() {
			// Get initial generation and observed generation
			initialVMCP := &mcpv1beta1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, initialVMCP)).Should(Succeed())

			initialObservedGeneration := initialVMCP.Status.ObservedGeneration

			var initialReadyTime metav1.Time
			for _, cond := range initialVMCP.Status.Conditions {
				if cond.Type == conditionReady {
					initialReadyTime = cond.LastTransitionTime
					break
				}
			}

			// Create a composite tool definition that is NOT referenced by the VirtualMCPServer
			compositeToolDef = &mcpv1beta1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
					CompositeToolConfig: vmcpconfig.CompositeToolConfig{
						Name:        "unrelated-workflow",
						Description: "Workflow not referenced by VirtualMCPServer",
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "step1",
								Tool: "tool1",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, compositeToolDef)).Should(Succeed())

			// Wait a bit to ensure any potential reconciliation would have occurred
			time.Sleep(2 * time.Second)

			// Verify that the VirtualMCPServer was NOT unnecessarily reconciled
			// The ObservedGeneration should remain the same, and conditions shouldn't change
			updatedVMCP := &mcpv1beta1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, updatedVMCP)).Should(Succeed())

			// ObservedGeneration should be unchanged
			Expect(updatedVMCP.Status.ObservedGeneration).To(Equal(initialObservedGeneration))

			// Ready condition timestamp should be unchanged
			for _, cond := range updatedVMCP.Status.Conditions {
				if cond.Type == conditionReady {
					Expect(cond.LastTransitionTime.Equal(&initialReadyTime)).To(BeTrue(),
						"Ready condition timestamp should not change for unrelated composite tool")
					break
				}
			}
		})
	})
})
