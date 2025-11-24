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

var _ = Describe("MCPRegistry Lifecycle Management", Label("k8s", "registry"), func() {
	var (
		ctx             context.Context
		registryHelper  *MCPRegistryTestHelper
		configMapHelper *ConfigMapTestHelper
		statusHelper    *StatusTestHelper
		timingHelper    *TimingTestHelper
		k8sHelper       *K8sResourceTestHelper
		testNamespace   string
		testHelpers     *serverConfigTestHelpers
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
		testHelpers = &serverConfigTestHelpers{
			ctx:            ctx,
			k8sClient:      k8sClient,
			testNamespace:  testNamespace,
			registryHelper: registryHelper,
			k8sHelper:      k8sHelper,
		}
	})

	AfterEach(func() {
		// Clean up test resources
		Expect(registryHelper.CleanupRegistries()).To(Succeed())
		Expect(configMapHelper.CleanupConfigMaps()).To(Succeed())
		deleteTestNamespace(ctx, testNamespace)
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
			configMap := configMapHelper.CreateSampleToolHiveRegistry("cleanup-config")

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

	Context("Multiple Registry Management", func() {
		var configMap1, configMap2 *corev1.ConfigMap
		It("should handle multiple registries in same namespace", func() {
			// Create multiple ConfigMaps
			configMap1 = configMapHelper.CreateSampleToolHiveRegistry("config-1")
			configMap2 = configMapHelper.CreateSampleToolHiveRegistry("config-2")

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
			// Check that both registries have sources configured
			Expect(len(registry1.Spec.Sources)).To(BeNumerically(">=", 1))
			Expect(len(registry2.Spec.Sources)).To(BeNumerically(">=", 1))
			// Check the sync policies on first source
			if len(registry1.Spec.Sources) > 0 && registry1.Spec.Sources[0].SyncPolicy != nil {
				Expect(registry1.Spec.Sources[0].SyncPolicy.Interval).To(Equal("1h"))
			}
			if len(registry2.Spec.Sources) > 0 && registry2.Spec.Sources[0].SyncPolicy != nil {
				Expect(registry2.Spec.Sources[0].SyncPolicy.Interval).To(Equal("30m"))
			}
			// Check the format on first source
			if len(registry1.Spec.Sources) > 0 {
				Expect(registry1.Spec.Sources[0].Format).To(Equal(mcpv1alpha1.RegistryFormatToolHive))
			}
			if len(registry2.Spec.Sources) > 0 {
				Expect(registry2.Spec.Sources[0].Format).To(Equal(mcpv1alpha1.RegistryFormatToolHive))
			}
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
			statusHelper.WaitForPhaseAny(registry1.Name, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, MediumTimeout)

			By("verifying registry servers config ConfigMap is created")
			serverConfigMap1 := testHelpers.waitForAndGetServerConfigMap(registry1.Name)
			serverConfigMap2 := testHelpers.waitForAndGetServerConfigMap(registry2.Name)

			deployment1 := testHelpers.getDeploymentForRegistry(registry1.Name)
			deployment2 := testHelpers.getDeploymentForRegistry(registry2.Name)

			By("checking registry server config ConfigMap volume and mount")
			testHelpers.verifyServerConfigVolume(deployment1, serverConfigMap1.Name)
			testHelpers.verifyServerConfigVolume(deployment2, serverConfigMap2.Name)

			By("checking registry source data ConfigMap volume and mount")
			testHelpers.verifySourceDataVolume(deployment1, registry1)
			testHelpers.verifySourceDataVolume(deployment2, registry2)
		})

		It("should handle registry name conflicts gracefully", func() {
			configMap := configMapHelper.CreateSampleToolHiveRegistry("conflict-config")

			// Create first registry
			registry1 := registryHelper.NewRegistryBuilder("conflict-registry").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			// Try to create second registry with same name - should fail
			duplicateRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "conflict-registry",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
						{
							Name: "default",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMap.Name,
									},
									Key: "registry.json",
								},
							},
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
}
