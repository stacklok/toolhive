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

var _ = Describe("MCPRegistry Git Automatic Sync", Label("k8s", "registry"), func() {
	var (
		ctx                     context.Context
		registryHelper          *MCPRegistryTestHelper
		gitHelper               *GitTestHelper
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
		gitHelper = NewGitTestHelper(ctx)
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
		Expect(gitHelper.CleanupRepositories()).To(Succeed())
		deleteTestNamespace(ctx, testNamespace)
		// Restore original values when test completes
		sync.DefaultSyncRequeueAfter = originalSyncRequeue
		controllers.DefaultControllerRetryAfter = originalControllerRetry
	})

	Context("Git Automatic Sync Scenarios", func() {
		var (
			registryName    string
			gitRepo         *GitTestRepository
			originalServers []RegistryServer
			updatedServers  []RegistryServer
		)

		BeforeEach(func() {
			names := NewUniqueNames("git-auto-sync")
			registryName = names.RegistryName

			// Create test registry data
			originalServers = CreateOriginalTestServers()
			updatedServers = CreateUpdatedTestServers()
		})

		It("should perform automatic sync at configured intervals from Git repository", func() {
			By("Creating a Git repository with registry data")
			gitRepo = gitHelper.CreateRepository("test-registry-repo")
			gitHelper.CommitRegistryData(gitRepo, "registry.json", originalServers, "Initial registry data")

			By("Creating an MCPRegistry with short sync interval and Git source")
			mcpRegistry := CreateMCPRegistryWithGitSource(registryName, testNamespace,
				"Git Auto Sync Test Registry", gitRepo.CloneURL, "main", "registry.json", "10s")
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

			By("Verifying storage data matches original Git repository content")
			var storedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(storageConfigMap.Data["registry.json"]), &storedRegistry)).To(Succeed())
			verifyServerContent(storedRegistry, originalServers)

			By("Updating the Git repository with new data")
			gitHelper.CommitRegistryData(gitRepo, "registry.json", updatedServers, "Updated registry with 2 servers")

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

			By("Verifying updated storage data matches new Git repository content")
			updatedStorageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, updatedStorageConfigMap)).To(Succeed())

			var newStoredRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(updatedStorageConfigMap.Data["registry.json"]), &newStoredRegistry)).To(Succeed())

			By("Storage should contain updated registry data from Git")
			verifyServerContent(newStoredRegistry, updatedServers)
		})

		It("should retry failed syncs when Git repository becomes accessible", func() {
			By("Creating an MCPRegistry with inaccessible Git repository (sync will fail)")
			mcpRegistry := CreateMCPRegistryWithGitSource(registryName, testNamespace,
				"Git Retry Test Registry", "https://invalid-git-repo.example.com/repo.git", "main", "registry.json", "5s")
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

			By("Updating MCPRegistry with valid Git repository")
			gitRepo = gitHelper.CreateRepository("valid-test-repo")
			gitHelper.CommitRegistryData(gitRepo, "registry.json", originalServers, "Initial registry data")

			// Update the registry spec to point to valid repository
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, failedRegistry)).To(Succeed())

			failedRegistry.Spec.Source.Git.Repository = gitRepo.CloneURL
			Expect(k8sClient.Update(ctx, failedRegistry)).To(Succeed())

			By("Waiting for sync to succeed after Git repository becomes accessible")
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

			By("Verifying storage ConfigMap was created with correct data from Git")
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

		It("should handle different Git branches and tags", func() {
			By("Creating a Git repository with registry data on main branch")
			gitRepo = gitHelper.CreateRepository("multi-branch-repo")
			gitHelper.CommitRegistryData(gitRepo, "registry.json", originalServers, "Initial data on main")

			By("Creating a feature branch with updated data")
			gitHelper.CreateBranch(gitRepo, "feature/updated-registry")
			gitHelper.CommitRegistryData(gitRepo, "registry.json", updatedServers, "Updated data on feature branch")

			By("Creating an MCPRegistry pointing to the feature branch")
			mcpRegistry := CreateMCPRegistryWithGitSource(registryName, testNamespace,
				"Git Branch Test Registry", gitRepo.CloneURL, "feature/updated-registry", "registry.json", "30s")
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

			By("Verifying data comes from the feature branch")
			registry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, registry)).To(Succeed())

			Expect(registry.Status.SyncStatus.ServerCount).To(Equal(2)) // Feature branch has 2 servers

			storageConfigMapName := registry.GetStorageName()
			storageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, storageConfigMap)).To(Succeed())

			var storedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(storageConfigMap.Data["registry.json"]), &storedRegistry)).To(Succeed())
			verifyServerContent(storedRegistry, updatedServers)
		})

		It("should handle Git repository with different file paths", func() {
			By("Creating a Git repository with registry data in subdirectory")
			gitRepo = gitHelper.CreateRepository("nested-path-repo")
			gitHelper.CommitRegistryDataAtPath(gitRepo, "configs/registries/registry.json", originalServers, "Registry in nested path")

			By("Creating an MCPRegistry pointing to the nested file path")
			mcpRegistry := CreateMCPRegistryWithGitSource(registryName, testNamespace,
				"Git Path Test Registry", gitRepo.CloneURL, "main", "configs/registries/registry.json", "30s")
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

			By("Verifying data was correctly fetched from nested path")
			registry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, registry)).To(Succeed())

			Expect(registry.Status.SyncStatus.ServerCount).To(Equal(1))

			storageConfigMapName := registry.GetStorageName()
			storageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, storageConfigMap)).To(Succeed())

			var storedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(storageConfigMap.Data["registry.json"]), &storedRegistry)).To(Succeed())
			verifyServerContent(storedRegistry, originalServers)
		})

		It("should verify Git sync with complex registry content", func() {
			By("Creating a Git repository with complex server configurations")
			complexServers := CreateComplexTestServers()
			gitRepo = gitHelper.CreateRepository("complex-content-repo")
			gitHelper.CommitRegistryData(gitRepo, "registry.json", complexServers, "Complex registry with multiple server types")

			By("Creating an MCPRegistry")
			mcpRegistry := CreateMCPRegistryWithGitSource(registryName, testNamespace,
				"Git Complex Content Test Registry", gitRepo.CloneURL, "main", "registry.json", "30s")
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

			By("Verifying exact content match from Git repository")
			Expect(registry.Status.SyncStatus.ServerCount).To(Equal(2))
			verifyServerContent(storedRegistry, complexServers)

			// Verify metadata
			Expect(storedRegistry.Version).To(Equal("1.0.0"))

			By("Verifying hash consistency")
			Expect(registry.Status.SyncStatus.LastSyncHash).NotTo(BeEmpty())

			By("Verifying timing constants are still configurable for Git tests")
			syncRequeueTime := sync.DefaultSyncRequeueAfter
			controllerRetryTime := controllers.DefaultControllerRetryAfter

			// Verify test values
			Expect(syncRequeueTime).To(Equal(shortSyncRequeue))
			Expect(controllerRetryTime).To(Equal(shortControllerRetry))

			// Verify constants are still available
			Expect(sync.DefaultSyncRequeueAfterConstant).To(Equal(5 * time.Minute))
			Expect(controllers.DefaultControllerRetryAfterConstant).To(Equal(5 * time.Minute))
		})

		It("should handle Git authentication and private repositories", func() {
			Skip("Private repository authentication tests require additional Git server setup")

			// This test would cover:
			// - SSH key-based authentication
			// - HTTPS token-based authentication
			// - Kubernetes Secret integration for credentials
			// - Authentication failure scenarios
		})
	})
})
