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
	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sync"
)

var _ = Describe("MCPRegistry Automatic Sync with ConfigMap", func() {
	var (
		ctx                     context.Context
		registryHelper          *MCPRegistryTestHelper
		configMapHelper         *ConfigMapTestHelper
		statusHelper            *StatusTestHelper
		testNamespace           string
		originalSyncRequeue     time.Duration
		originalControllerRetry time.Duration
	)
	const (
		shortSyncRequeue     = time.Second * 10
		shortControllerRetry = time.Second * 10
	)

	BeforeEach(func() {
		ctx = context.Background()
		testNamespace = createTestNamespace(ctx)

		// Initialize helpers
		registryHelper = NewMCPRegistryTestHelper(ctx, k8sClient, testNamespace)
		configMapHelper = NewConfigMapTestHelper(ctx, k8sClient, testNamespace)
		statusHelper = NewStatusTestHelper(ctx, k8sClient, testNamespace)

		// Store original values to restore later
		originalSyncRequeue = sync.DefaultSyncRequeueAfter
		originalControllerRetry = controllers.DefaultControllerRetryAfter

		By("Setting shorter retry interval for faster testing")
		// Set shorter intervals for faster test execution
		sync.DefaultSyncRequeueAfter = shortSyncRequeue
		controllers.DefaultControllerRetryAfter = shortControllerRetry
	})

	AfterEach(func() {
		// Clean up test resources
		Expect(registryHelper.CleanupRegistries()).To(Succeed())
		Expect(configMapHelper.CleanupConfigMaps()).To(Succeed())
		deleteTestNamespace(ctx, testNamespace)
		// Restore original values when test completes
		defer func() {
			sync.DefaultSyncRequeueAfter = originalSyncRequeue
			controllers.DefaultControllerRetryAfter = originalControllerRetry
		}()
	})

	Context("Automatic Sync Scenarios", func() {
		var (
			registryName    string
			configMapName   string
			originalServers []RegistryServer
			updatedServers  []RegistryServer
		)

		BeforeEach(func() {
			names := NewUniqueNames("auto-sync")
			registryName = names.RegistryName
			configMapName = names.ConfigMapName

			// Create test registry data
			originalServers = CreateOriginalTestServers()
			// Create updated registry data (for later tests)
			updatedServers = CreateUpdatedTestServers()
		})

		It("should perform automatic sync at configured intervals", func() {
			By("Creating a ConfigMap with registry data")
			configMap := configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", originalServers).
				Create(configMapHelper)

			By("Creating an MCPRegistry with short sync interval")
			mcpRegistry := CreateMCPRegistryWithSyncPolicy(registryName, testNamespace,
				"Auto Sync Test Registry", configMapName, "10s")
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

			By("Waiting for automatic re-sync (should happen within 15s)")
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
					newServerCount == 2 // Updated registry has 2 servers
			}, 20*time.Second, 2*time.Second).Should(BeTrue(), "Registry should automatically re-sync within interval")

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

		It("should retry failed syncs and increment attempt counter", func() {
			By("Creating an MCPRegistry without the source ConfigMap (sync will fail)")
			mcpRegistry := CreateMCPRegistryWithSyncPolicy(registryName, testNamespace,
				"Retry Test Registry", configMapName, "5s") // This ConfigMap doesn't exist yet
			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for sync to fail")
			statusHelper.WaitForPhase(registryName, mcpv1alpha1.MCPRegistryPhaseFailed, 30*time.Second)

			// Verify attempt counter incremented
			failedRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, failedRegistry)).To(Succeed())

			Expect(failedRegistry.Status.Phase).To(Equal(mcpv1alpha1.MCPRegistryPhaseFailed))
			Expect(failedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseFailed))
			initialAttemptCount := failedRegistry.Status.SyncStatus.AttemptCount
			Expect(initialAttemptCount).To(BeNumerically(">", 0))

			By("Waiting for retry attempt and verifying attempt counter increments")
			Eventually(func() int {
				registry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      registryName,
					Namespace: testNamespace,
				}, registry); err != nil {
					return -1
				}

				if registry.Status.SyncStatus == nil {
					return -1
				}

				return registry.Status.SyncStatus.AttemptCount
			}, 15*time.Second, 2*time.Second).Should(BeNumerically(">", initialAttemptCount),
				"Attempt count should increment on retry")

			By("Creating the missing ConfigMap")
			_ = configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", originalServers).
				Create(configMapHelper)

			By("Waiting for sync to succeed after ConfigMap creation")
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

		It("should fail sync when source ConfigMap is deleted after successful sync", func() {
			By("Creating a ConfigMap with registry data")
			configMap := configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", originalServers).
				Create(configMapHelper)

			By("Creating an MCPRegistry with automatic sync")
			mcpRegistry := CreateMCPRegistryWithSyncPolicy(registryName, testNamespace,
				"ConfigMap Deletion Test Registry", configMapName, "8s")
			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for initial sync to complete")
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

			By("Waiting for sync to fail due to missing ConfigMap")
			statusHelper.WaitForPhase(registryName, mcpv1alpha1.MCPRegistryPhaseFailed, 20*time.Second)

			By("Verifying sync failure preserves previous successful sync data")
			failedRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, failedRegistry)).To(Succeed())

			// Previous sync data should be preserved
			Expect(failedRegistry.Status.SyncStatus.LastSyncTime).To(Equal(successSyncTime))
			Expect(failedRegistry.Status.SyncStatus.LastSyncHash).To(Equal(successSyncHash))
			Expect(failedRegistry.Status.SyncStatus.ServerCount).To(Equal(successServerCount))
			Expect(failedRegistry.Status.SyncStatus.AttemptCount).To(BeNumerically(">", 0))

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

		It("should verify persistence data matches original ConfigMap content exactly", func() {
			By("Creating a complex ConfigMap with multiple servers and metadata")
			complexServers := CreateComplexTestServers()

			_ = configMapHelper.NewConfigMapBuilder(configMapName).
				WithToolHiveRegistry("registry.json", complexServers).
				Create(configMapHelper)

			By("Creating an MCPRegistry")
			mcpRegistry := CreateMCPRegistryWithSyncPolicy(registryName, testNamespace,
				"Content Verification Registry", configMapName, "30s")
			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for sync to complete")
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

			By("Verifying timing constants are accessible and configurable from test code")
			// These variables can be used in tests to adjust timeout expectations and behavior
			syncRequeueTime := sync.DefaultSyncRequeueAfter
			controllerRetryTime := controllers.DefaultControllerRetryAfter

			// Verify default values
			Expect(syncRequeueTime).To(Equal(shortSyncRequeue))
			Expect(controllerRetryTime).To(Equal(shortControllerRetry))

			// Verify constants are also available
			Expect(sync.DefaultSyncRequeueAfterConstant).To(Equal(5 * time.Minute))
			Expect(controllers.DefaultControllerRetryAfterConstant).To(Equal(5 * time.Minute))
		})
	})
})
