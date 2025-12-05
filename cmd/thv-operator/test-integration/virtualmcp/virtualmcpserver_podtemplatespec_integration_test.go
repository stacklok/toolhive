// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("VirtualMCPServer PodTemplateSpec Integration Tests", func() {
	const (
		timeout                           = time.Second * 30
		interval                          = time.Millisecond * 250
		defaultNamespace                  = "default"
		conditionTypePodTemplateSpecValid = "PodTemplateSpecValid"
	)

	Context("When creating a VirtualMCPServer with invalid PodTemplateSpec", Ordered, func() {
		var (
			namespace        string
			mcpGroupName     string
			virtualMCPName   string
			mcpGroup         *mcpv1alpha1.MCPGroup
			virtualMCPServer *mcpv1alpha1.VirtualMCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpGroupName = "test-group-invalid-podtemplate"
			virtualMCPName = "test-vmcp-invalid-podtemplate"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			err := k8sClient.Create(ctx, ns)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			// Create MCPGroup first (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for PodTemplateSpec tests",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Define the VirtualMCPServer resource with invalid PodTemplateSpec
			virtualMCPServer = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      virtualMCPName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
					// Invalid PodTemplateSpec - containers should be an array, not a string
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec": {"containers": "invalid-not-an-array"}}`),
					},
				},
			}

			// Create the VirtualMCPServer
			Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up the VirtualMCPServer
			Expect(k8sClient.Delete(ctx, virtualMCPServer)).Should(Succeed())
			// Clean up the MCPGroup
			Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
		})

		It("Should set PodTemplateSpecValid condition to False", func() {
			// Wait for the status to be updated with the invalid condition
			Eventually(func() bool {
				updatedVirtualMCPServer := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: namespace,
				}, updatedVirtualMCPServer)
				if err != nil {
					return false
				}

				// Check for PodTemplateSpecValid condition
				for _, cond := range updatedVirtualMCPServer.Status.Conditions {
					if cond.Type == conditionTypePodTemplateSpecValid {
						return cond.Status == metav1.ConditionFalse &&
							cond.Reason == "InvalidPodTemplateSpec"
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Verify the condition message contains expected text
			updatedVirtualMCPServer := &mcpv1alpha1.VirtualMCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      virtualMCPName,
				Namespace: namespace,
			}, updatedVirtualMCPServer)).Should(Succeed())

			var foundCondition *metav1.Condition
			for i, cond := range updatedVirtualMCPServer.Status.Conditions {
				if cond.Type == conditionTypePodTemplateSpecValid {
					foundCondition = &updatedVirtualMCPServer.Status.Conditions[i]
					break
				}
			}

			Expect(foundCondition).NotTo(BeNil())
			Expect(foundCondition.Message).To(ContainSubstring("Failed to parse PodTemplateSpec"))
			Expect(foundCondition.Message).To(ContainSubstring("Deployment blocked until fixed"))
		})

		It("Should not create a Deployment for invalid VirtualMCPServer", func() {
			// Verify that no deployment was created
			deployment := &appsv1.Deployment{}
			Consistently(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: namespace,
				}, deployment)
				return err != nil
			}, time.Second*5, interval).Should(BeTrue())
		})

		It("Should have Failed phase in status", func() {
			updatedVirtualMCPServer := &mcpv1alpha1.VirtualMCPServer{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: namespace,
				}, updatedVirtualMCPServer)
				if err != nil {
					return false
				}
				return updatedVirtualMCPServer.Status.Phase == mcpv1alpha1.VirtualMCPServerPhaseFailed
			}, timeout, interval).Should(BeTrue())

			Expect(updatedVirtualMCPServer.Status.Message).To(ContainSubstring("Invalid PodTemplateSpec"))
		})
	})

	Context("When creating a VirtualMCPServer with valid PodTemplateSpec", Ordered, func() {
		var (
			namespace        string
			mcpGroupName     string
			virtualMCPName   string
			mcpGroup         *mcpv1alpha1.MCPGroup
			virtualMCPServer *mcpv1alpha1.VirtualMCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpGroupName = "test-group-valid-podtemplate"
			virtualMCPName = "test-vmcp-valid-podtemplate"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			err := k8sClient.Create(ctx, ns)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			// Create MCPGroup first (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for PodTemplateSpec tests",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Define the VirtualMCPServer resource with valid PodTemplateSpec containing nodeSelector
			// Only specify nodeSelector - don't include containers array
			// Strategic merge will preserve the controller-generated vmcp container
			virtualMCPServer = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      virtualMCPName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec":{"nodeSelector":{"disktype":"ssd"}}}`),
					},
				},
			}

			// Create the VirtualMCPServer
			Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up the VirtualMCPServer
			Expect(k8sClient.Delete(ctx, virtualMCPServer)).Should(Succeed())
			// Clean up the MCPGroup
			Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
		})

		It("Should have PodTemplateSpecValid condition set to True", func() {
			Eventually(func() bool {
				updatedVirtualMCPServer := &mcpv1alpha1.VirtualMCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: namespace,
				}, updatedVirtualMCPServer)
				if err != nil {
					return false
				}

				for _, cond := range updatedVirtualMCPServer.Status.Conditions {
					if cond.Type == conditionTypePodTemplateSpecValid {
						return cond.Status == metav1.ConditionTrue
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should create a Deployment with nodeSelector applied", func() {
			// Wait for Deployment to be created
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: namespace,
				}, deployment)
			}, timeout, interval).Should(Succeed())

			// Verify the nodeSelector is applied directly to the PodSpec
			Expect(deployment.Spec.Template.Spec.NodeSelector).NotTo(BeNil())
			Expect(deployment.Spec.Template.Spec.NodeSelector["disktype"]).To(Equal("ssd"))
		})
	})

	Context("When updating VirtualMCPServer PodTemplateSpec", Ordered, func() {
		var (
			namespace        string
			mcpGroupName     string
			virtualMCPName   string
			mcpGroup         *mcpv1alpha1.MCPGroup
			virtualMCPServer *mcpv1alpha1.VirtualMCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpGroupName = "test-group-update-podtemplate"
			virtualMCPName = "test-vmcp-update-podtemplate"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			err := k8sClient.Create(ctx, ns)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			// Create MCPGroup first (required by VirtualMCPServer)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for PodTemplateSpec tests",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Define the VirtualMCPServer resource with PodTemplateSpec containing nodeSelector
			// Only specify nodeSelector - don't include containers array
			// Strategic merge will preserve the controller-generated vmcp container
			virtualMCPServer = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      virtualMCPName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec":{"nodeSelector":{"disktype":"ssd"}}}`),
					},
				},
			}

			// Create the VirtualMCPServer
			Expect(k8sClient.Create(ctx, virtualMCPServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up the VirtualMCPServer
			Expect(k8sClient.Delete(ctx, virtualMCPServer)).Should(Succeed())
			// Clean up the MCPGroup
			Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
		})

		It("Should initially create a Deployment with nodeSelector=ssd", func() {
			// Wait for Deployment to be created
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: namespace,
				}, deployment)
			}, timeout, interval).Should(Succeed())

			// Verify the initial nodeSelector
			Expect(deployment.Spec.Template.Spec.NodeSelector).NotTo(BeNil())
			Expect(deployment.Spec.Template.Spec.NodeSelector["disktype"]).To(Equal("ssd"))
		})

		It("Should update Deployment when PodTemplateSpec nodeSelector is changed", func() {
			// Update the VirtualMCPServer to change nodeSelector
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: namespace,
				}, virtualMCPServer); err != nil {
					return err
				}
				virtualMCPServer.Spec.PodTemplateSpec = &runtime.RawExtension{
					Raw: []byte(`{"spec":{"nodeSelector":{"disktype":"nvme"}}}`),
				}
				return k8sClient.Update(ctx, virtualMCPServer)
			}, timeout, interval).Should(Succeed())

			// Wait for Deployment to be updated with new nodeSelector
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      virtualMCPName,
					Namespace: namespace,
				}, deployment); err != nil {
					return false
				}

				// Check if nodeSelector has been updated to nvme
				if deployment.Spec.Template.Spec.NodeSelector == nil {
					return false
				}
				return deployment.Spec.Template.Spec.NodeSelector["disktype"] == "nvme"
			}, timeout, interval).Should(BeTrue())
		})
	})
})
