// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

var _ = Describe("MCPRemoteProxy Controller Basic Functionality", Label("k8s", "remoteproxy"), func() {
	var (
		testCtx       context.Context
		proxyHelper   *MCPRemoteProxyTestHelper
		statusHelper  *RemoteProxyStatusTestHelper
		testNamespace string
	)

	BeforeEach(func() {
		testCtx = context.Background()
		testNamespace = createTestNamespace(testCtx)

		// Initialize helpers
		proxyHelper = NewMCPRemoteProxyTestHelper(testCtx, k8sClient, testNamespace)
		statusHelper = NewRemoteProxyStatusTestHelper(testCtx, k8sClient, testNamespace)
	})

	AfterEach(func() {
		// Clean up test resources
		Expect(proxyHelper.CleanupRemoteProxies()).To(Succeed())
		deleteTestNamespace(testCtx, testNamespace)
	})

	Context("Deployment Creation and Validation", func() {
		It("should create a Deployment for the MCPRemoteProxy", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-deployment").Create(proxyHelper)

			By("waiting for the Deployment to be created")
			var deployment appsv1.Deployment
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      proxy.Name,
				}, &deployment)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the Deployment has correct labels")
			Expect(deployment.Labels).To(HaveKeyWithValue("app", "mcpremoteproxy"))
			Expect(deployment.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "mcpremoteproxy"))
			Expect(deployment.Labels).To(HaveKeyWithValue("app.kubernetes.io/instance", proxy.Name))
			Expect(deployment.Labels).To(HaveKeyWithValue("toolhive", "true"))
			Expect(deployment.Labels).To(HaveKeyWithValue("toolhive-name", proxy.Name))

			By("verifying the Deployment has correct spec")
			Expect(deployment.Spec.Replicas).NotTo(BeNil())
			Expect(*deployment.Spec.Replicas).To(Equal(int32(1)))

			By("verifying the Deployment has correct selector labels")
			Expect(deployment.Spec.Selector.MatchLabels).To(HaveKeyWithValue("app", "mcpremoteproxy"))
			Expect(deployment.Spec.Selector.MatchLabels).To(HaveKeyWithValue("toolhive-name", proxy.Name))

			By("verifying the pod template has correct labels")
			Expect(deployment.Spec.Template.Labels).To(HaveKeyWithValue("app", "mcpremoteproxy"))
			Expect(deployment.Spec.Template.Labels).To(HaveKeyWithValue("toolhive", "true"))

			By("verifying the container configuration")
			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("toolhive"))
			Expect(container.Ports).To(HaveLen(1))
			Expect(container.Ports[0].ContainerPort).To(Equal(int32(8080)))
			Expect(container.Ports[0].Name).To(Equal("http"))
		})

		It("should create a Deployment with correct ServiceAccount", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-deployment-sa").Create(proxyHelper)

			By("waiting for the Deployment to be created")
			var deployment appsv1.Deployment
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      proxy.Name,
				}, &deployment)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the Deployment uses the correct ServiceAccount")
			expectedServiceAccountName := fmt.Sprintf("%s-remote-proxy-runner", proxy.Name)
			Expect(deployment.Spec.Template.Spec.ServiceAccountName).To(Equal(expectedServiceAccountName))
		})

		It("should create a Deployment with custom port", func() {
			By("creating an MCPRemoteProxy with custom port")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-custom-port").
				WithPort(9090).
				Create(proxyHelper)

			By("waiting for the Deployment to be created")
			var deployment appsv1.Deployment
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      proxy.Name,
				}, &deployment)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the container port is correct")
			Expect(deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(9090)))
		})
	})

	Context("Service Creation and Validation", func() {
		It("should create a Service for the MCPRemoteProxy", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-service").Create(proxyHelper)

			By("waiting for the Service to be created")
			serviceName := fmt.Sprintf("mcp-%s-remote-proxy", proxy.Name)
			var service corev1.Service
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      serviceName,
				}, &service)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the Service has correct labels")
			Expect(service.Labels).To(HaveKeyWithValue("app", "mcpremoteproxy"))
			Expect(service.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "mcpremoteproxy"))
			Expect(service.Labels).To(HaveKeyWithValue("app.kubernetes.io/instance", proxy.Name))
			Expect(service.Labels).To(HaveKeyWithValue("toolhive", "true"))

			By("verifying the Service port configuration")
			Expect(service.Spec.Ports).To(HaveLen(1))
			Expect(service.Spec.Ports[0].Port).To(Equal(int32(8080)))
			Expect(service.Spec.Ports[0].Name).To(Equal("http"))

			By("verifying the Service selector")
			Expect(service.Spec.Selector).To(HaveKeyWithValue("app", "mcpremoteproxy"))
			Expect(service.Spec.Selector).To(HaveKeyWithValue("toolhive-name", proxy.Name))
		})

		It("should create a Service with custom port", func() {
			By("creating an MCPRemoteProxy with custom port")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-service-port").
				WithPort(9090).
				Create(proxyHelper)

			By("waiting for the Service to be created")
			serviceName := fmt.Sprintf("mcp-%s-remote-proxy", proxy.Name)
			var service corev1.Service
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      serviceName,
				}, &service)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the Service port is correct")
			Expect(service.Spec.Ports[0].Port).To(Equal(int32(9090)))
		})
	})

	Context("ConfigMap Creation and Validation", func() {
		It("should create a RunConfig ConfigMap for the MCPRemoteProxy", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-configmap").Create(proxyHelper)

			By("waiting for the RunConfig ConfigMap to be created")
			configMapName := fmt.Sprintf("%s-runconfig", proxy.Name)
			var configMap corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      configMapName,
				}, &configMap)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the ConfigMap has correct labels")
			Expect(configMap.Labels).To(HaveKeyWithValue("toolhive.stacklok.io/component", "run-config"))
			Expect(configMap.Labels).To(HaveKeyWithValue("toolhive.stacklok.io/mcp-remote-proxy", proxy.Name))
			Expect(configMap.Labels).To(HaveKeyWithValue("toolhive.stacklok.io/managed-by", "toolhive-operator"))

			By("verifying the ConfigMap has runconfig.json data")
			Expect(configMap.Data).To(HaveKey("runconfig.json"))

			By("verifying the RunConfig content")
			var runConfig runner.RunConfig
			err := json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)
			Expect(err).NotTo(HaveOccurred())

			// Verify key RunConfig fields match the MCPRemoteProxy spec
			Expect(runConfig.Name).To(Equal(proxy.Name))
			Expect(runConfig.RemoteURL).To(Equal("https://remote.example.com/mcp"))
			Expect(runConfig.Transport.String()).To(Equal("streamable-http"))
			Expect(runConfig.Port).To(Equal(8080))
			Expect(runConfig.Host).To(Equal("0.0.0.0"))

			By("verifying the ConfigMap has correct owner reference")
			Expect(configMap.OwnerReferences).To(HaveLen(1))
			Expect(configMap.OwnerReferences[0].Name).To(Equal(proxy.Name))
			Expect(configMap.OwnerReferences[0].Kind).To(Equal("MCPRemoteProxy"))
		})
	})

	Context("RBAC Resource Creation", func() {
		It("should create ServiceAccount for the MCPRemoteProxy", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-rbac-sa").Create(proxyHelper)

			By("waiting for the ServiceAccount to be created")
			saName := fmt.Sprintf("%s-remote-proxy-runner", proxy.Name)
			var sa corev1.ServiceAccount
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      saName,
				}, &sa)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the ServiceAccount exists")
			Expect(sa.Name).To(Equal(saName))
		})

		It("should create Role for the MCPRemoteProxy", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-rbac-role").Create(proxyHelper)

			By("waiting for the Role to be created")
			roleName := fmt.Sprintf("%s-remote-proxy-runner", proxy.Name)
			var role rbacv1.Role
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      roleName,
				}, &role)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the Role exists")
			Expect(role.Name).To(Equal(roleName))
		})

		It("should create RoleBinding for the MCPRemoteProxy", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-rbac-binding").Create(proxyHelper)

			By("waiting for the RoleBinding to be created")
			rbName := fmt.Sprintf("%s-remote-proxy-runner", proxy.Name)
			var roleBinding rbacv1.RoleBinding
			Eventually(func() error {
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      rbName,
				}, &roleBinding)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying the RoleBinding configuration")
			Expect(roleBinding.Name).To(Equal(rbName))
			Expect(roleBinding.RoleRef.Kind).To(Equal("Role"))
			Expect(roleBinding.RoleRef.Name).To(Equal(rbName))
			Expect(roleBinding.Subjects).To(HaveLen(1))
			Expect(roleBinding.Subjects[0].Kind).To(Equal("ServiceAccount"))
			Expect(roleBinding.Subjects[0].Name).To(Equal(rbName))
		})
	})

	Context("Status Condition Tracking", func() {
		It("should set Ready condition based on deployment status", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-ready-condition").Create(proxyHelper)

			By("waiting for Ready condition to be set")
			Eventually(func() bool {
				condition, err := proxyHelper.GetRemoteProxyCondition(proxy.Name, mcpv1alpha1.ConditionTypeReady)
				if err != nil {
					return false
				}
				return condition != nil
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("verifying the Ready condition is set (initially False as deployment is not ready)")
			condition, err := proxyHelper.GetRemoteProxyCondition(proxy.Name, mcpv1alpha1.ConditionTypeReady)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition).NotTo(BeNil())
			// Initially the condition will be False because the deployment pods won't be running in envtest
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonDeploymentNotReady))
		})

		It("should set Pending phase initially", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-pending-phase").Create(proxyHelper)

			By("waiting for status to be updated")
			statusHelper.WaitForPhaseAny(proxy.Name, []mcpv1alpha1.MCPRemoteProxyPhase{
				mcpv1alpha1.MCPRemoteProxyPhasePending,
				mcpv1alpha1.MCPRemoteProxyPhaseReady,
			}, MediumTimeout)

			By("verifying the phase is Pending (since deployment is not ready in envtest)")
			phase, err := proxyHelper.GetRemoteProxyPhase(proxy.Name)
			Expect(err).NotTo(HaveOccurred())
			// In envtest, pods don't actually run so phase will be Pending
			Expect(phase).To(Equal(mcpv1alpha1.MCPRemoteProxyPhasePending))
		})

		It("should update ObservedGeneration in status", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-observed-gen").Create(proxyHelper)

			By("waiting for ObservedGeneration to be set")
			Eventually(func() int64 {
				status, err := proxyHelper.GetRemoteProxyStatus(proxy.Name)
				if err != nil {
					return -1
				}
				return status.ObservedGeneration
			}, MediumTimeout, DefaultPollingInterval).Should(BeNumerically(">", 0))

			By("verifying ObservedGeneration matches resource generation")
			updatedProxy, err := proxyHelper.GetRemoteProxy(proxy.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedProxy.Status.ObservedGeneration).To(Equal(updatedProxy.Generation))
		})

		It("should set service URL in status", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-service-url").Create(proxyHelper)

			By("waiting for URL to be set in status")
			statusHelper.WaitForURL(proxy.Name, MediumTimeout)

			By("verifying the URL format")
			status, err := proxyHelper.GetRemoteProxyStatus(proxy.Name)
			Expect(err).NotTo(HaveOccurred())
			expectedURL := fmt.Sprintf("http://mcp-%s-remote-proxy.%s.svc.cluster.local:8080",
				proxy.Name, testNamespace)
			Expect(status.URL).To(Equal(expectedURL))
		})

		It("should set AuthConfigured condition for valid OIDC config", func() {
			By("creating an MCPRemoteProxy with valid OIDC config")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-auth-configured").Create(proxyHelper)

			By("waiting for controller to process the resource")
			Eventually(func() bool {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return false
				}
				return p.Status.Phase != ""
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("verifying that no AuthConfigured=False condition exists (valid config)")
			// With valid config, the controller should not set AuthConfigured to False
			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeAuthConfigured,
			)
			if err == nil {
				// If condition exists, it should not be False with AuthInvalid reason
				Expect(condition.Status).NotTo(Equal(metav1.ConditionFalse))
			}
			// It's also valid for the condition to not exist if auth is valid
		})
	})

	Context("Status Message Updates", func() {
		It("should set appropriate status message", func() {
			By("creating an MCPRemoteProxy")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-status-message").Create(proxyHelper)

			By("waiting for status message to be set")
			Eventually(func() string {
				status, err := proxyHelper.GetRemoteProxyStatus(proxy.Name)
				if err != nil {
					return ""
				}
				return status.Message
			}, MediumTimeout, DefaultPollingInterval).ShouldNot(BeEmpty())

			By("verifying the status message is set")
			status, err := proxyHelper.GetRemoteProxyStatus(proxy.Name)
			Expect(err).NotTo(HaveOccurred())
			// In envtest, pods don't run, so we expect the "starting" or "no pods" message
			Expect(status.Message).To(Or(
				ContainSubstring("starting"),
				ContainSubstring("No pods found"),
			))
		})
	})
})

// Helper function to create test namespace
func createTestNamespace(ctx context.Context) string {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-remote-proxy-",
			Labels: map[string]string{
				"test.toolhive.io/suite": "operator-e2e",
			},
		},
	}

	Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
	return namespace.Name
}

// Helper function to delete test namespace
func deleteTestNamespace(ctx context.Context, name string) {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	By(fmt.Sprintf("deleting namespace %s", name))
	_ = k8sClient.Delete(ctx, namespace)
	By(fmt.Sprintf("deleted namespace %s", name))
}

var _ = Describe("MCPRemoteProxy Controller Integration with Other Resources", Label("k8s", "remoteproxy", "integration"), func() {
	var (
		testCtx       context.Context
		proxyHelper   *MCPRemoteProxyTestHelper
		statusHelper  *RemoteProxyStatusTestHelper
		testNamespace string
	)

	BeforeEach(func() {
		testCtx = context.Background()
		testNamespace = createTestNamespace(testCtx)

		// Initialize helpers
		proxyHelper = NewMCPRemoteProxyTestHelper(testCtx, k8sClient, testNamespace)
		statusHelper = NewRemoteProxyStatusTestHelper(testCtx, k8sClient, testNamespace)
	})

	AfterEach(func() {
		// Clean up test resources
		Expect(proxyHelper.CleanupRemoteProxies()).To(Succeed())
		deleteTestNamespace(testCtx, testNamespace)
	})

	Context("ExternalAuthConfigRef Integration", func() {
		It("should fail validation when referenced MCPExternalAuthConfig does not exist", func() {
			By("creating an MCPRemoteProxy referencing non-existent MCPExternalAuthConfig")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-ext-auth-missing").
				WithExternalAuthConfigRef("non-existent-auth-config").
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase due to missing ExternalAuthConfig")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying the error message indicates the config was not found")
			status, err := proxyHelper.GetRemoteProxyStatus(proxy.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Message).To(ContainSubstring("non-existent-auth-config"))

			By("verifying the AuthConfigured condition is False")
			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeAuthConfigured,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonAuthInvalid))
		})

		It("should successfully reconcile when referenced MCPExternalAuthConfig exists", func() {
			By("creating an MCPExternalAuthConfig")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth-config",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "api-key-secret",
							Key:  "key",
						},
					},
				},
			}
			Expect(k8sClient.Create(testCtx, authConfig)).To(Succeed())

			By("waiting for MCPExternalAuthConfig to have a ConfigHash")
			Eventually(func() string {
				config := &mcpv1alpha1.MCPExternalAuthConfig{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      authConfig.Name,
				}, config); err != nil {
					return ""
				}
				return config.Status.ConfigHash
			}, MediumTimeout, DefaultPollingInterval).ShouldNot(BeEmpty())

			By("creating an MCPRemoteProxy referencing the MCPExternalAuthConfig")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-ext-auth-valid").
				WithExternalAuthConfigRef("test-auth-config").
				Create(proxyHelper)

			By("waiting for the proxy to be reconciled")
			Eventually(func() bool {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return false
				}
				// Phase should be Pending (not Failed) and ExternalAuthConfigHash should be set
				return p.Status.Phase == mcpv1alpha1.MCPRemoteProxyPhasePending &&
					p.Status.ExternalAuthConfigHash != ""
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("verifying the ExternalAuthConfigHash is tracked in status")
			status, err := proxyHelper.GetRemoteProxyStatus(proxy.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.ExternalAuthConfigHash).NotTo(BeEmpty())

			By("verifying the ExternalAuthConfigValidated condition is True")
			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonMCPRemoteProxyExternalAuthConfigValid))
			Expect(condition.Message).To(ContainSubstring("test-auth-config"))

			By("cleaning up the auth config")
			Expect(k8sClient.Delete(testCtx, authConfig)).To(Succeed())
		})

		It("should trigger reconciliation when MCPExternalAuthConfig is updated", func() {
			By("creating an MCPExternalAuthConfig")
			authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth-update",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-Original-Header",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "original-secret",
							Key:  "key",
						},
					},
				},
			}
			Expect(k8sClient.Create(testCtx, authConfig)).To(Succeed())

			By("waiting for MCPExternalAuthConfig to have a ConfigHash")
			var originalHash string
			Eventually(func() string {
				config := &mcpv1alpha1.MCPExternalAuthConfig{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      authConfig.Name,
				}, config); err != nil {
					return ""
				}
				originalHash = config.Status.ConfigHash
				return originalHash
			}, MediumTimeout, DefaultPollingInterval).ShouldNot(BeEmpty())

			By("creating an MCPRemoteProxy referencing the MCPExternalAuthConfig")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-ext-auth-update").
				WithExternalAuthConfigRef("test-auth-update").
				Create(proxyHelper)

			By("waiting for the proxy to track the auth config hash")
			Eventually(func() string {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return ""
				}
				return p.Status.ExternalAuthConfigHash
			}, MediumTimeout, DefaultPollingInterval).Should(Equal(originalHash))

			By("updating the MCPExternalAuthConfig")
			Eventually(func() error {
				config := &mcpv1alpha1.MCPExternalAuthConfig{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      authConfig.Name,
				}, config); err != nil {
					return err
				}
				config.Spec.HeaderInjection.HeaderName = "X-Updated-Header"
				return k8sClient.Update(testCtx, config)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("waiting for the auth config hash to change")
			Eventually(func() string {
				config := &mcpv1alpha1.MCPExternalAuthConfig{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      authConfig.Name,
				}, config); err != nil {
					return originalHash
				}
				return config.Status.ConfigHash
			}, MediumTimeout, DefaultPollingInterval).ShouldNot(Equal(originalHash))

			By("verifying the proxy's ExternalAuthConfigHash is updated")
			Eventually(func() bool {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return false
				}
				return p.Status.ExternalAuthConfigHash != originalHash &&
					p.Status.ExternalAuthConfigHash != ""
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("cleaning up the auth config")
			Expect(k8sClient.Delete(testCtx, authConfig)).To(Succeed())
		})
	})

	Context("ToolConfigRef Integration", func() {
		It("should fail validation when referenced MCPToolConfig does not exist", func() {
			By("creating an MCPRemoteProxy referencing non-existent MCPToolConfig")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-tool-config-missing").
				WithToolConfigRef("non-existent-tool-config").
				Create(proxyHelper)

			By("waiting for the proxy to reach Failed phase due to missing ToolConfig")
			statusHelper.WaitForPhase(proxy.Name, mcpv1alpha1.MCPRemoteProxyPhaseFailed, MediumTimeout)

			By("verifying the ToolConfigValidated condition indicates not found")
			Eventually(func() bool {
				condition, err := proxyHelper.GetRemoteProxyCondition(
					proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyToolConfigValidated,
				)
				if err != nil {
					return false
				}
				return condition.Status == metav1.ConditionFalse &&
					condition.Reason == mcpv1alpha1.ConditionReasonMCPRemoteProxyToolConfigNotFound
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyToolConfigValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Message).To(ContainSubstring("non-existent-tool-config"))
		})

		It("should successfully reconcile when referenced MCPToolConfig exists", func() {
			By("creating an MCPToolConfig")
			toolConfig := &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tool-config",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
			}
			Expect(k8sClient.Create(testCtx, toolConfig)).To(Succeed())

			By("waiting for MCPToolConfig to have a ConfigHash")
			Eventually(func() string {
				config := &mcpv1alpha1.MCPToolConfig{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      toolConfig.Name,
				}, config); err != nil {
					return ""
				}
				return config.Status.ConfigHash
			}, MediumTimeout, DefaultPollingInterval).ShouldNot(BeEmpty())

			By("creating an MCPRemoteProxy referencing the MCPToolConfig")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-tool-config-valid").
				WithToolConfigRef("test-tool-config").
				Create(proxyHelper)

			By("waiting for the proxy to be reconciled")
			Eventually(func() bool {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return false
				}
				// Phase should be Pending (not Failed) and ToolConfigHash should be set
				return p.Status.Phase == mcpv1alpha1.MCPRemoteProxyPhasePending &&
					p.Status.ToolConfigHash != ""
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("verifying the ToolConfigHash is tracked in status")
			status, err := proxyHelper.GetRemoteProxyStatus(proxy.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.ToolConfigHash).NotTo(BeEmpty())

			By("verifying the ToolConfigValidated condition is True")
			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyToolConfigValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonMCPRemoteProxyToolConfigValid))
			Expect(condition.Message).To(ContainSubstring("test-tool-config"))

			By("cleaning up the tool config")
			Expect(k8sClient.Delete(testCtx, toolConfig)).To(Succeed())
		})

		It("should propagate tool config changes to the RunConfig ConfigMap", func() {
			By("creating an MCPToolConfig with initial filter")
			toolConfig := &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tool-propagate",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"initial-tool"},
				},
			}
			Expect(k8sClient.Create(testCtx, toolConfig)).To(Succeed())

			By("waiting for MCPToolConfig to have a ConfigHash")
			var initialHash string
			Eventually(func() string {
				config := &mcpv1alpha1.MCPToolConfig{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      toolConfig.Name,
				}, config); err != nil {
					return ""
				}
				initialHash = config.Status.ConfigHash
				return initialHash
			}, MediumTimeout, DefaultPollingInterval).ShouldNot(BeEmpty())

			By("creating an MCPRemoteProxy referencing the MCPToolConfig")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-tool-propagate").
				WithToolConfigRef("test-tool-propagate").
				Create(proxyHelper)

			By("waiting for the proxy to track the tool config hash")
			Eventually(func() string {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return ""
				}
				return p.Status.ToolConfigHash
			}, MediumTimeout, DefaultPollingInterval).Should(Equal(initialHash))

			By("verifying initial RunConfig ConfigMap exists")
			configMapName := fmt.Sprintf("%s-runconfig", proxy.Name)
			Eventually(func() error {
				cm := &corev1.ConfigMap{}
				return k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      configMapName,
				}, cm)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("updating the MCPToolConfig with new filter")
			Eventually(func() error {
				config := &mcpv1alpha1.MCPToolConfig{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      toolConfig.Name,
				}, config); err != nil {
					return err
				}
				config.Spec.ToolsFilter = []string{"updated-tool-1", "updated-tool-2"}
				return k8sClient.Update(testCtx, config)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("waiting for the tool config hash to change")
			Eventually(func() string {
				config := &mcpv1alpha1.MCPToolConfig{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      toolConfig.Name,
				}, config); err != nil {
					return initialHash
				}
				return config.Status.ConfigHash
			}, MediumTimeout, DefaultPollingInterval).ShouldNot(Equal(initialHash))

			By("verifying the proxy's ToolConfigHash is updated")
			Eventually(func() bool {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return false
				}
				return p.Status.ToolConfigHash != initialHash &&
					p.Status.ToolConfigHash != ""
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("cleaning up the tool config")
			Expect(k8sClient.Delete(testCtx, toolConfig)).To(Succeed())
		})
	})

	Context("GroupRef Integration", func() {
		It("should set GroupRefValidated condition to False when referenced MCPGroup does not exist", func() {
			By("creating an MCPRemoteProxy referencing non-existent MCPGroup")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-group-missing").
				WithGroupRef("non-existent-group").
				Create(proxyHelper)

			By("waiting for the GroupRefValidated condition to be set")
			Eventually(func() bool {
				condition, err := proxyHelper.GetRemoteProxyCondition(
					proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
				)
				if err != nil {
					return false
				}
				return condition != nil
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("verifying the GroupRefValidated condition is False")
			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonMCPRemoteProxyGroupRefNotFound))
			Expect(condition.Message).To(ContainSubstring("non-existent-group"))
		})

		It("should set GroupRefValidated condition to True when referenced MCPGroup exists and is Ready", func() {
			By("creating an MCPGroup")
			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group-valid",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for MCPRemoteProxy integration",
				},
			}
			Expect(k8sClient.Create(testCtx, mcpGroup)).To(Succeed())

			By("waiting for the MCPGroup to be Ready")
			Eventually(func() mcpv1alpha1.MCPGroupPhase {
				group := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      mcpGroup.Name,
				}, group); err != nil {
					return ""
				}
				return group.Status.Phase
			}, MediumTimeout, DefaultPollingInterval).Should(Equal(mcpv1alpha1.MCPGroupPhaseReady))

			By("creating an MCPRemoteProxy referencing the MCPGroup")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-group-valid").
				WithGroupRef("test-group-valid").
				Create(proxyHelper)

			By("waiting for the GroupRefValidated condition to be True")
			Eventually(func() bool {
				condition, err := proxyHelper.GetRemoteProxyCondition(
					proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
				)
				if err != nil {
					return false
				}
				return condition.Status == metav1.ConditionTrue
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("verifying the GroupRefValidated condition details")
			condition, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonMCPRemoteProxyGroupRefValidated))
			Expect(condition.Message).To(ContainSubstring("test-group-valid"))
			Expect(condition.Message).To(ContainSubstring("valid and ready"))

			By("cleaning up the MCPGroup")
			Expect(k8sClient.Delete(testCtx, mcpGroup)).To(Succeed())
		})

		// Note: Testing "MCPGroup is not Ready" is difficult because the MCPGroup controller
		// immediately reconciles empty groups to Ready state. The NotReady state only occurs
		// when the group contains servers that are not ready, which is complex to set up.
		// The GroupRefNotFound case (tested above) covers the validation failure path.

		It("should not have GroupRefValidated condition when no GroupRef is specified", func() {
			By("creating an MCPRemoteProxy without GroupRef")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-no-group").Create(proxyHelper)

			By("waiting for the proxy to be reconciled")
			Eventually(func() bool {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return false
				}
				return p.Status.Phase != ""
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("verifying no GroupRefValidated condition exists")
			_, err := proxyHelper.GetRemoteProxyCondition(
				proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("should update GroupRefValidated condition when MCPGroup is created", func() {
			By("creating an MCPRemoteProxy referencing a non-existent MCPGroup")
			proxy := proxyHelper.NewRemoteProxyBuilder("test-group-trans").
				WithGroupRef("test-group-transition").
				Create(proxyHelper)

			By("waiting for the GroupRefValidated condition to be False (NotFound)")
			Eventually(func() bool {
				condition, err := proxyHelper.GetRemoteProxyCondition(
					proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
				)
				if err != nil {
					return false
				}
				return condition.Status == metav1.ConditionFalse &&
					condition.Reason == mcpv1alpha1.ConditionReasonMCPRemoteProxyGroupRefNotFound
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("creating the MCPGroup that was referenced")
			mcpGroup := &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-group-transition",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for transition testing",
				},
			}
			Expect(k8sClient.Create(testCtx, mcpGroup)).To(Succeed())

			By("waiting for the MCPGroup to become Ready")
			Eventually(func() mcpv1alpha1.MCPGroupPhase {
				group := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(testCtx, types.NamespacedName{
					Namespace: testNamespace,
					Name:      mcpGroup.Name,
				}, group); err != nil {
					return ""
				}
				return group.Status.Phase
			}, MediumTimeout, DefaultPollingInterval).Should(Equal(mcpv1alpha1.MCPGroupPhaseReady))

			By("triggering reconciliation by updating the proxy")
			Eventually(func() error {
				p, err := proxyHelper.GetRemoteProxy(proxy.Name)
				if err != nil {
					return err
				}
				if p.Annotations == nil {
					p.Annotations = make(map[string]string)
				}
				p.Annotations["test.toolhive.io/trigger"] = "reconcile"
				return k8sClient.Update(testCtx, p)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("waiting for the GroupRefValidated condition to become True")
			Eventually(func() bool {
				condition, err := proxyHelper.GetRemoteProxyCondition(
					proxy.Name, mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
				)
				if err != nil {
					return false
				}
				return condition.Status == metav1.ConditionTrue &&
					condition.Reason == mcpv1alpha1.ConditionReasonMCPRemoteProxyGroupRefValidated
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("cleaning up the MCPGroup")
			Expect(k8sClient.Delete(testCtx, mcpGroup)).To(Succeed())
		})
	})
})
