package operator_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPRegistry Filtering", Label("k8s", "registry"), func() {
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

	Context("Name-based filtering", func() {
		var configMap *corev1.ConfigMap

		BeforeEach(func() {
			// Create ConfigMap with multiple servers for filtering tests
			configMap = configMapHelper.NewConfigMapBuilder("filter-test-config").
				WithToolHiveRegistry("registry.json", []RegistryServer{
					{
						Name:        "production-server",
						Description: "Production server",
						Tier:        "Official",
						Status:      "Active",
						Transport:   "stdio",
						Tools:       []string{"prod_tool"},
						Image:       "test/prod:1.0.0",
						Tags:        []string{"production", "stable"},
					},
					{
						Name:        "test-server-alpha",
						Description: "Test server alpha",
						Tier:        "Community",
						Status:      "Active",
						Transport:   "streamable-http",
						Tools:       []string{"test_tool_alpha"},
						Image:       "test/alpha:1.0.0",
						Tags:        []string{"testing", "experimental"},
					},
					{
						Name:        "test-server-beta",
						Description: "Test server beta",
						Tier:        "Community",
						Status:      "Active",
						Transport:   "stdio",
						Tools:       []string{"test_tool_beta"},
						Image:       "test/beta:1.0.0",
						Tags:        []string{"testing", "beta"},
					},
					{
						Name:        "dev-server",
						Description: "Development server",
						Tier:        "Community",
						Status:      "Active",
						Transport:   "sse",
						Tools:       []string{"dev_tool"},
						Image:       "test/dev:1.0.0",
						Tags:        []string{"development", "unstable"},
					},
				}).
				Create(configMapHelper)
		})

		It("should apply name include filters correctly", func() {
			// Create registry with name include filter
			registry := registryHelper.NewRegistryBuilder("name-include-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithNameIncludeFilter([]string{"production-*", "dev-*"}).
				Create(registryHelper)

			// Wait for registry initialization
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			// Verify filtering applied - should include only production-server and dev-server
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			Expect(updatedRegistry.Status.SyncStatus.ServerCount).To(Equal(2)) // Only production-server and dev-server

			// Verify storage contains filtered content
			storageConfigMapName := updatedRegistry.Status.StorageRef.ConfigMapRef.Name
			storageConfigMap, err := k8sHelper.GetConfigMap(storageConfigMapName)
			Expect(err).NotTo(HaveOccurred())

			filteredData := storageConfigMap.Data["registry.json"]
			Expect(filteredData).To(ContainSubstring("production-server"))
			Expect(filteredData).To(ContainSubstring("dev-server"))
			Expect(filteredData).NotTo(ContainSubstring("test-server-alpha"))
			Expect(filteredData).NotTo(ContainSubstring("test-server-beta"))
		})

		It("should apply name exclude filters correctly", func() {
			// Create registry with name exclude filter
			registry := registryHelper.NewRegistryBuilder("name-exclude-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithNameExcludeFilter([]string{"test-*"}).
				Create(registryHelper)

			// Wait for registry initialization
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			// Verify filtering applied - should exclude test-server-alpha and test-server-beta
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			Expect(updatedRegistry.Status.SyncStatus.ServerCount).To(Equal(2)) // Only production-server and dev-server

			// Verify storage contains filtered content
			storageConfigMapName := updatedRegistry.Status.StorageRef.ConfigMapRef.Name
			storageConfigMap, err := k8sHelper.GetConfigMap(storageConfigMapName)
			Expect(err).NotTo(HaveOccurred())

			filteredData := storageConfigMap.Data["registry.json"]
			Expect(filteredData).To(ContainSubstring("production-server"))
			Expect(filteredData).To(ContainSubstring("dev-server"))
			Expect(filteredData).NotTo(ContainSubstring("test-server-alpha"))
			Expect(filteredData).NotTo(ContainSubstring("test-server-beta"))
		})

		It("should apply both name include and exclude filters correctly", func() {
			// Create registry with both include and exclude filters
			registry := registryHelper.NewRegistryBuilder("name-include-exclude-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithNameIncludeFilter([]string{"*-server*"}).       // Include all servers
				WithNameExcludeFilter([]string{"test-*", "dev-*"}). // Exclude test and dev servers
				Create(registryHelper)

			// Wait for registry initialization
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			// Verify filtering applied - should only include production-server
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			Expect(updatedRegistry.Status.SyncStatus.ServerCount).To(Equal(1)) // Only production-server

			// Verify storage contains filtered content
			storageConfigMapName := updatedRegistry.Status.StorageRef.ConfigMapRef.Name
			storageConfigMap, err := k8sHelper.GetConfigMap(storageConfigMapName)
			Expect(err).NotTo(HaveOccurred())

			filteredData := storageConfigMap.Data["registry.json"]
			Expect(filteredData).To(ContainSubstring("production-server"))
			Expect(filteredData).NotTo(ContainSubstring("test-server-alpha"))
			Expect(filteredData).NotTo(ContainSubstring("test-server-beta"))
			Expect(filteredData).NotTo(ContainSubstring("dev-server"))
		})
	})

	Context("Tag-based filtering", func() {
		var configMap *corev1.ConfigMap

		BeforeEach(func() {
			// Create ConfigMap with servers having different tags
			configMap = configMapHelper.NewConfigMapBuilder("tag-filter-config").
				WithToolHiveRegistry("registry.json", []RegistryServer{
					{
						Name:        "stable-server",
						Description: "Stable production server",
						Tier:        "Official",
						Status:      "Active",
						Transport:   "stdio",
						Tools:       []string{"stable_tool"},
						Image:       "test/stable:1.0.0",
						Tags:        []string{"production", "stable", "verified"},
					},
					{
						Name:        "beta-server",
						Description: "Beta testing server",
						Tier:        "Community",
						Status:      "Active",
						Transport:   "streamable-http",
						Tools:       []string{"beta_tool"},
						Image:       "test/beta:1.0.0",
						Tags:        []string{"testing", "beta"},
					},
					{
						Name:        "experimental-server",
						Description: "Experimental server",
						Tier:        "Community",
						Status:      "Active",
						Transport:   "stdio",
						Tools:       []string{"experimental_tool"},
						Image:       "test/experimental:1.0.0",
						Tags:        []string{"experimental", "unstable"},
					},
				}).
				Create(configMapHelper)
		})

		It("should apply tag include filters correctly", func() {
			// Create registry with tag include filter
			registry := registryHelper.NewRegistryBuilder("tag-include-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithTagIncludeFilter([]string{"production", "testing"}).
				Create(registryHelper)

			// Wait for registry initialization
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			// Verify filtering applied - should include stable-server and beta-server
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			Expect(updatedRegistry.Status.SyncStatus.ServerCount).To(Equal(2)) // stable-server and beta-server

			// Verify storage contains filtered content
			storageConfigMapName := updatedRegistry.Status.StorageRef.ConfigMapRef.Name
			storageConfigMap, err := k8sHelper.GetConfigMap(storageConfigMapName)
			Expect(err).NotTo(HaveOccurred())

			filteredData := storageConfigMap.Data["registry.json"]
			Expect(filteredData).To(ContainSubstring("stable-server"))
			Expect(filteredData).To(ContainSubstring("beta-server"))
			Expect(filteredData).NotTo(ContainSubstring("experimental-server"))
		})

		It("should apply tag exclude filters correctly", func() {
			// Create registry with tag exclude filter
			registry := registryHelper.NewRegistryBuilder("tag-exclude-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithTagExcludeFilter([]string{"experimental", "unstable"}).
				Create(registryHelper)

			// Wait for registry initialization
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			// Verify filtering applied - should exclude experimental-server
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(updatedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			Expect(updatedRegistry.Status.SyncStatus.ServerCount).To(Equal(2)) // stable-server and beta-server

			// Verify storage contains filtered content
			storageConfigMapName := updatedRegistry.Status.StorageRef.ConfigMapRef.Name
			storageConfigMap, err := k8sHelper.GetConfigMap(storageConfigMapName)
			Expect(err).NotTo(HaveOccurred())

			filteredData := storageConfigMap.Data["registry.json"]
			Expect(filteredData).To(ContainSubstring("stable-server"))
			Expect(filteredData).To(ContainSubstring("beta-server"))
			Expect(filteredData).NotTo(ContainSubstring("experimental-server"))
		})
	})

	Context("Filter updates", func() {
		var configMap *corev1.ConfigMap
		var registry *mcpv1alpha1.MCPRegistry

		BeforeEach(func() {
			// Create ConfigMap with multiple servers
			configMap = configMapHelper.NewConfigMapBuilder("update-filter-config").
				WithToolHiveRegistry("registry.json", []RegistryServer{
					{
						Name:        "server-alpha",
						Description: "Server alpha",
						Tier:        "Community",
						Status:      "Active",
						Transport:   "stdio",
						Tools:       []string{"alpha_tool"},
						Image:       "test/alpha:1.0.0",
						Tags:        []string{"alpha", "testing"},
					},
					{
						Name:        "server-beta",
						Description: "Server beta",
						Tier:        "Community",
						Status:      "Active",
						Transport:   "streamable-http",
						Tools:       []string{"beta_tool"},
						Image:       "test/beta:1.0.0",
						Tags:        []string{"beta", "testing"},
					},
					{
						Name:        "server-prod",
						Description: "Production server",
						Tier:        "Official",
						Status:      "Active",
						Transport:   "stdio",
						Tools:       []string{"prod_tool"},
						Image:       "test/prod:1.0.0",
						Tags:        []string{"production", "stable"},
					},
				}).
				Create(configMapHelper)

			// Create registry without any sync policy (manual sync only)
			registry = registryHelper.NewRegistryBuilder("filter-update-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithNameIncludeFilter([]string{"server-alpha", "server-beta"}). // Initially include alpha and beta
				Create(registryHelper)

			// Wait for initial sync
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)
		})

		It("should update storage content when filters are changed", func() {
			// Verify initial filtering - should have 2 servers
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedRegistry.Status.SyncStatus.ServerCount).To(Equal(2))

			// Get initial storage content
			initialStorageConfigMapName := updatedRegistry.Status.StorageRef.ConfigMapRef.Name
			initialStorageConfigMap, err := k8sHelper.GetConfigMap(initialStorageConfigMapName)
			Expect(err).NotTo(HaveOccurred())
			initialData := initialStorageConfigMap.Data["registry.json"]
			Expect(initialData).To(ContainSubstring("server-alpha"))
			Expect(initialData).To(ContainSubstring("server-beta"))
			Expect(initialData).NotTo(ContainSubstring("server-prod"))

			By("updating the filter to include all servers")
			// Update registry filter to include all servers
			updatedRegistry.Spec.Filter.NameFilters.Include = []string{"*"}
			Expect(registryHelper.UpdateRegistry(updatedRegistry)).To(Succeed())

			// Wait for sync to complete with new filter
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				currentRegistry, err := registryHelper.GetRegistry(registry.Name)
				if err != nil {
					return false
				}
				return currentRegistry.Status.SyncStatus.ServerCount == 3 // All 3 servers now included
			}).Should(BeTrue(), "Registry should sync with updated filter")

			By("verifying storage content reflects the filter change")
			// Verify updated storage content
			finalRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			finalStorageConfigMapName := finalRegistry.Status.StorageRef.ConfigMapRef.Name
			finalStorageConfigMap, err := k8sHelper.GetConfigMap(finalStorageConfigMapName)
			Expect(err).NotTo(HaveOccurred())
			finalData := finalStorageConfigMap.Data["registry.json"]
			Expect(finalData).To(ContainSubstring("server-alpha"))
			Expect(finalData).To(ContainSubstring("server-beta"))
			Expect(finalData).To(ContainSubstring("server-prod")) // Now included

			By("updating the filter to exclude beta and alpha servers")
			// Update filter again to exclude alpha and beta
			finalRegistry.Spec.Filter.NameFilters = &mcpv1alpha1.NameFilter{
				Include: []string{"*"},
				Exclude: []string{"*-alpha", "*-beta"},
			}
			Expect(registryHelper.UpdateRegistry(finalRegistry)).To(Succeed())

			// Wait for sync to complete with new exclusion filter
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				currentRegistry, err := registryHelper.GetRegistry(registry.Name)
				if err != nil {
					return false
				}
				return currentRegistry.Status.SyncStatus.ServerCount == 1 // Only server-prod
			}).Should(BeTrue(), "Registry should sync with updated exclusion filter")

			By("verifying final storage content reflects the exclusion")
			// Verify final storage content
			endRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			endStorageConfigMapName := endRegistry.Status.StorageRef.ConfigMapRef.Name
			endStorageConfigMap, err := k8sHelper.GetConfigMap(endStorageConfigMapName)
			Expect(err).NotTo(HaveOccurred())
			endData := endStorageConfigMap.Data["registry.json"]
			Expect(endData).NotTo(ContainSubstring("server-alpha"))
			Expect(endData).NotTo(ContainSubstring("server-beta"))
			Expect(endData).To(ContainSubstring("server-prod")) // Only this remains
		})
	})
})
