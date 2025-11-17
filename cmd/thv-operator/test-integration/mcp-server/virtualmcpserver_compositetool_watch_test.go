// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
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
			vmcp                 *mcpv1alpha1.VirtualMCPServer
			mcpGroup             *mcpv1alpha1.MCPGroup
			compositeToolDef     *mcpv1alpha1.VirtualMCPCompositeToolDefinition
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			vmcpName = "test-vmcp-composite"
			mcpGroupName = "test-group-composite"
			compositeToolDefName = "test-composite-tool"

			// Create MCPGroup first (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for composite tool watch",
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

			// Create VirtualMCPServer that references the composite tool definition
			// (even though the composite tool doesn't exist yet)
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
						{Name: compositeToolDefName},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for initial VirtualMCPServer reconciliation
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
			// Clean up
			if compositeToolDef != nil {
				_ = k8sClient.Delete(ctx, compositeToolDef)
			}
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should trigger VirtualMCPServer reconciliation when composite tool definition is created", func() {
			// Get the current ObservedGeneration
			initialVMCP := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, initialVMCP)).Should(Succeed())

			// Get initial Ready condition timestamp
			var initialReadyTime metav1.Time
			for _, cond := range initialVMCP.Status.Conditions {
				if cond.Type == conditionReady {
					initialReadyTime = cond.LastTransitionTime
					break
				}
			}

			// Create the VirtualMCPCompositeToolDefinition
			compositeToolDef = &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					Name:        "test-workflow",
					Description: "Test workflow for integration test",
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Tool: "tool1",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, compositeToolDef)).Should(Succeed())

			// The VirtualMCPServer should be reconciled after the composite tool definition is created
			// We verify this by checking that the conditions were updated (indicating reconciliation)
			Eventually(func() bool {
				updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				if err != nil {
					return false
				}

				// Check that ObservedGeneration is current
				if updatedVMCP.Status.ObservedGeneration != updatedVMCP.Generation {
					return false
				}

				// Check if Ready condition timestamp changed or was set
				for _, cond := range updatedVMCP.Status.Conditions {
					if cond.Type == conditionReady {
						// If timestamp is different or wasn't set before, reconciliation occurred
						return !cond.LastTransitionTime.Equal(&initialReadyTime) ||
							initialReadyTime.IsZero()
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Verify the VirtualMCPServer is in a valid state
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

	Context("When a VirtualMCPCompositeToolDefinition is updated", Ordered, func() {
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
			vmcpName = "test-vmcp-update"
			mcpGroupName = "test-group-update"
			compositeToolDefName = "test-composite-tool-update"

			// Create MCPGroup
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for composite tool update",
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

			// Create VirtualMCPCompositeToolDefinition first
			compositeToolDef = &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					Name:        "test-workflow-update",
					Description: "Initial description",
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Tool: "tool1",
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
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
						{Name: compositeToolDefName},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for initial reconciliation
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
			// Clean up
			_ = k8sClient.Delete(ctx, compositeToolDef)
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should trigger VirtualMCPServer reconciliation when composite tool definition is updated", func() {
			// Get initial conditions timestamp
			initialVMCP := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, initialVMCP)).Should(Succeed())

			var initialReadyConditionTime metav1.Time
			for _, cond := range initialVMCP.Status.Conditions {
				if cond.Type == conditionReady {
					initialReadyConditionTime = cond.LastTransitionTime
					break
				}
			}

			// Update the VirtualMCPCompositeToolDefinition
			Eventually(func() error {
				freshCompositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      compositeToolDefName,
					Namespace: namespace,
				}, freshCompositeToolDef); err != nil {
					return err
				}
				freshCompositeToolDef.Spec.Description = "Updated description"
				return k8sClient.Update(ctx, freshCompositeToolDef)
			}, timeout, interval).Should(Succeed())

			// The VirtualMCPServer should be reconciled after the update
			// Wait for the conditions to be updated (indicating a new reconciliation)
			Eventually(func() bool {
				updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				if err != nil {
					return false
				}

				// Check if the Ready condition was updated after the initial time
				for _, cond := range updatedVMCP.Status.Conditions {
					if cond.Type == conditionReady {
						// If the timestamp is different, a reconciliation occurred
						return !cond.LastTransitionTime.Equal(&initialReadyConditionTime)
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Verify the VirtualMCPServer is still in a valid state
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

	Context("When VirtualMCPServer does not reference composite tool definition", Ordered, func() {
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
			vmcpName = "test-vmcp-noref"
			mcpGroupName = "test-group-noref"
			compositeToolDefName = "test-composite-tool-noref"

			// Create MCPGroup
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group without composite tool ref",
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

			// Create VirtualMCPServer WITHOUT referencing the composite tool definition
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					// No CompositeToolRefs
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())

			// Wait for initial reconciliation
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
			// Clean up
			_ = k8sClient.Delete(ctx, compositeToolDef)
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should NOT trigger VirtualMCPServer reconciliation when unrelated composite tool definition is created", func() {
			// Get initial generation and observed generation
			initialVMCP := &mcpv1alpha1.VirtualMCPServer{}
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
			compositeToolDef = &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      compositeToolDefName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
					Name:        "unrelated-workflow",
					Description: "Workflow not referenced by VirtualMCPServer",
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Tool: "tool1",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, compositeToolDef)).Should(Succeed())

			// Wait a bit to ensure any potential reconciliation would have occurred
			time.Sleep(2 * time.Second)

			// Verify that the VirtualMCPServer was NOT unnecessarily reconciled
			// The ObservedGeneration should remain the same, and conditions shouldn't change
			updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
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
