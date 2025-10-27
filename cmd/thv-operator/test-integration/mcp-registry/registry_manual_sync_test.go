package operator_test

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
)

var _ = Describe("MCPRegistry Manual Sync with ConfigMap", Label("k8s", "registry"), func() {
	var (
		ctx             context.Context
		registryHelper  *MCPRegistryTestHelper
		configMapHelper *ConfigMapTestHelper
		statusHelper    *StatusTestHelper
		testNamespace   string
	)

	BeforeEach(func() {
		ctx = context.Background()
		testNamespace = createTestNamespace(ctx)

		// Initialize helpers
		registryHelper = NewMCPRegistryTestHelper(ctx, k8sClient, testNamespace)
		configMapHelper = NewConfigMapTestHelper(ctx, k8sClient, testNamespace)
		statusHelper = NewStatusTestHelper(ctx, k8sClient, testNamespace)
	})

	AfterEach(func() {
		// Clean up test resources
		Expect(registryHelper.CleanupRegistries()).To(Succeed())
		Expect(configMapHelper.CleanupConfigMaps()).To(Succeed())
		deleteTestNamespace(ctx, testNamespace)
	})

	Context("Manual Sync Trigger Scenarios", func() {
		var (
			registryName    string
			configMapName   string
			originalServers []RegistryServer
			updatedServers  []RegistryServer
		)

		BeforeEach(func() {
			names := NewUniqueNames("manual-sync")
			registryName = names.RegistryName
			configMapName = names.ConfigMapName

			// Create test registry data
			originalServers = CreateOriginalTestServers()
			// Create updated registry data (for later tests)
			updatedServers = CreateUpdatedTestServers()
		})

		It("should trigger sync when manual sync annotation is added", func() {
			By("Creating a ConfigMap with registry data")
			configMap := configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", originalServers).
				Create(configMapHelper)

			By("Creating an MCPRegistry without automatic sync policy")
			mcpRegistry := CreateMCPRegistryManualOnly(registryName, testNamespace,
				"Manual Sync Test Registry", configMapName) // No SyncPolicy - manual sync only
			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for initial sync to complete")
			statusHelper.WaitForPhaseAny(registryName, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, 30*time.Second)

			// Capture first sync time
			firstSyncRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, firstSyncRegistry)).To(Succeed())

			Expect(firstSyncRegistry.Status).NotTo(BeNil())
			Expect(firstSyncRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			firstSyncTime := firstSyncRegistry.Status.SyncStatus.LastSyncTime
			Expect(firstSyncTime).NotTo(BeNil())
			serverCount := firstSyncRegistry.Status.SyncStatus.ServerCount
			Expect(serverCount).To(Equal(1)) // Original registry has 1 server

			By("Verifying initial storage ConfigMap was created")
			storageConfigMapName := firstSyncRegistry.GetStorageName()
			storageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, storageConfigMap)).To(Succeed())

			By("Verifying storage data matches original ConfigMap")
			var storedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(storageConfigMap.Data["registry.json"]), &storedRegistry)).To(Succeed())
			verifyServerContent(storedRegistry, originalServers)

			By("Updating the source ConfigMap")
			Expect(UpdateConfigMapWithServers(configMap, updatedServers)).To(Succeed())
			Expect(k8sClient.Update(ctx, configMap)).To(Succeed())

			By("Adding manual sync trigger annotation")
			names := NewUniqueNames("manual-sync")
			triggerValue := names.GenerateTriggerValue("manual-sync")
			// Refresh the registry object first
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, mcpRegistry)).To(Succeed())

			AddManualSyncTrigger(mcpRegistry, triggerValue, mcpregistrystatus.SyncTriggerAnnotation)
			Expect(k8sClient.Update(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for manual sync to complete")
			Eventually(func() bool {
				registry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      registryName,
					Namespace: testNamespace,
				}, registry); err != nil {
					return false
				}

				// Check if sync time was updated and server count changed
				if registry.Status.SyncStatus == nil {
					return false
				}

				newSyncTime := registry.Status.SyncStatus.LastSyncTime
				newServerCount := registry.Status.SyncStatus.ServerCount

				return newSyncTime != nil &&
					newSyncTime.After(firstSyncTime.Time) &&
					newServerCount == 2 && // Updated registry has 2 servers
					registry.Status.LastManualSyncTrigger == triggerValue // Trigger was processed
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "Registry should sync when manual trigger annotation is added")

			By("Verifying updated storage data matches new ConfigMap")
			updatedStorageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, updatedStorageConfigMap)).To(Succeed())

			var newStoredRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(updatedStorageConfigMap.Data["registry.json"]), &newStoredRegistry)).To(Succeed())

			By("Storage should contain updated registry data")
			verifyServerContent(newStoredRegistry, updatedServers)
		})

		It("should handle manual sync with no data changes", func() {
			By("Creating a ConfigMap with registry data")
			_ = configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", originalServers).
				Create(configMapHelper)

			By("Creating an MCPRegistry")
			mcpRegistry := CreateMCPRegistryManualOnly(registryName, testNamespace,
				"Manual Sync No Changes Test Registry", configMapName)
			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for initial sync to complete")
			statusHelper.WaitForPhaseAny(registryName, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, 30*time.Second)

			// Capture initial sync state
			initialRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, initialRegistry)).To(Succeed())

			initialSyncTime := initialRegistry.Status.SyncStatus.LastSyncTime
			initialSyncHash := initialRegistry.Status.SyncStatus.LastSyncHash
			initialServerCount := initialRegistry.Status.SyncStatus.ServerCount

			By("Triggering manual sync without data changes")
			names := NewUniqueNames("no-changes-sync")
			triggerValue := names.GenerateTriggerValue("no-changes-sync")
			AddManualSyncTrigger(initialRegistry, triggerValue, mcpregistrystatus.SyncTriggerAnnotation)
			Expect(k8sClient.Update(ctx, initialRegistry)).To(Succeed())

			By("Waiting for manual sync trigger to be processed")
			Eventually(func() bool {
				registry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      registryName,
					Namespace: testNamespace,
				}, registry); err != nil {
					return false
				}

				// Check if trigger was processed (should update LastManualSyncTrigger)
				return registry.Status.LastManualSyncTrigger == triggerValue
			}, 20*time.Second, 2*time.Second).Should(BeTrue(), "Manual sync trigger should be processed even with no data changes")

			By("Verifying sync data remains unchanged")
			finalRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, finalRegistry)).To(Succeed())

			// Sync data should remain the same since no changes occurred
			Expect(finalRegistry.Status.SyncStatus.LastSyncTime).To(Equal(initialSyncTime))
			Expect(finalRegistry.Status.SyncStatus.LastSyncHash).To(Equal(initialSyncHash))
			Expect(finalRegistry.Status.SyncStatus.ServerCount).To(Equal(initialServerCount))
		})

		It("should retry failed manual syncs when source becomes available", func() {
			By("Creating an MCPRegistry without the source ConfigMap (sync will fail)")
			mcpRegistry := CreateMCPRegistryManualOnly(registryName, testNamespace,
				"Manual Retry Test Registry", configMapName) // This ConfigMap doesn't exist yet
			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for sync to fail")
			statusHelper.WaitForPhase(registryName, mcpv1alpha1.MCPRegistryPhaseFailed, 30*time.Second)

			By("Triggering manual sync while source is still missing")
			names1 := NewUniqueNames("manual-retry-1")
			triggerValue1 := names1.GenerateTriggerValue("manual-retry-1")
			// Refresh the registry object first
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, mcpRegistry)).To(Succeed())

			AddManualSyncTrigger(mcpRegistry, triggerValue1, mcpregistrystatus.SyncTriggerAnnotation)
			Expect(k8sClient.Update(ctx, mcpRegistry)).To(Succeed())

			By("Verifying manual sync also fails")
			Eventually(func() bool {
				registry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      registryName,
					Namespace: testNamespace,
				}, registry); err != nil {
					return false
				}

				return registry.Status.Phase == mcpv1alpha1.MCPRegistryPhaseFailed &&
					registry.Status.LastManualSyncTrigger == triggerValue1
			}, 20*time.Second, 2*time.Second).Should(BeTrue(), "Manual sync should also fail when source is missing")

			By("Creating the missing ConfigMap")
			_ = configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", originalServers).
				Create(configMapHelper)

			By("Triggering manual sync after ConfigMap creation")
			names2 := NewUniqueNames("manual-retry-2")
			triggerValue2 := names2.GenerateTriggerValue("manual-retry-2")
			// Refresh the registry object first
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, mcpRegistry)).To(Succeed())

			AddManualSyncTrigger(mcpRegistry, triggerValue2, mcpregistrystatus.SyncTriggerAnnotation)
			Expect(k8sClient.Update(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for manual sync to succeed after ConfigMap creation")
			Eventually(func() mcpv1alpha1.MCPRegistryPhase {
				registry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      registryName,
					Namespace: testNamespace,
				}, registry); err != nil {
					return ""
				}
				return registry.Status.Phase
			}, 30*time.Second, 2*time.Second).Should(BeElementOf(
				mcpv1alpha1.MCPRegistryPhaseReady,
				mcpv1alpha1.MCPRegistryPhasePending,
			))

			By("Verifying sync data is now correct")
			successRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, successRegistry)).To(Succeed())

			Expect(successRegistry.Status.SyncStatus.ServerCount).To(Equal(1))
			Expect(successRegistry.Status.SyncStatus.LastSyncTime).NotTo(BeNil())
			Expect(successRegistry.Status.LastManualSyncTrigger).To(Equal(triggerValue2))

			By("Verifying storage ConfigMap was created with correct data")
			storageConfigMapName := successRegistry.GetStorageName()
			storageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, storageConfigMap)).To(Succeed())

			var storedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(storageConfigMap.Data["registry.json"]), &storedRegistry)).To(Succeed())
			verifyServerContent(storedRegistry, originalServers)
		})

		It("should fail manual sync when source ConfigMap is deleted after successful sync", func() {
			By("Creating a ConfigMap with registry data")
			configMap := configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", originalServers).
				Create(configMapHelper)

			By("Creating an MCPRegistry")
			mcpRegistry := CreateMCPRegistryManualOnly(registryName, testNamespace,
				"Manual ConfigMap Deletion Test Registry", configMapName)
			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for initial sync to complete")
			statusHelper.WaitForPhaseAny(registryName, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, 30*time.Second)

			// Capture successful sync state
			successRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, successRegistry)).To(Succeed())

			successSyncTime := successRegistry.Status.SyncStatus.LastSyncTime
			successServerCount := successRegistry.Status.SyncStatus.ServerCount
			successSyncHash := successRegistry.Status.SyncStatus.LastSyncHash

			Expect(successServerCount).To(Equal(1))
			Expect(successSyncTime).NotTo(BeNil())
			Expect(successSyncHash).NotTo(BeEmpty())

			By("Verifying storage ConfigMap exists with correct data")
			storageConfigMapName := successRegistry.GetStorageName()
			storageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, storageConfigMap)).To(Succeed())

			var storedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(storageConfigMap.Data["registry.json"]), &storedRegistry)).To(Succeed())
			verifyServerContent(storedRegistry, originalServers)

			By("Deleting the source ConfigMap")
			Expect(k8sClient.Delete(ctx, configMap)).To(Succeed())

			By("Triggering manual sync after ConfigMap deletion")
			names := NewUniqueNames("manual-after-deletion")
			triggerValue := names.GenerateTriggerValue("manual-after-deletion")
			// Refresh the registry object first
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, mcpRegistry)).To(Succeed())

			AddManualSyncTrigger(mcpRegistry, triggerValue, mcpregistrystatus.SyncTriggerAnnotation)
			Expect(k8sClient.Update(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for manual sync to fail due to missing ConfigMap")
			Eventually(func() mcpv1alpha1.MCPRegistryPhase {
				registry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      registryName,
					Namespace: testNamespace,
				}, registry); err != nil {
					return ""
				}
				return registry.Status.Phase
			}, 20*time.Second, 2*time.Second).Should(Equal(mcpv1alpha1.MCPRegistryPhaseFailed))

			By("Verifying manual sync failure preserves previous successful sync data")
			failedRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, failedRegistry)).To(Succeed())

			// Previous sync data should be preserved
			Expect(failedRegistry.Status.SyncStatus.LastSyncTime).To(Equal(successSyncTime))
			Expect(failedRegistry.Status.SyncStatus.LastSyncHash).To(Equal(successSyncHash))
			Expect(failedRegistry.Status.SyncStatus.ServerCount).To(Equal(successServerCount))
			Expect(failedRegistry.Status.LastManualSyncTrigger).To(Equal(triggerValue))

			By("Verifying storage ConfigMap still exists with previous data")
			preservedStorageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, preservedStorageConfigMap)).To(Succeed())

			var preservedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(preservedStorageConfigMap.Data["registry.json"]), &preservedRegistry)).To(Succeed())
			verifyServerContent(preservedRegistry, originalServers)

			By("Verifying overall registry phase reflects the failure")
			Expect(failedRegistry.Status.Phase).To(Equal(mcpv1alpha1.MCPRegistryPhaseFailed))
		})

		It("should verify manual sync triggers work with complex registry content", func() {
			By("Creating a complex ConfigMap with multiple servers and metadata")
			complexServers := CreateComplexTestServers()

			_ = configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", complexServers).
				Create(configMapHelper)

			By("Creating an MCPRegistry")
			mcpRegistry := CreateMCPRegistryManualOnly(registryName, testNamespace,
				"Manual Complex Content Test Registry", configMapName)
			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for initial sync to complete")
			statusHelper.WaitForPhaseAny(registryName, []mcpv1alpha1.MCPRegistryPhase{mcpv1alpha1.MCPRegistryPhaseReady, mcpv1alpha1.MCPRegistryPhasePending}, 30*time.Second)

			By("Triggering manual sync")
			names := NewUniqueNames("complex-manual-sync")
			triggerValue := names.GenerateTriggerValue("complex-manual-sync")
			// Refresh the registry object first
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, mcpRegistry)).To(Succeed())

			AddManualSyncTrigger(mcpRegistry, triggerValue, mcpregistrystatus.SyncTriggerAnnotation)
			Expect(k8sClient.Update(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for sync to complete")
			Eventually(func() string {
				registry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      registryName,
					Namespace: testNamespace,
				}, registry); err != nil {
					return ""
				}
				return registry.Status.LastManualSyncTrigger
			}, 30*time.Second, 2*time.Second).Should(Equal(triggerValue))

			By("Retrieving and verifying storage ConfigMap content")
			registry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, registry)).To(Succeed())

			storageConfigMapName := registry.GetStorageName()
			storageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, storageConfigMap)).To(Succeed())

			var storedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(storageConfigMap.Data["registry.json"]), &storedRegistry)).To(Succeed())

			By("Verifying exact content match")
			Expect(registry.Status.SyncStatus.ServerCount).To(Equal(2))
			verifyServerContent(storedRegistry, complexServers)

			// Verify metadata
			Expect(storedRegistry.Version).To(Equal("1.0.0"))

			By("Verifying hash consistency")
			Expect(registry.Status.SyncStatus.LastSyncHash).NotTo(BeEmpty())

			By("Verifying manual sync trigger was processed")
			Expect(registry.Status.LastManualSyncTrigger).To(Equal(triggerValue))
		})
	})
})
