// Package controllers contains integration tests for the MCPServer controller
package controllers

import (
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPServer Controller Integration Tests", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When creating an Stdio MCPServer", Ordered, func() {
		var (
			namespace        string
			mcpServerName    string
			mcpServer        *mcpv1alpha1.MCPServer
			createdMCPServer *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = "default"
			mcpServerName = "test-mcpserver"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Define the MCPServer resource
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:      "example/mcp-server:latest",
					Transport:  "stdio",
					ProxyMode:  "sse",
					Port:       8080,
					TargetPort: 8080,
					Args:       []string{"--verbose"},
					Env: []mcpv1alpha1.EnvVar{
						{
							Name:  "DEBUG",
							Value: "true",
						},
					},
					Resources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU:    "500m",
							Memory: "1Gi",
						},
						Requests: mcpv1alpha1.ResourceList{
							CPU:    "100m",
							Memory: "128Mi",
						},
					},
				},
			}

			// Create the MCPServer
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())

			createdMCPServer = &mcpv1alpha1.MCPServer{}
			k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, createdMCPServer)
		})

		AfterAll(func() {
			// Clean up the MCPServer
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())
		})

		It("Should create a Deployment with proper configuration", func() {

			// Wait for Deployment to be created
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, deployment)
			}, timeout, interval).Should(Succeed())

			// Verify Deployment metadata
			Expect(deployment.Name).To(Equal(mcpServerName))
			Expect(deployment.Namespace).To(Equal(namespace))

			// Verify owner reference is set correctly
			verifyOwnerReference(deployment.OwnerReferences, createdMCPServer, "Deployment")

			// Verify Deployment labels
			expectedLabels := map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": mcpServerName,
				"toolhive":                   "true",
				"toolhive-name":              mcpServerName,
			}
			for key, value := range expectedLabels {
				Expect(deployment.Labels).To(HaveKeyWithValue(key, value))
			}

			// Verify Deployment spec
			Expect(deployment.Spec.Replicas).To(Equal(ptr.To(int32(1))))

			// Verify selector
			Expect(deployment.Spec.Selector.MatchLabels).To(Equal(expectedLabels))

			// Verify pod template labels
			for key, value := range expectedLabels {
				Expect(deployment.Spec.Template.Labels).To(HaveKeyWithValue(key, value))
			}

			// Verify ServiceAccount
			expectedServiceAccount := fmt.Sprintf("%s-proxy-runner", mcpServerName)
			Expect(deployment.Spec.Template.Spec.ServiceAccountName).To(Equal(expectedServiceAccount))

			// Verify there's exactly one container (the toolhive proxy runner)
			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))

			templateSpec := deployment.Spec.Template.Spec

			foundRunconfigVolume := false
			for _, v := range templateSpec.Volumes {
				if v.Name == "runconfig" && v.ConfigMap != nil && v.ConfigMap.Name == (mcpServerName+"-runconfig") {
					foundRunconfigVolume = true
					break
				}
			}
			Expect(foundRunconfigVolume).To(BeTrue(), "Deployment should have a volume sourced from runconfig ConfigMap")

			container := deployment.Spec.Template.Spec.Containers[0]

			// Verify that the runconfig ConfigMap is mounted as a volume
			foundRunconfigMount := false
			for _, vm := range container.VolumeMounts {
				if vm.Name == "runconfig" && vm.MountPath == "/etc/runconfig" {
					foundRunconfigMount = true
					break
				}
			}
			Expect(foundRunconfigMount).To(BeTrue(), "runconfig ConfigMap should be mounted at /etc/runconfig")

			// Verify container name and image
			Expect(container.Name).To(Equal("toolhive"))
			Expect(container.Image).To(Equal(getExpectedRunnerImage()))

			// Verify resource requirements
			Expect(container.Resources.Requests).To(HaveKeyWithValue(
				corev1.ResourceCPU,
				resource.MustParse("100m"),
			))
			Expect(container.Resources.Requests).To(HaveKeyWithValue(
				corev1.ResourceMemory,
				resource.MustParse("128Mi"),
			))
			Expect(container.Resources.Limits).To(HaveKeyWithValue(
				corev1.ResourceCPU,
				resource.MustParse("500m"),
			))
			Expect(container.Resources.Limits).To(HaveKeyWithValue(
				corev1.ResourceMemory,
				resource.MustParse("1Gi"),
			))

			// Verify container args contain the required parameters
			Expect(container.Args).To(ContainElement("run"))
			Expect(container.Args).To(ContainElement("--foreground=true"))
			Expect(container.Args).To(ContainElement(mcpServer.Spec.Image))

			// Verify container ports
			Expect(container.Ports).To(HaveLen(1))
			Expect(container.Ports[0].Name).To(Equal("http"))
			Expect(container.Ports[0].ContainerPort).To(Equal(mcpServer.Spec.Port))
			Expect(container.Ports[0].Protocol).To(Equal(corev1.ProtocolTCP))

			// Verify probes
			Expect(container.LivenessProbe).NotTo(BeNil())
			Expect(container.LivenessProbe.ProbeHandler.HTTPGet.Path).To(Equal("/health"))
			Expect(container.LivenessProbe.ProbeHandler.HTTPGet.Port).To(Equal(intstr.FromString("http")))
			Expect(container.LivenessProbe.InitialDelaySeconds).To(Equal(int32(30)))
			Expect(container.LivenessProbe.PeriodSeconds).To(Equal(int32(10)))

			Expect(container.ReadinessProbe).NotTo(BeNil())
			Expect(container.ReadinessProbe.ProbeHandler.HTTPGet.Path).To(Equal("/health"))
			Expect(container.ReadinessProbe.ProbeHandler.HTTPGet.Port).To(Equal(intstr.FromString("http")))
			Expect(container.ReadinessProbe.InitialDelaySeconds).To(Equal(int32(5)))
			Expect(container.ReadinessProbe.PeriodSeconds).To(Equal(int32(5)))

		})

		It("Should create the RunConfig ConfigMap", func() {

			// Wait for Service to be created (using the correct naming pattern)
			configMap := &corev1.ConfigMap{}
			configMapName := mcpServerName + "-runconfig"
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: namespace,
				}, configMap)
			}, timeout, interval).Should(Succeed())

			// Verify owner reference is set correctly
			verifyOwnerReference(configMap.OwnerReferences, createdMCPServer, "ConfigMap")

			// Verify Service configuration
			Expect(configMap.Data).To(HaveKey("runconfig.json"))
			Expect(configMap.Annotations).To(HaveKey("toolhive.stacklok.dev/content-checksum"))
		})

		It("Should create a Service for the MCPServer Proxy", func() {

			// Wait for Service to be created (using the correct naming pattern)
			service := &corev1.Service{}
			serviceName := "mcp-" + mcpServerName + "-proxy"
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      serviceName,
					Namespace: namespace,
				}, service)
			}, timeout, interval).Should(Succeed())

			// Verify owner reference is set correctly
			verifyOwnerReference(service.OwnerReferences, createdMCPServer, "Service")

			// Verify Service configuration
			Expect(service.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
			Expect(service.Spec.Ports).To(HaveLen(1))
			Expect(service.Spec.Ports[0].Port).To(Equal(int32(8080)))

		})

		It("Should create RBAC resources when ServiceAccount is not specified", func() {

			// Wait for ServiceAccount to be created
			serviceAccountName := mcpServerName + "-proxy-runner"
			serviceAccount := &corev1.ServiceAccount{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      serviceAccountName,
					Namespace: namespace,
				}, serviceAccount)
			}, timeout, interval).Should(Succeed())

			// Verify ServiceAccount owner reference
			verifyOwnerReference(serviceAccount.OwnerReferences, createdMCPServer, "ServiceAccount")

			// Wait for Role to be created
			role := &rbacv1.Role{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      serviceAccountName,
					Namespace: namespace,
				}, role)
			}, timeout, interval).Should(Succeed())

			// Verify Role owner reference
			verifyOwnerReference(role.OwnerReferences, createdMCPServer, "Role")

			// Verify Role has expected rules
			Expect(role.Rules).NotTo(BeEmpty())

			// Wait for RoleBinding to be created
			roleBinding := &rbacv1.RoleBinding{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      serviceAccountName,
					Namespace: namespace,
				}, roleBinding)
			}, timeout, interval).Should(Succeed())

			// Verify RoleBinding owner reference
			verifyOwnerReference(roleBinding.OwnerReferences, createdMCPServer, "RoleBinding")

			// Verify RoleBinding references the correct ServiceAccount and Role
			Expect(roleBinding.Subjects).To(HaveLen(1))
			Expect(roleBinding.Subjects[0].Name).To(Equal(serviceAccountName))
			Expect(roleBinding.RoleRef.Name).To(Equal(serviceAccountName))

		})

		It("Should update Deployment when MCPServer spec changes", func() {

			// Wait for Deployment to be created
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, deployment)
			}, timeout, interval).Should(Succeed())

			// Verify owner reference is set correctly
			verifyOwnerReference(deployment.OwnerReferences, createdMCPServer, "Deployment")

			// Verify initial configuration
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Args).To(ContainElement("example/mcp-server:latest"))

			// Update the MCPServer spec
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, mcpServer); err != nil {
					return err
				}
				mcpServer.Spec.Image = "example/mcp-server:v2"
				return k8sClient.Update(ctx, mcpServer)
			}, timeout, interval).Should(Succeed())

			// Wait for Deployment to be updated
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, deployment); err != nil {
					return false
				}
				container := deployment.Spec.Template.Spec.Containers[0]
				// Check if the new image is in the args
				hasNewImage := false
				for _, arg := range container.Args {
					if arg == "example/mcp-server:v2" {
						hasNewImage = true
					}
				}
				return hasNewImage
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating an MCPServer with invalid PodTemplateSpec", Ordered, func() {
		var (
			namespace     string
			mcpServerName string
			mcpServer     *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = "default"
			mcpServerName = "test-invalid-podtemplate"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Define the MCPServer resource with invalid PodTemplateSpec
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
					// Invalid PodTemplateSpec - containers should be an array, not a string
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec": {"containers": "invalid-not-an-array"}}`),
					},
				},
			}

			// Create the MCPServer
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up the MCPServer
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())
		})

		It("Should set PodTemplateValid condition to False", func() {
			// Wait for the status to be updated with the invalid condition
			Eventually(func() bool {
				updatedMCPServer := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}

				// Check for PodTemplateValid condition
				for _, cond := range updatedMCPServer.Status.Conditions {
					if cond.Type == "PodTemplateValid" {
						return cond.Status == metav1.ConditionFalse &&
							cond.Reason == "InvalidPodTemplateSpec"
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Verify the condition message contains expected text
			updatedMCPServer := &mcpv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, updatedMCPServer)).Should(Succeed())

			var foundCondition *metav1.Condition
			for i, cond := range updatedMCPServer.Status.Conditions {
				if cond.Type == "PodTemplateValid" {
					foundCondition = &updatedMCPServer.Status.Conditions[i]
					break
				}
			}

			Expect(foundCondition).NotTo(BeNil())
			Expect(foundCondition.Message).To(ContainSubstring("Failed to parse PodTemplateSpec"))
			Expect(foundCondition.Message).To(ContainSubstring("Deployment blocked until fixed"))
		})

		It("Should not create a Deployment for invalid MCPServer", func() {
			// Verify that no deployment was created
			deployment := &appsv1.Deployment{}
			Consistently(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, deployment)
				return err != nil
			}, time.Second*5, interval).Should(BeTrue())
		})

		It("Should have Failed phase in status", func() {
			updatedMCPServer := &mcpv1alpha1.MCPServer{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}
				return updatedMCPServer.Status.Phase == mcpv1alpha1.MCPServerPhaseFailed
			}, timeout, interval).Should(BeTrue())

			Expect(updatedMCPServer.Status.Message).To(ContainSubstring("Invalid PodTemplateSpec"))
		})
	})
})

func verifyOwnerReference(ownerRefs []metav1.OwnerReference, mcpServer *mcpv1alpha1.MCPServer, resourceType string) {
	ExpectWithOffset(1, ownerRefs).To(HaveLen(1), fmt.Sprintf("%s should have exactly one owner reference", resourceType))
	ownerRef := ownerRefs[0]

	ExpectWithOffset(1, ownerRef.APIVersion).To(Equal("toolhive.stacklok.dev/v1alpha1"))
	ExpectWithOffset(1, ownerRef.Kind).To(Equal("MCPServer"))
	ExpectWithOffset(1, ownerRef.Name).To(Equal(mcpServer.Name))
	ExpectWithOffset(1, ownerRef.UID).To(Equal(mcpServer.UID))
	ExpectWithOffset(1, ownerRef.Controller).NotTo(BeNil(), "Controller field should be set")
	ExpectWithOffset(1, *ownerRef.Controller).To(BeTrue(), "Controller field should be true")
	ExpectWithOffset(1, ownerRef.BlockOwnerDeletion).NotTo(BeNil(), "BlockOwnerDeletion field should be set")
	ExpectWithOffset(1, *ownerRef.BlockOwnerDeletion).To(BeTrue(), "BlockOwnerDeletion should be true")
}

func getExpectedRunnerImage() string {
	image := os.Getenv("TOOLHIVE_RUNNER_IMAGE")
	if image == "" {
		image = "ghcr.io/stacklok/toolhive/proxyrunner:latest"
	}
	return image
}
