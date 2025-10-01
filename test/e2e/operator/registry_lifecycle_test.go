package operator_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	registryFinalizerName = "mcpregistry.toolhive.stacklok.dev/finalizer"
)

var _ = Describe("MCPRegistry Lifecycle Management", func() {
	var (
		ctx             context.Context
		registryHelper  *MCPRegistryTestHelper
		configMapHelper *ConfigMapTestHelper
		statusHelper    *StatusTestHelper
		timingHelper    *TimingTestHelper
		k8sHelper       *K8sResourceTestHelper
		testNamespace   string
	)

	BeforeEach(func() {
		ctx = context.Background()
		testNamespace = createTestNamespace(ctx)

		// Initialize helpers
		registryHelper = NewMCPRegistryTestHelper(ctx, k8sClient, testNamespace)
		configMapHelper = NewConfigMapTestHelper(ctx, k8sClient, testNamespace)
		statusHelper = NewStatusTestHelper(ctx, k8sClient, testNamespace)
		timingHelper = NewTimingTestHelper(ctx, k8sClient)
		k8sHelper = NewK8sResourceTestHelper(ctx, k8sClient, testNamespace)
	})

	AfterEach(func() {
		// Clean up test resources
		Expect(registryHelper.CleanupRegistries()).To(Succeed())
		Expect(configMapHelper.CleanupConfigMaps()).To(Succeed())
		deleteTestNamespace(ctx, testNamespace)
	})

	Context("Basic Registry Creation", func() {
		It("should create MCPRegistry with correct initial status", func() {
			// Create test ConfigMap
			configMap, numServers := configMapHelper.CreateSampleToolHiveRegistry("test-config")

			// Create MCPRegistry
			registry := registryHelper.NewRegistryBuilder("test-registry").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			// Verify registry was created
			Expect(registry.Name).To(Equal("test-registry"))
			Expect(registry.Namespace).To(Equal(testNamespace))

			// Verify initial spec
			Expect(registry.Spec.Source.Type).To(Equal(mcpv1alpha1.RegistrySourceTypeConfigMap))
			Expect(registry.Spec.Source.ConfigMap.Name).To(Equal(configMap.Name))
			Expect(registry.Spec.SyncPolicy.Interval).To(Equal("1h"))

			// Wait for registry initialization (finalizer + initial status)
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			By("verifying storage ConfigMap is defined in status and exists")
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			// Verify storage reference is set in status
			Expect(updatedRegistry.Status.StorageRef).NotTo(BeNil())
			Expect(updatedRegistry.Status.StorageRef.Type).To(Equal("configmap"))
			Expect(updatedRegistry.Status.StorageRef.ConfigMapRef).NotTo(BeNil())
			Expect(updatedRegistry.Status.StorageRef.ConfigMapRef.Name).NotTo(BeEmpty())

			// Verify the storage ConfigMap actually exists
			storageConfigMapName := updatedRegistry.Status.StorageRef.ConfigMapRef.Name
			storageConfigMap, err := k8sHelper.GetConfigMap(storageConfigMapName)
			Expect(err).NotTo(HaveOccurred())
			Expect(storageConfigMap.Name).To(Equal(storageConfigMapName))
			Expect(storageConfigMap.Namespace).To(Equal(testNamespace))

			// Verify it has the registry.json key
			Expect(storageConfigMap.Data).To(HaveKey("registry.json"))
			Expect(storageConfigMap.Data["registry.json"]).NotTo(BeEmpty())

			By("verifying Registry API Service and Deployment exist")
			apiResourceName := updatedRegistry.GetAPIResourceName()

			// Wait for Service to be created
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				return k8sHelper.ServiceExists(apiResourceName)
			}).Should(BeTrue(), "Registry API Service should exist")

			// Wait for Deployment to be created
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				return k8sHelper.DeploymentExists(apiResourceName)
			}).Should(BeTrue(), "Registry API Deployment should exist")

			// Verify the Service has correct configuration
			service, err := k8sHelper.GetService(apiResourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(service.Name).To(Equal(apiResourceName))
			Expect(service.Namespace).To(Equal(testNamespace))
			Expect(service.Spec.Ports).To(HaveLen(1))
			Expect(service.Spec.Ports[0].Name).To(Equal("http"))

			// Verify the Deployment has correct configuration
			deployment, err := k8sHelper.GetDeployment(apiResourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(deployment.Name).To(Equal(apiResourceName))
			Expect(deployment.Namespace).To(Equal(testNamespace))
			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deployment.Spec.Template.Spec.Containers[0].Name).To(Equal("registry-api"))

			By("verifying deployment has proper ownership")
			Expect(deployment.OwnerReferences).To(HaveLen(1))
			Expect(deployment.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
			Expect(deployment.OwnerReferences[0].Name).To(Equal(registry.Name))

			By("verifying registry status")
			updatedRegistry, err = registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			// In envtest, the deployment won't actually be ready, so expect Pending phase
			// but verify that sync is complete and API deployment is in progress
			Expect(updatedRegistry.Status.Phase).To(BeElementOf(
				mcpv1alpha1.MCPRegistryPhasePending, // API deployment in progress
				mcpv1alpha1.MCPRegistryPhaseReady,   // If somehow API becomes ready
			))

			// Verify sync is complete
			Expect(updatedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			Expect(updatedRegistry.Status.SyncStatus.AttemptCount).To(Equal(0))
			Expect(updatedRegistry.Status.SyncStatus.ServerCount).To(Equal(numServers))

			// Verify API status exists and shows deployment
			Expect(updatedRegistry.Status.APIStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.APIStatus.Phase).To(BeElementOf(
				mcpv1alpha1.APIPhaseDeploying, // Deployment created but not ready
				mcpv1alpha1.APIPhaseReady,     // If somehow becomes ready
			))
			if updatedRegistry.Status.APIStatus.Phase == mcpv1alpha1.APIPhaseReady {
				Expect(updatedRegistry.Status.APIStatus.Endpoint).To(Equal(fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", apiResourceName, testNamespace)))
			}
			By("BYE")
		})

		It("should handle registry with minimal configuration", func() {
			// Create minimal ConfigMap
			configMap := configMapHelper.NewConfigMapBuilder("minimal-config").
				WithToolHiveRegistry("registry.json", []RegistryServer{
					{
						Name:        "test-server",
						Description: "Test server",
						Tier:        "Community",
						Status:      "Active",
						Transport:   "stdio",
						Tools:       []string{"test_tool"},
						Image:       "test/server:1.0.0",
					},
				}).
				Create(configMapHelper)

			// Create minimal registry (no sync policy)
			registry := registryHelper.NewRegistryBuilder("minimal-registry").
				WithConfigMapSource(configMap.Name, "registry.json").
				Create(registryHelper)

			// Verify creation
			Expect(registry.Spec.SyncPolicy).To(BeNil())

			// Wait for registry initialization (finalizer + initial status)
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			// Verify sync status is or complete
			Expect(updatedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			Expect(updatedRegistry.Status.SyncStatus.ServerCount).To(Equal(1))
		})

		It("should set correct metadata labels and annotations", func() {
			configMap, _ := configMapHelper.CreateSampleToolHiveRegistry("labeled-config")

			registry := registryHelper.NewRegistryBuilder("labeled-registry").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithLabel("app", "test").
				WithLabel("version", "1.0").
				WithAnnotation("description", "Test registry").
				Create(registryHelper)

			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			// Verify labels and annotations
			Expect(registry.Labels).To(HaveKeyWithValue("app", "test"))
			Expect(registry.Labels).To(HaveKeyWithValue("version", "1.0"))
			Expect(registry.Annotations).To(HaveKeyWithValue("description", "Test registry"))
		})
	})

	Context("Finalizer Management", func() {
		It("should add finalizer on creation", func() {
			configMap, _ := configMapHelper.CreateSampleToolHiveRegistry("finalizer-config")

			registry := registryHelper.NewRegistryBuilder("finalizer-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				Create(registryHelper)

			// Wait for finalizer to be added
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
				if err != nil {
					return false
				}
				return containsFinalizer(updatedRegistry.Finalizers, registryFinalizerName)
			}).Should(BeTrue())
		})

		It("should remove finalizer during deletion", func() {
			configMap, _ := configMapHelper.CreateSampleToolHiveRegistry("deletion-config")

			registry := registryHelper.NewRegistryBuilder("deletion-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				Create(registryHelper)

			// Wait for finalizer to be added
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
				if err != nil {
					return false
				}
				return containsFinalizer(updatedRegistry.Finalizers, registryFinalizerName)
			}).Should(BeTrue())

			// Delete the registry
			Expect(registryHelper.DeleteRegistry(registry.Name)).To(Succeed())

			// Verify registry enters terminating phase
			By("waiting for registry to enter terminating phase")
			statusHelper.WaitForPhase(registry.Name, mcpv1alpha1.MCPRegistryPhaseTerminating, MediumTimeout)

			By("waiting for finalizer to be removed")
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
				if err != nil {
					return true // Registry might be deleted, which means finalizer was removed
				}
				return !containsFinalizer(updatedRegistry.Finalizers, registryFinalizerName)
			}).Should(BeTrue())

			// Verify registry is eventually deleted (finalizer removed)
			By("waiting for registry to be deleted")
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registry.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Context("Deletion Handling", func() {
		It("should perform graceful deletion with cleanup", func() {
			configMap, _ := configMapHelper.CreateSampleToolHiveRegistry("cleanup-config")

			registry := registryHelper.NewRegistryBuilder("cleanup-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("30m").
				Create(registryHelper)

			// Wait for registry to be ready
			statusHelper.WaitForPhaseAny(registry.Name, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, MediumTimeout)

			// Store initial storage reference for cleanup verification
			status, err := registryHelper.GetRegistryStatus(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			initialStorageRef := status.StorageRef

			// Delete the registry
			Expect(registryHelper.DeleteRegistry(registry.Name)).To(Succeed())

			// Verify graceful deletion process
			statusHelper.WaitForPhase(registry.Name, mcpv1alpha1.MCPRegistryPhaseTerminating, QuickTimeout)

			// Verify cleanup of associated resources (if any storage was created)
			if initialStorageRef != nil && initialStorageRef.ConfigMapRef != nil {
				timingHelper.WaitForControllerReconciliation(func() interface{} {
					_, err := configMapHelper.GetConfigMap(initialStorageRef.ConfigMapRef.Name)
					// Storage ConfigMap should be cleaned up or marked for deletion
					return errors.IsNotFound(err)
				}).Should(BeTrue())
			}

			// Verify complete deletion
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registry.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("should handle deletion when source ConfigMap is missing", func() {
			configMap, _ := configMapHelper.CreateSampleToolHiveRegistry("missing-config")

			registry := registryHelper.NewRegistryBuilder("missing-source-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				Create(registryHelper)

			// Delete the source ConfigMap first
			Expect(configMapHelper.DeleteConfigMap(configMap.Name)).To(Succeed())

			// Now delete the registry - should still succeed
			Expect(registryHelper.DeleteRegistry(registry.Name)).To(Succeed())

			// Verify deletion completes despite missing source
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registry.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Context("Spec Validation", func() {
		It("should reject invalid source configuration", func() {
			// Try to create registry with missing ConfigMap reference
			invalidRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-registry",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
						// Missing ConfigMap field
					},
				},
			}

			// Should fail validation
			err := k8sClient.Create(ctx, invalidRegistry)
			By("verifying validation error")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("configMap field is required"))
		})

		It("should reject invalid sync interval", func() {
			configMap, _ := configMapHelper.CreateSampleToolHiveRegistry("interval-config")

			invalidRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-interval",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: configMap.Name,
							Key:  "registry.json",
						},
					},
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "invalid-duration",
					},
				},
			}

			// Should fail validation
			err := k8sClient.Create(ctx, invalidRegistry)
			Expect(err).To(HaveOccurred())
		})

		It("should handle missing source ConfigMap gracefully", func() {
			registry := registryHelper.NewRegistryBuilder("missing-configmap").
				WithConfigMapSource("nonexistent-configmap", "registry.json").
				Create(registryHelper)

			By("waiting for registry to enter failed state")
			// Should enter failed state due to missing source
			statusHelper.WaitForPhase(registry.Name, mcpv1alpha1.MCPRegistryPhaseFailed, MediumTimeout)

			// Check condition reflects the problem
			statusHelper.WaitForCondition(registry.Name, mcpv1alpha1.ConditionSyncSuccessful,
				metav1.ConditionFalse, MediumTimeout)

			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			By("verifying sync status")
			Expect(updatedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseFailed))
			Expect(updatedRegistry.Status.SyncStatus.AttemptCount).To(Equal(1))

			By("verifying API status")
			Expect(updatedRegistry.Status.APIStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.APIStatus.Phase).To(Equal(mcpv1alpha1.APIPhaseDeploying))
			Expect(updatedRegistry.Status.APIStatus.Endpoint).To(BeEmpty())
		})
	})

	Context("Multiple Registry Management", func() {
		var configMap1, configMap2 *corev1.ConfigMap
		It("should handle multiple registries in same namespace", func() {
			// Create multiple ConfigMaps
			configMap1, _ = configMapHelper.CreateSampleToolHiveRegistry("config-1")
			configMap2, _ = configMapHelper.CreateSampleToolHiveRegistry("config-2")

			// Create multiple registries
			registry1 := registryHelper.NewRegistryBuilder("registry-1").
				WithConfigMapSource(configMap1.Name, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			registry2 := registryHelper.NewRegistryBuilder("registry-2").
				WithConfigMapSource(configMap2.Name, "registry.json").
				// WithUpstreamFormat().
				WithSyncPolicy("30m").
				Create(registryHelper)

			// Both should become ready independently
			statusHelper.WaitForPhaseAny(registry1.Name, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, MediumTimeout)
			statusHelper.WaitForPhaseAny(registry2.Name, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, MediumTimeout)

			// Verify they operate independently
			Expect(registry1.Spec.SyncPolicy.Interval).To(Equal("1h"))
			Expect(registry2.Spec.SyncPolicy.Interval).To(Equal("30m"))
			Expect(registry1.Spec.Source.Format).To(Equal(mcpv1alpha1.RegistryFormatToolHive))
			Expect(registry2.Spec.Source.Format).To(Equal(mcpv1alpha1.RegistryFormatToolHive))
		})

		It("should allow multiple registries with same ConfigMap source", func() {
			// Create shared ConfigMap
			sharedConfigMap, _ := configMapHelper.CreateSampleToolHiveRegistry("shared-config")

			// Create multiple registries using same source
			registry1 := registryHelper.NewRegistryBuilder("shared-registry-1").
				WithConfigMapSource(sharedConfigMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			registry2 := registryHelper.NewRegistryBuilder("shared-registry-2").
				WithConfigMapSource(sharedConfigMap.Name, "registry.json").
				WithSyncPolicy("2h").
				Create(registryHelper)

			// Both should become ready
			statusHelper.WaitForPhaseAny(registry1.Name, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, MediumTimeout)
			statusHelper.WaitForPhaseAny(registry2.Name, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, MediumTimeout)

			// Both should have same server count from shared source
			sharedNumServers := 2 // Sample ToolHive registry has 2 servers
			statusHelper.WaitForServerCount(registry1.Name, sharedNumServers, MediumTimeout)
			statusHelper.WaitForServerCount(registry2.Name, sharedNumServers, MediumTimeout)
		})

		It("should handle registry name conflicts gracefully", func() {
			configMap, _ := configMapHelper.CreateSampleToolHiveRegistry("conflict-config")

			// Create first registry
			registry1 := registryHelper.NewRegistryBuilder("conflict-registry").
				WithConfigMapSource(configMap.Name, "registry.json").
				Create(registryHelper)

			// Try to create second registry with same name - should fail
			duplicateRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "conflict-registry",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: configMap.Name,
							Key:  "registry.json",
						},
					},
				},
			}

			err := k8sClient.Create(ctx, duplicateRegistry)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsAlreadyExists(err)).To(BeTrue())

			// Original registry should still be functional
			statusHelper.WaitForPhaseAny(registry1.Name, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, MediumTimeout)
		})
	})
})

// Helper function to create test namespace
func createTestNamespace(ctx context.Context) string {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-registry-lifecycle-",
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

	// Wait for namespace deletion
	// Eventually(func() bool {
	// 	err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, namespace)
	// 	return errors.IsNotFound(err)
	// }, LongTimeout, DefaultPollingInterval).Should(BeTrue())
}
