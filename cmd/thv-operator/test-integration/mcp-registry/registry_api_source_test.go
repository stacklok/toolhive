package operator_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPRegistry API Source Integration", Label("k8s", "registry", "api"), func() {
	var (
		ctx             context.Context
		registryHelper  *MCPRegistryTestHelper
		configMapHelper *ConfigMapTestHelper
		statusHelper    *StatusTestHelper
		testNamespace   string
		mockAPIServer   *httptest.Server
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
		if mockAPIServer != nil {
			mockAPIServer.Close()
		}
		// Clean up test resources
		Expect(registryHelper.CleanupRegistries()).To(Succeed())
		Expect(configMapHelper.CleanupConfigMaps()).To(Succeed())
		deleteTestNamespace(ctx, testNamespace)
	})

	Context("ToolHive API Source", func() {
		var (
			registryName    string
			expectedServers []APIServerSummary
		)

		BeforeEach(func() {
			names := NewUniqueNames("api-source")
			registryName = names.RegistryName

			// Get expected default test servers
			expectedServers = CreateDefaultToolHiveServers()

			// Create mock ToolHive API server using helper
			mockAPIServer = NewToolHiveMockServer()
		})

		It("should successfully sync from ToolHive API endpoint", func() {
			By("Creating an MCPRegistry with API source")
			mcpRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      registryName,
					Namespace: testNamespace,
					Labels: map[string]string{
						"test.toolhive.io/suite": "operator-e2e",
						"test.toolhive.io/type":  "api-source",
					},
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					DisplayName: "Test API Registry",
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeAPI,
						API: &mcpv1alpha1.APISource{
							Endpoint: mockAPIServer.URL,
						},
					},
				},
			}

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

			By("Verifying sync status")
			syncedRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, syncedRegistry)).To(Succeed())

			Expect(syncedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(syncedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseComplete))
			Expect(syncedRegistry.Status.SyncStatus.ServerCount).To(Equal(2))
			Expect(syncedRegistry.Status.SyncStatus.LastSyncTime).NotTo(BeNil())
			Expect(syncedRegistry.Status.SyncStatus.LastSyncHash).NotTo(BeEmpty())

			By("Verifying storage ConfigMap was created")
			storageConfigMapName := syncedRegistry.GetStorageName()
			storageConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      storageConfigMapName,
				Namespace: testNamespace,
			}, storageConfigMap)).To(Succeed())

			By("Verifying storage data contains server information")
			var storedRegistry ToolHiveRegistryData
			Expect(json.Unmarshal([]byte(storageConfigMap.Data["registry.json"]), &storedRegistry)).To(Succeed())

			// Verify server count matches expected
			totalServers := len(storedRegistry.Servers) + len(storedRegistry.RemoteServers)
			Expect(totalServers).To(Equal(len(expectedServers)), "Should have correct number of servers")

			// Verify server data - validate all fields against expected servers
			Expect(storedRegistry.Servers).NotTo(BeNil())
			Expect(storedRegistry.Servers).To(HaveLen(len(expectedServers)))

			// Validate each expected server
			for _, expected := range expectedServers {
				By("Verifying server: " + expected.Name)
				storedServer := storedRegistry.Servers[expected.Name]
				Expect(storedServer).NotTo(BeNil(), expected.Name+" server should exist")
				Expect(storedServer.Name).To(Equal(expected.Name))
				Expect(storedServer.Description).To(Equal(expected.Description))
				Expect(storedServer.Tier).To(Equal(expected.Tier))
				Expect(storedServer.Status).To(Equal(expected.Status))
				Expect(storedServer.Transport).To(Equal(expected.Transport))
				Expect(storedServer.Image).NotTo(BeEmpty(), expected.Name+" should have an image")

				// Verify image follows expected pattern
				expectedImagePrefix := "ghcr.io/modelcontextprotocol/server-" + expected.Name
				Expect(storedServer.Image).To(HavePrefix(expectedImagePrefix))
			}
		})

		It("should handle invalid API endpoint gracefully", func() {
			By("Creating an MCPRegistry with invalid API endpoint")
			mcpRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      registryName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					DisplayName: "Invalid API Registry",
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeAPI,
						API: &mcpv1alpha1.APISource{
							Endpoint: "http://invalid-endpoint-does-not-exist.local:9999",
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for sync to fail")
			statusHelper.WaitForPhase(registryName, mcpv1alpha1.MCPRegistryPhaseFailed, 30*time.Second)

			By("Verifying failure status")
			failedRegistry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, failedRegistry)).To(Succeed())

			Expect(failedRegistry.Status.Phase).To(Equal(mcpv1alpha1.MCPRegistryPhaseFailed))
			Expect(failedRegistry.Status.SyncStatus).NotTo(BeNil())
			Expect(failedRegistry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseFailed))
			Expect(failedRegistry.Status.SyncStatus.Message).NotTo(BeEmpty())
		})
	})

	Context("Format Detection", func() {
		var registryName string

		BeforeEach(func() {
			names := NewUniqueNames("format-detect")
			registryName = names.RegistryName
		})

		It("should detect ToolHive format from /v0/info endpoint", func() {
			// Create mock server with single test server using builder
			testServer := APIServerSummary{
				Name:        "test",
				Description: "Test server",
				Tier:        "official",
				Status:      "active",
				Transport:   "stdio",
			}

			mockAPIServer = NewMockAPIServerBuilder().
				WithToolHiveInfo("1.0.0", "2025-01-15T12:00:00Z", "test", 1).
				WithToolHiveServers([]APIServerSummary{testServer}).
				WithServerDetail("test", "Test server", "official", "active", "stdio", "test:latest").
				Build()

			By("Creating MCPRegistry pointing to ToolHive-compatible API")
			mcpRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      registryName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					DisplayName: "Format Detection Test",
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeAPI,
						API: &mcpv1alpha1.APISource{
							Endpoint: mockAPIServer.URL,
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for successful sync")
			Eventually(func() mcpv1alpha1.SyncPhase {
				registry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      registryName,
					Namespace: testNamespace,
				}, registry); err != nil {
					return ""
				}
				if registry.Status.SyncStatus == nil {
					return ""
				}
				return registry.Status.SyncStatus.Phase
			}, 30*time.Second, 2*time.Second).Should(Equal(mcpv1alpha1.SyncPhaseComplete))

			By("Verifying ToolHive format was detected and sync succeeded")
			registry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, registry)).To(Succeed())

			Expect(registry.Status.SyncStatus.ServerCount).To(Equal(1))
		})

		It("should fail gracefully for upstream format (Phase 2 not implemented)", func() {
			// Create mock upstream MCP Registry API server using helper
			mockAPIServer = NewUpstreamMockServer()

			By("Creating MCPRegistry pointing to upstream-format API")
			mcpRegistry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      registryName,
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					DisplayName: "Upstream Format Test",
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeAPI,
						API: &mcpv1alpha1.APISource{
							Endpoint: mockAPIServer.URL,
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, mcpRegistry)).To(Succeed())

			By("Waiting for sync to fail (Phase 2 not implemented)")
			statusHelper.WaitForPhase(registryName, mcpv1alpha1.MCPRegistryPhaseFailed, 30*time.Second)

			By("Verifying appropriate error message")
			registry := &mcpv1alpha1.MCPRegistry{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: testNamespace,
			}, registry)).To(Succeed())

			Expect(registry.Status.SyncStatus.Phase).To(Equal(mcpv1alpha1.SyncPhaseFailed))
			Expect(registry.Status.SyncStatus.Message).To(ContainSubstring("not yet implemented"))
		})
	})
})
