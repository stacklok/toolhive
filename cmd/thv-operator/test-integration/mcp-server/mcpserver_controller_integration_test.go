// Package controllers contains integration tests for the MCPServer controller
package controllers

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
		timeout                        = time.Second * 30
		interval                       = time.Millisecond * 250
		defaultNamespace               = "default"
		conditionTypeGroupRefValidated = "GroupRefValidated"
		conditionTypePodTemplateValid  = "PodTemplateValid"
		runconfigVolumeName            = "runconfig"
	)

	Context("When creating an Stdio MCPServer", Ordered, func() {
		var (
			namespace        string
			mcpServerName    string
			mcpServer        *mcpv1alpha1.MCPServer
			createdMCPServer *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
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
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							PodTemplateMetadataOverrides: &mcpv1alpha1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"podspec-testlabel": "true",
								},
							},
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
			baseExpectedLabels := map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": mcpServerName,
				"toolhive":                   "true",
				"toolhive-name":              mcpServerName,
			}
			for key, value := range baseExpectedLabels {
				Expect(deployment.Labels).To(HaveKeyWithValue(key, value))
			}

			// Verify Deployment spec
			Expect(deployment.Spec.Replicas).To(Equal(ptr.To(int32(1))))

			// Verify selector
			Expect(deployment.Spec.Selector.MatchLabels).To(Equal(baseExpectedLabels))

			// Verify pod template labels
			podTemplateExepectedLabels := baseExpectedLabels
			podTemplateExepectedLabels["podspec-testlabel"] = "true"
			for key, value := range podTemplateExepectedLabels {
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
				if v.Name == runconfigVolumeName && v.ConfigMap != nil && v.ConfigMap.Name == (mcpServerName+"-runconfig") {
					foundRunconfigVolume = true
					break
				}
			}
			Expect(foundRunconfigVolume).To(BeTrue(), "Deployment should have a volume sourced from runconfig ConfigMap")

			container := deployment.Spec.Template.Spec.Containers[0]

			// Verify that the runconfig ConfigMap is mounted as a volume
			foundRunconfigMount := false
			for _, vm := range container.VolumeMounts {
				if vm.Name == runconfigVolumeName && vm.MountPath == "/etc/runconfig" {
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
			Expect(container.Args).To(ContainElement(mcpServer.Spec.Image))

			// Verify container ports
			Expect(container.Ports).To(HaveLen(1))
			Expect(container.Ports[0].Name).To(Equal("http"))
			Expect(container.Ports[0].ContainerPort).To(Equal(mcpServer.GetProxyPort()))
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
			namespace = defaultNamespace
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
					if cond.Type == conditionTypePodTemplateValid {
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
				if cond.Type == conditionTypePodTemplateValid {
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

	Context("When creating an MCPServer with PodTemplateSpec resource limits", Ordered, func() {
		var (
			namespace        string
			mcpServerName    string
			mcpServer        *mcpv1alpha1.MCPServer
			createdMCPServer *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpServerName = "test-podtemplate-resources"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Define the MCPServer resource with PodTemplateSpec resource limits
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec":{"containers":[{"name":"mcp","resources":{"limits":{"cpu":"2","memory":"2Gi"},"requests":{"cpu":"500m","memory":"512Mi"}}}]}}`),
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

		It("Should create a Deployment with --k8s-pod-patch argument containing resource limits", func() {
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

			// Find the --k8s-pod-patch argument
			container := deployment.Spec.Template.Spec.Containers[0]
			var podPatchJSON string
			for _, arg := range container.Args {
				if strings.HasPrefix(arg, "--k8s-pod-patch=") {
					podPatchJSON = strings.TrimPrefix(arg, "--k8s-pod-patch=")
					break
				}
			}
			Expect(podPatchJSON).NotTo(BeEmpty(), "Deployment should have --k8s-pod-patch argument")

			// Parse and verify the patch contains resource limits
			var patch map[string]interface{}
			Expect(json.Unmarshal([]byte(podPatchJSON), &patch)).Should(Succeed())

			spec, ok := patch["spec"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "patch should have spec")

			containers, ok := spec["containers"].([]interface{})
			Expect(ok).To(BeTrue(), "spec should have containers")
			Expect(containers).NotTo(BeEmpty())

			mcpContainer := containers[0].(map[string]interface{})
			Expect(mcpContainer["name"]).To(Equal("mcp"))

			resources, ok := mcpContainer["resources"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "container should have resources")

			limits, ok := resources["limits"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "resources should have limits")
			Expect(limits["cpu"]).To(Equal("2"))
			Expect(limits["memory"]).To(Equal("2Gi"))

			requests, ok := resources["requests"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "resources should have requests")
			Expect(requests["cpu"]).To(Equal("500m"))
			Expect(requests["memory"]).To(Equal("512Mi"))
		})

		It("Should have PodTemplateValid condition set to True", func() {
			Eventually(func() bool {
				updatedMCPServer := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}

				for _, cond := range updatedMCPServer.Status.Conditions {
					if cond.Type == conditionTypePodTemplateValid {
						return cond.Status == metav1.ConditionTrue
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating an MCPServer with PodTemplateSpec securityContext", Ordered, func() {
		var (
			namespace        string
			mcpServerName    string
			mcpServer        *mcpv1alpha1.MCPServer
			createdMCPServer *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpServerName = "test-podtemplate-security"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Define the MCPServer resource with PodTemplateSpec securityContext
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec":{"securityContext":{"runAsUser":1000,"runAsGroup":1000,"fsGroup":1000}}}`),
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

		It("Should create a Deployment with --k8s-pod-patch argument containing securityContext", func() {
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

			// Find the --k8s-pod-patch argument
			container := deployment.Spec.Template.Spec.Containers[0]
			var podPatchJSON string
			for _, arg := range container.Args {
				if strings.HasPrefix(arg, "--k8s-pod-patch=") {
					podPatchJSON = strings.TrimPrefix(arg, "--k8s-pod-patch=")
					break
				}
			}
			Expect(podPatchJSON).NotTo(BeEmpty(), "Deployment should have --k8s-pod-patch argument")

			// Parse and verify the patch contains securityContext
			var patch map[string]interface{}
			Expect(json.Unmarshal([]byte(podPatchJSON), &patch)).Should(Succeed())

			spec, ok := patch["spec"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "patch should have spec")

			securityContext, ok := spec["securityContext"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "spec should have securityContext")

			// JSON numbers are decoded as float64
			Expect(securityContext["runAsUser"]).To(BeNumerically("==", 1000))
			Expect(securityContext["runAsGroup"]).To(BeNumerically("==", 1000))
			Expect(securityContext["fsGroup"]).To(BeNumerically("==", 1000))
		})

		It("Should have PodTemplateValid condition set to True", func() {
			Eventually(func() bool {
				updatedMCPServer := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}

				for _, cond := range updatedMCPServer.Status.Conditions {
					if cond.Type == conditionTypePodTemplateValid {
						return cond.Status == metav1.ConditionTrue
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When updating MCPServer PodTemplateSpec", Ordered, func() {
		var (
			namespace     string
			mcpServerName string
			mcpServer     *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpServerName = "test-podtemplate-update"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Define the MCPServer resource WITHOUT PodTemplateSpec initially
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
				},
			}

			// Create the MCPServer
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up the MCPServer
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())
		})

		It("Should initially create a Deployment without nodeSelector in --k8s-pod-patch", func() {
			// Wait for Deployment to be created
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, deployment)
			}, timeout, interval).Should(Succeed())

			// Verify no nodeSelector in --k8s-pod-patch initially
			// Note: The patch may still exist with serviceAccountName, but should not contain nodeSelector
			container := deployment.Spec.Template.Spec.Containers[0]
			hasNodeSelector := false
			for _, arg := range container.Args {
				if strings.HasPrefix(arg, "--k8s-pod-patch=") {
					podPatchJSON := strings.TrimPrefix(arg, "--k8s-pod-patch=")
					var patch map[string]interface{}
					if err := json.Unmarshal([]byte(podPatchJSON), &patch); err == nil {
						if spec, ok := patch["spec"].(map[string]interface{}); ok {
							if _, ok := spec["nodeSelector"]; ok {
								hasNodeSelector = true
							}
						}
					}
					break
				}
			}
			Expect(hasNodeSelector).To(BeFalse(), "Deployment should not have nodeSelector in --k8s-pod-patch initially")
		})

		It("Should update Deployment with --k8s-pod-patch when PodTemplateSpec is added", func() {
			// Update the MCPServer to add PodTemplateSpec with nodeSelector
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, mcpServer); err != nil {
					return err
				}
				mcpServer.Spec.PodTemplateSpec = &runtime.RawExtension{
					Raw: []byte(`{"spec":{"nodeSelector":{"disktype":"ssd"}}}`),
				}
				return k8sClient.Update(ctx, mcpServer)
			}, timeout, interval).Should(Succeed())

			// Wait for Deployment to be updated with --k8s-pod-patch
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, deployment); err != nil {
					return false
				}

				container := deployment.Spec.Template.Spec.Containers[0]
				for _, arg := range container.Args {
					if strings.HasPrefix(arg, "--k8s-pod-patch=") {
						podPatchJSON := strings.TrimPrefix(arg, "--k8s-pod-patch=")
						var patch map[string]interface{}
						if err := json.Unmarshal([]byte(podPatchJSON), &patch); err != nil {
							return false
						}
						spec, ok := patch["spec"].(map[string]interface{})
						if !ok {
							return false
						}
						nodeSelector, ok := spec["nodeSelector"].(map[string]interface{})
						if !ok {
							return false
						}
						return nodeSelector["disktype"] == "ssd"
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When creating an MCPServer with valid PodTemplateSpec", Ordered, func() {
		var (
			namespace     string
			mcpServerName string
			mcpServer     *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpServerName = "test-podtemplate-valid"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Define the MCPServer resource with a simple valid PodTemplateSpec
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
					PodTemplateSpec: &runtime.RawExtension{
						Raw: []byte(`{"spec":{"serviceAccountName":"custom-sa"}}`),
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

		It("Should set PodTemplateValid condition to True with reason ValidPodTemplateSpec", func() {
			Eventually(func() bool {
				updatedMCPServer := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}

				for _, cond := range updatedMCPServer.Status.Conditions {
					if cond.Type == conditionTypePodTemplateValid {
						return cond.Status == metav1.ConditionTrue &&
							cond.Reason == "ValidPodTemplateSpec"
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Verify the condition details
			updatedMCPServer := &mcpv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: namespace,
			}, updatedMCPServer)).Should(Succeed())

			var foundCondition *metav1.Condition
			for i, cond := range updatedMCPServer.Status.Conditions {
				if cond.Type == conditionTypePodTemplateValid {
					foundCondition = &updatedMCPServer.Status.Conditions[i]
					break
				}
			}

			Expect(foundCondition).NotTo(BeNil())
			Expect(foundCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(foundCondition.Reason).To(Equal("ValidPodTemplateSpec"))
		})
	})

	Context("When creating an MCPServer with invalid GroupRef", Ordered, func() {
		var (
			namespace     string
			mcpServerName string
			mcpServer     *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpServerName = "test-invalid-groupref"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Define the MCPServer resource with invalid GroupRef
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
					GroupRef:  "non-existent-group", // This group doesn't exist
				},
			}

			// Create the MCPServer
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up the MCPServer
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())
		})

		It("Should set GroupRefValidated condition to False with reason GroupRefNotFound", func() {
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

				// Check for GroupRefValidated condition
				for _, cond := range updatedMCPServer.Status.Conditions {
					if cond.Type == conditionTypeGroupRefValidated {
						return cond.Status == metav1.ConditionFalse &&
							cond.Reason == "GroupRefNotFound"
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
				if cond.Type == conditionTypeGroupRefValidated {
					foundCondition = &updatedMCPServer.Status.Conditions[i]
					break
				}
			}

			Expect(foundCondition).NotTo(BeNil())
			Expect(foundCondition.Message).To(Equal(fmt.Sprintf("MCPGroup 'non-existent-group' not found in namespace '%s'", defaultNamespace)))
		})

		It("Should not block creation of other resources despite invalid GroupRef", func() {
			// Verify that deployment still gets created (GroupRef doesn't block deployment)
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, deployment)
			}, timeout, interval).Should(Succeed())

			// Verify the deployment was created successfully
			Expect(deployment.Name).To(Equal(mcpServerName))
		})
	})

	Context("When creating an MCPServer with valid GroupRef", Ordered, func() {
		var (
			namespace     string
			mcpServerName string
			mcpGroupName  string
			mcpServer     *mcpv1alpha1.MCPServer
			mcpGroup      *mcpv1alpha1.MCPGroup
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpServerName = "test-valid-groupref"
			mcpGroupName = "test-group"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create MCPGroup first
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "A test group for integration testing",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for the group to be created and ready
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Define the MCPServer resource with valid GroupRef
			mcpServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/stackloklabs/mcp-fetch:latest",
					Transport: "stdio",
					Port:      8080,
					GroupRef:  mcpGroupName, // This group exists
				},
			}

			// Create the MCPServer
			Expect(k8sClient.Create(ctx, mcpServer)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up the MCPServer first
			Expect(k8sClient.Delete(ctx, mcpServer)).Should(Succeed())
			// Then clean up the MCPGroup
			Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())
		})

		It("Should set GroupRefValidated condition to True with reason GroupRefIsValid", func() {
			// Wait for the status to be updated with the valid condition
			Eventually(func() bool {
				updatedMCPServer := &mcpv1alpha1.MCPServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: namespace,
				}, updatedMCPServer)
				if err != nil {
					return false
				}

				// Check for GroupRefValidated condition
				for _, cond := range updatedMCPServer.Status.Conditions {
					if cond.Type == conditionTypeGroupRefValidated {
						return cond.Status == metav1.ConditionTrue &&
							cond.Reason == "GroupRefIsValid"
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
				if cond.Type == conditionTypeGroupRefValidated {
					foundCondition = &updatedMCPServer.Status.Conditions[i]
					break
				}
			}

			Expect(foundCondition).NotTo(BeNil())
			Expect(foundCondition.Message).To(Equal("MCPGroup 'test-group' is valid and ready"))
		})

		It("Should update MCPGroup with server reference", func() {
			// Wait for the MCPGroup to be updated with the server reference
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				if err != nil {
					return false
				}

				// Check if the server is in the group's servers list
				for _, server := range updatedGroup.Status.Servers {
					if server == mcpServerName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
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
