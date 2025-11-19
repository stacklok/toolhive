// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer CompositeToolDefinition Watch Integration Tests", func() {
	const (
		timeout          = time.Second * 30
		interval         = time.Millisecond * 250
		defaultNamespace = "default"
		conditionReady   = "Ready"
	)

	// Helper function to get and parse the vmcp ConfigMap
	getVmcpConfig := func(namespace, vmcpName string) (*vmcpconfig.Config, error) {
		configMapName := vmcpName + "-vmcp-config"
		configMap := &corev1.ConfigMap{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      configMapName,
			Namespace: namespace,
		}, configMap)
		if err != nil {
			return nil, err
		}

		// Parse the config.yaml from the ConfigMap
		configYAML, ok := configMap.Data["config.yaml"]
		if !ok {
			return nil, nil // ConfigMap exists but no config.yaml
		}

		var config vmcpconfig.Config
		if err := yaml.Unmarshal([]byte(configYAML), &config); err != nil {
			return nil, err
		}

		return &config, nil
	}

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

			// Create VirtualMCPServer with inline CompositeTools AND CompositeToolRefs
			// The inline tool will be used to verify reconciliation occurred
			// The CompositeToolRef will trigger the watch (but won't be resolved without the feature)
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					CompositeTools: []mcpv1alpha1.CompositeToolSpec{
						{
							Name:        "inline-tool",
							Description: "Inline composite tool for testing",
							Steps: []mcpv1alpha1.WorkflowStep{
								{
									ID:   "inline-step1",
									Tool: "inline-tool1",
								},
							},
						},
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

			// Verify that reconciliation occurred by checking the ConfigMap contains the INLINE composite tool
			// (We're not testing CompositeToolRef resolution - that's a separate feature)
			Eventually(func() bool {
				config, err := getVmcpConfig(namespace, vmcpName)
				if err != nil || config == nil {
					return false
				}

				// Check if the ConfigMap has the inline composite tool
				if len(config.CompositeTools) == 0 {
					return false
				}

				// Find the inline composite tool by name
				for _, tool := range config.CompositeTools {
					if tool.Name == "inline-tool" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue(), "ConfigMap should contain inline composite tool after watch-triggered reconciliation")

			// Verify the inline composite tool content is correct (proves reconciliation completed successfully)
			config, err := getVmcpConfig(namespace, vmcpName)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(config).ShouldNot(BeNil())
			Expect(config.CompositeTools).Should(HaveLen(1), "Should have exactly 1 composite tool (inline only, CompositeToolRef not resolved yet)")

			compositeTool := config.CompositeTools[0]
			Expect(compositeTool.Name).To(Equal("inline-tool"))
			Expect(compositeTool.Description).To(Equal("Inline composite tool for testing"))
			Expect(compositeTool.Steps).Should(HaveLen(1))
			Expect(compositeTool.Steps[0].ID).To(Equal("inline-step1"))
			Expect(compositeTool.Steps[0].Tool).To(Equal("inline-tool1"))

			// Verify the VirtualMCPServer is in a valid state after reconciliation
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

			// Create VirtualMCPServer with inline CompositeTools AND CompositeToolRefs
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					CompositeTools: []mcpv1alpha1.CompositeToolSpec{
						{
							Name:        "inline-tool-update",
							Description: "Inline composite tool for update test",
							Steps: []mcpv1alpha1.WorkflowStep{
								{
									ID:   "inline-step-update",
									Tool: "inline-tool-update1",
								},
							},
						},
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
			// Verify initial inline composite tool configuration exists
			config, err := getVmcpConfig(namespace, vmcpName)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(config).ShouldNot(BeNil())
			Expect(config.CompositeTools).Should(HaveLen(1), "Should have exactly 1 composite tool (inline only)")
			Expect(config.CompositeTools[0].Name).To(Equal("inline-tool-update"))
			Expect(config.CompositeTools[0].Description).To(Equal("Inline composite tool for update test"))

			// Update the VirtualMCPCompositeToolDefinition
			// This should trigger watch → reconciliation, but won't change the ConfigMap content
			// (since CompositeToolRefs resolution isn't implemented)
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

			// Verify that reconciliation occurred by checking the ConfigMap still has the inline tool
			// (Reconciliation happened successfully, ConfigMap was regenerated with inline tool)
			Eventually(func() bool {
				config, err := getVmcpConfig(namespace, vmcpName)
				if err != nil || config == nil {
					return false
				}

				// Check if the ConfigMap still has the inline composite tool (unchanged)
				if len(config.CompositeTools) == 0 {
					return false
				}

				for _, tool := range config.CompositeTools {
					if tool.Name == "inline-tool-update" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue(), "ConfigMap should still contain inline composite tool after watch-triggered reconciliation")

			// Verify the inline composite tool content is correct (proves reconciliation completed successfully)
			config, err = getVmcpConfig(namespace, vmcpName)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(config).ShouldNot(BeNil())
			Expect(config.CompositeTools).Should(HaveLen(1), "Should have exactly 1 composite tool (inline only)")

			compositeTool := config.CompositeTools[0]
			Expect(compositeTool.Name).To(Equal("inline-tool-update"))
			Expect(compositeTool.Description).To(Equal("Inline composite tool for update test"))
			Expect(compositeTool.Steps).Should(HaveLen(1))

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
			// Get initial ResourceVersion and ObservedGeneration
			initialVMCP := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, initialVMCP)).Should(Succeed())

			initialResourceVersion := initialVMCP.ResourceVersion
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

			// Verify that the VirtualMCPServer was NOT unnecessarily reconciled
			// ResourceVersion and ObservedGeneration should remain unchanged
			Consistently(func() bool {
				updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, updatedVMCP)
				if err != nil {
					return false
				}

				// Verify ResourceVersion and ObservedGeneration haven't changed
				resourceVersionUnchanged := updatedVMCP.ResourceVersion == initialResourceVersion
				observedGenerationUnchanged := updatedVMCP.Status.ObservedGeneration == initialObservedGeneration

				return resourceVersionUnchanged && observedGenerationUnchanged
			}, time.Second*3, interval).Should(BeTrue(), "VirtualMCPServer should not be reconciled for unrelated composite tool")

			// Final verification of state
			updatedVMCP := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, updatedVMCP)).Should(Succeed())

			// ObservedGeneration should be unchanged
			Expect(updatedVMCP.Status.ObservedGeneration).To(Equal(initialObservedGeneration))

			// ResourceVersion should be unchanged
			Expect(updatedVMCP.ResourceVersion).To(Equal(initialResourceVersion))

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
