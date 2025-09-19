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
			configMap := configMapHelper.CreateSampleToolHiveRegistry("test-config")

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

			// Wait for controller to process and verify initial status
			By("waiting for controller to process and verify initial status")
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				phase, err := registryHelper.GetRegistryPhase(registry.Name)
				if err != nil {
					return ""
				}
				return phase
			}).Should(BeElementOf(
				mcpv1alpha1.MCPRegistryPhasePending,
				mcpv1alpha1.MCPRegistryPhaseReady,
				mcpv1alpha1.MCPRegistryPhaseSyncing,
			))

			// Verify finalizer was added
			By("waiting for finalizer to be added")
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
				if err != nil {
					return false
				}
				return containsFinalizer(updatedRegistry.Finalizers, registryFinalizerName)
			}).Should(BeTrue())

			By("verifying registry status")
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedRegistry.Status.Phase).To(Equal(mcpv1alpha1.MCPRegistryPhaseReady))
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

			// Should still become ready for manual sync
			statusHelper.WaitForPhase(registry.Name, mcpv1alpha1.MCPRegistryPhaseReady, MediumTimeout)
		})

		It("should set correct metadata labels and annotations", func() {
			configMap := configMapHelper.CreateSampleToolHiveRegistry("labeled-config")

			registry := registryHelper.NewRegistryBuilder("labeled-registry").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithLabel("app", "test").
				WithLabel("version", "1.0").
				WithAnnotation("description", "Test registry").
				Create(registryHelper)

			// Verify labels and annotations
			Expect(registry.Labels).To(HaveKeyWithValue("app", "test"))
			Expect(registry.Labels).To(HaveKeyWithValue("version", "1.0"))
			Expect(registry.Annotations).To(HaveKeyWithValue("description", "Test registry"))
		})
	})

	Context("Finalizer Management", func() {
		It("should add finalizer on creation", func() {
			configMap := configMapHelper.CreateSampleToolHiveRegistry("finalizer-config")

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
			configMap := configMapHelper.CreateSampleToolHiveRegistry("deletion-config")

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
			statusHelper.WaitForPhase(registry.Name, mcpv1alpha1.MCPRegistryPhaseTerminating, MediumTimeout)

			// Verify registry is eventually deleted (finalizer removed)
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registry.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Context("Deletion Handling", func() {
		It("should perform graceful deletion with cleanup", func() {
			configMap := configMapHelper.CreateSampleToolHiveRegistry("cleanup-config")

			registry := registryHelper.NewRegistryBuilder("cleanup-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("30m").
				Create(registryHelper)

			// Wait for registry to be ready
			statusHelper.WaitForPhase(registry.Name, mcpv1alpha1.MCPRegistryPhaseReady, MediumTimeout)

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
			configMap := configMapHelper.CreateSampleToolHiveRegistry("missing-config")

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
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("configMap field is required"))
		})

		It("should reject invalid sync interval", func() {
			configMap := configMapHelper.CreateSampleToolHiveRegistry("interval-config")

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

			// Should enter failed state due to missing source
			statusHelper.WaitForPhase(registry.Name, mcpv1alpha1.MCPRegistryPhaseFailed, MediumTimeout)

			// Check condition reflects the problem
			statusHelper.WaitForCondition(registry.Name, mcpv1alpha1.ConditionSourceAvailable,
				metav1.ConditionFalse, MediumTimeout)
		})
	})

	Context("Multiple Registry Management", func() {
		It("should handle multiple registries in same namespace", func() {
			// Create multiple ConfigMaps
			configMap1 := configMapHelper.CreateSampleToolHiveRegistry("config-1")
			configMap2 := configMapHelper.CreateSampleUpstreamRegistry("config-2")

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
			statusHelper.WaitForPhase(registry1.Name, mcpv1alpha1.MCPRegistryPhaseReady, MediumTimeout)
			statusHelper.WaitForPhase(registry2.Name, mcpv1alpha1.MCPRegistryPhaseReady, MediumTimeout)

			// Verify they operate independently
			Expect(registry1.Spec.SyncPolicy.Interval).To(Equal("1h"))
			Expect(registry2.Spec.SyncPolicy.Interval).To(Equal("30m"))
			Expect(registry2.Spec.Source.Format).To(Equal(mcpv1alpha1.RegistryFormatUpstream))
		})

		It("should allow multiple registries with same ConfigMap source", func() {
			// Create shared ConfigMap
			sharedConfigMap := configMapHelper.CreateSampleToolHiveRegistry("shared-config")

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
			statusHelper.WaitForPhase(registry1.Name, mcpv1alpha1.MCPRegistryPhaseReady, MediumTimeout)
			statusHelper.WaitForPhase(registry2.Name, mcpv1alpha1.MCPRegistryPhaseReady, MediumTimeout)

			// Both should have same server count from shared source
			statusHelper.WaitForServerCount(registry1.Name, 2, MediumTimeout)
			statusHelper.WaitForServerCount(registry2.Name, 2, MediumTimeout)
		})

		It("should handle registry name conflicts gracefully", func() {
			configMap := configMapHelper.CreateSampleToolHiveRegistry("conflict-config")

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
			statusHelper.WaitForPhase(registry1.Name, mcpv1alpha1.MCPRegistryPhaseReady, MediumTimeout)
		})
	})
})

// Helper function to check if a finalizer exists in the list
func containsFinalizer(finalizers []string, finalizer string) bool {
	for _, f := range finalizers {
		if f == finalizer {
			return true
		}
	}
	return false
}

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
