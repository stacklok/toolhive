package operator_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
)

// Helper functions to reduce duplication in tests
type serverConfigTestHelpers struct {
	ctx            context.Context
	k8sClient      client.Client
	testNamespace  string
	registryHelper *MCPRegistryTestHelper
	k8sHelper      *K8sResourceTestHelper
}

var _ = Describe("MCPRegistry Server Config (Consolidated)", Label("k8s", "registry", "config"), func() {
	var (
		ctx             context.Context
		registryHelper  *MCPRegistryTestHelper
		configMapHelper *ConfigMapTestHelper
		statusHelper    *StatusTestHelper
		timingHelper    *TimingTestHelper
		k8sHelper       *K8sResourceTestHelper
		testHelpers     *serverConfigTestHelpers
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

		// Initialize test helpers
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

	// Table-driven test for different source types
	DescribeTable("Registry Server Config Creation for Different Sources",
		func(
			registryName string,
			setupRegistry func() *mcpv1alpha1.MCPRegistry,
			expectedConfigContent map[string]string,
			verifySourceVolume func(*appsv1.Deployment, *mcpv1alpha1.MCPRegistry),
		) {
			By("creating an MCPRegistry resource")
			registry := setupRegistry()

			// Verify registry was created
			Expect(registry.Name).To(Equal(registryName))
			Expect(registry.Namespace).To(Equal(testNamespace))

			By("waiting for registry initialization")
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			By("verifying Registry API Service and Deployment exist")
			apiResourceName := registry.GetAPIResourceName()

			// Wait for Service to be created
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				return k8sHelper.ServiceExists(apiResourceName)
			}).Should(BeTrue(), "Registry API Service should exist")

			// Wait for Deployment to be created
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				return k8sHelper.DeploymentExists(apiResourceName)
			}).Should(BeTrue(), "Registry API Deployment should exist")

			// By("waiting for finalizer to be added")
			// timingHelper.WaitForControllerReconciliation(func() interface{} {
			// 	return containsFinalizer(registry.Finalizers, registryFinalizerName)
			// }).Should(BeTrue(), "Registry should have finalizer")

			service, err := k8sHelper.GetService(apiResourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(service.Name).To(Equal(apiResourceName))
			Expect(service.Namespace).To(Equal(testNamespace))
			Expect(service.Spec.Ports).To(HaveLen(1))
			Expect(service.Spec.Ports[0].Name).To(Equal("http"))

			// Verify the Deployment has correct configuration
			By("verifying the deployment is created")
			deployment := testHelpers.getDeploymentForRegistry(registry.Name)
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
			registry, err = registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			// In envtest, the deployment won't actually be ready, so expect Pending phase
			// but verify that sync is complete and API deployment is in progress
			Expect(registry.Status.Phase).To(BeElementOf(
				mcpv1alpha1.MCPRegistryPhasePending, // API deployment in progress
				mcpv1alpha1.MCPRegistryPhaseReady,   // If somehow API becomes ready
			))

			// Verify API status exists and shows deployment
			Expect(registry.Status.APIStatus).NotTo(BeNil())
			Expect(registry.Status.APIStatus.Phase).To(BeElementOf(
				mcpv1alpha1.APIPhaseDeploying, // Deployment created but not ready
				mcpv1alpha1.APIPhaseReady,     // If somehow becomes ready
			))
			if registry.Status.APIStatus.Phase == mcpv1alpha1.APIPhaseReady {
				Expect(registry.Status.APIStatus.Endpoint).To(Equal(fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", apiResourceName, testNamespace)))
			}

			By("verifying registry server config ConfigMap is created")
			serverConfigMap := testHelpers.waitForAndGetServerConfigMap(registry.Name)

			By("validating the registry server config ConfigMap contents")
			// Verify basic properties
			testHelpers.verifyConfigMapBasics(serverConfigMap)

			// Verify source-specific content
			configYAML := serverConfigMap.Data["config.yaml"]
			testHelpers.verifyConfigMapContent(configYAML, registry.Name, expectedConfigContent)

			// Verify the appropriate source type field is present (file, git, or api)
			// This is determined by which source is configured in the registry
			if registry.Spec.Registries[0].ConfigMapRef != nil {
				Expect(configYAML).To(ContainSubstring("file:"), "ConfigMap source should have file field")
			} else if registry.Spec.Registries[0].Git != nil {
				Expect(configYAML).To(ContainSubstring("git:"), "Git source should have git field")
			} else if registry.Spec.Registries[0].API != nil {
				Expect(configYAML).To(ContainSubstring("api:"), "API source should have api field")
			}

			By("verifying the ConfigMap is owned by the MCPRegistry")
			testHelpers.verifyConfigMapOwnership(serverConfigMap, registry)

			By("checking registry server config ConfigMap volume and mount")
			testHelpers.verifyServerConfigVolume(deployment, serverConfigMap.Name)

			By("checking source-specific volumes")
			verifySourceVolume(deployment, registry)

			By("checking storage emptyDir volume and mount")
			testHelpers.verifyStorageVolume(deployment)

			By("verifying container arguments use the server config")
			testHelpers.verifyContainerArguments(deployment)
		},

		Entry("ConfigMap Source",
			"test-config-registry",
			func() *mcpv1alpha1.MCPRegistry {
				configMap := configMapHelper.CreateSampleToolHiveRegistry("test-config")
				return registryHelper.NewRegistryBuilder("test-config-registry").
					WithConfigMapSource(configMap.Name, "registry.json").
					WithSyncPolicy("1h").
					WithLabel("app", "test-config-registry").
					WithAnnotation("description", "Test config registry").
					Create(registryHelper)
			},
			map[string]string{
				"path":     filepath.Join(config.RegistryJSONFilePath, "default", config.RegistryJSONFileName),
				"interval": "1h",
			},
			func(deployment *appsv1.Deployment, registry *mcpv1alpha1.MCPRegistry) {
				// ConfigMap sources need the source data volume
				testHelpers.verifySourceDataVolume(deployment, registry)
			},
		),

		Entry("Git Source",
			"test-git-registry",
			func() *mcpv1alpha1.MCPRegistry {
				return registryHelper.NewRegistryBuilder("test-git-registry").
					WithGitSource(
						"https://github.com/mcp-servers/example-registry.git",
						"main",
						"registry.json",
					).
					WithSyncPolicy("2h").
					Create(registryHelper)
			},
			map[string]string{
				"repository": "https://github.com/mcp-servers/example-registry.git",
				"branch":     "main",
				"interval":   "2h",
			},

			func(deployment *appsv1.Deployment, _ *mcpv1alpha1.MCPRegistry) {
				// Git sources should NOT have the source data volume
				testHelpers.verifyNoSourceDataVolume(deployment, "Git")
			},
		),

		Entry("API Source",
			"test-api-registry",
			func() *mcpv1alpha1.MCPRegistry {
				return registryHelper.NewRegistryBuilder("test-api-registry").
					WithAPISource("http://registry-api.default.svc.cluster.local:8080/api").
					WithSyncPolicy("30m").
					Create(registryHelper)
			},
			map[string]string{
				"endpoint": "http://registry-api.default.svc.cluster.local:8080/api",
				"interval": "30m",
			},
			func(deployment *appsv1.Deployment, _ *mcpv1alpha1.MCPRegistry) {
				// API sources should NOT have the source data volume
				testHelpers.verifyNoSourceDataVolume(deployment, "API")
			},
		),
	)

	Describe("Multiple ConfigMap Sources", func() {
		It("should create proper volume mounts for multiple ConfigMap sources", func() {
			By("creating ConfigMap sources")
			// First ConfigMap
			configMap1 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "registry-cm-1",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"servers.json": `{
						"version": "1.0",
						"servers": [
							{
								"name": "server-a",
								"description": "Server A from ConfigMap 1",
								"image": "example.com/server-a:latest"
							}
						]
					}`,
				},
			}
			Expect(k8sClient.Create(ctx, configMap1)).Should(Succeed())

			// Second ConfigMap
			configMap2 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "registry-cm-2",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"data.json": `{
						"version": "1.0",
						"servers": [
							{
								"name": "server-b",
								"description": "Server B from ConfigMap 2",
								"image": "example.com/server-b:latest"
							}
						]
					}`,
				},
			}
			Expect(k8sClient.Create(ctx, configMap2)).Should(Succeed())

			// Third ConfigMap
			configMap3 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "registry-cm-3",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"registry.json": `{
						"version": "1.0",
						"servers": [
							{
								"name": "server-c",
								"description": "Server C from ConfigMap 3",
								"image": "example.com/server-c:latest"
							}
						]
					}`,
				},
			}
			Expect(k8sClient.Create(ctx, configMap3)).Should(Succeed())

			By("creating MCPRegistry with multiple ConfigMap sources")
			registry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-cm-volumes-test",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Registries: []mcpv1alpha1.MCPRegistryConfig{
						{
							Name:   "source-alpha",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: configMap1.Name,
								},
								Key: "servers.json",
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "10m",
							},
						},
						{
							Name:   "source-beta",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: configMap2.Name,
								},
								Key: "data.json",
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "15m",
							},
						},
						{
							Name:   "source-gamma",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							ConfigMapRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: configMap3.Name,
								},
								Key: "registry.json",
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "20m",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, registry)).Should(Succeed())

			By("waiting for deployment to be created")
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      fmt.Sprintf("%s-api", registry.Name),
					Namespace: testNamespace,
				}, deployment)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying volumes are created for each ConfigMap source")
			// We should have at least 3 volumes for the ConfigMap sources
			// Plus possibly config and storage volumes
			Expect(len(deployment.Spec.Template.Spec.Volumes)).To(BeNumerically(">=", 3))

			// Verify each source has its own volume
			volumeNames := make(map[string]bool)
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				volumeNames[volume.Name] = true
			}

			// Check for expected volume names
			Expect(volumeNames["registry-data-0-source-alpha"]).To(BeTrue(), "Volume for source-alpha not found")
			Expect(volumeNames["registry-data-1-source-beta"]).To(BeTrue(), "Volume for source-beta not found")
			Expect(volumeNames["registry-data-2-source-gamma"]).To(BeTrue(), "Volume for source-gamma not found")

			// Verify volumes point to correct ConfigMaps
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				switch volume.Name {
				case "registry-data-0-source-alpha":
					Expect(volume.ConfigMap).NotTo(BeNil())
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(configMap1.Name))
					Expect(volume.ConfigMap.Items).To(HaveLen(1))
					Expect(volume.ConfigMap.Items[0].Key).To(Equal("servers.json"))
					Expect(volume.ConfigMap.Items[0].Path).To(Equal("registry.json"))
				case "registry-data-1-source-beta":
					Expect(volume.ConfigMap).NotTo(BeNil())
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(configMap2.Name))
					Expect(volume.ConfigMap.Items).To(HaveLen(1))
					Expect(volume.ConfigMap.Items[0].Key).To(Equal("data.json"))
					Expect(volume.ConfigMap.Items[0].Path).To(Equal("registry.json"))
				case "registry-data-2-source-gamma":
					Expect(volume.ConfigMap).NotTo(BeNil())
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(configMap3.Name))
					Expect(volume.ConfigMap.Items).To(HaveLen(1))
					Expect(volume.ConfigMap.Items[0].Key).To(Equal("registry.json"))
					Expect(volume.ConfigMap.Items[0].Path).To(Equal("registry.json"))
				}
			}

			By("verifying container has volume mounts at correct paths")
			container := deployment.Spec.Template.Spec.Containers[0]

			// Create map of mounts for easy checking
			mounts := make(map[string]string)
			for _, mount := range container.VolumeMounts {
				mounts[mount.Name] = mount.MountPath
			}

			// Verify mount paths match expected pattern /config/registry/{registryName}/
			Expect(mounts["registry-data-source-alpha"]).To(Equal("/config/registry/source-alpha"))
			Expect(mounts["registry-data-source-beta"]).To(Equal("/config/registry/source-beta"))
			Expect(mounts["registry-data-source-gamma"]).To(Equal("/config/registry/source-gamma"))

			// Verify all mounts are read-only
			for _, mount := range container.VolumeMounts {
				if mount.Name == "registry-data-source-alpha" ||
					mount.Name == "registry-data-source-beta" ||
					mount.Name == "registry-data-source-gamma" {
					Expect(mount.ReadOnly).To(BeTrue(), "ConfigMap mount should be read-only")
				}
			}

			By("verifying registry server config contains all sources with correct paths")
			configMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)
			serverConfig := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      configMapName,
					Namespace: testNamespace,
				}, serverConfig)
			}, QuickTimeout, DefaultPollingInterval).Should(Succeed())

			configYAML := serverConfig.Data["config.yaml"]
			Expect(configYAML).NotTo(BeEmpty())

			// Verify all three sources are in the config with correct file paths
			Expect(configYAML).To(ContainSubstring("name: source-alpha"))
			Expect(configYAML).To(ContainSubstring("name: source-beta"))
			Expect(configYAML).To(ContainSubstring("name: source-gamma"))

			// Verify file paths are correct
			Expect(configYAML).To(ContainSubstring("path: /config/registry/source-alpha/registry.json"))
			Expect(configYAML).To(ContainSubstring("path: /config/registry/source-beta/registry.json"))
			Expect(configYAML).To(ContainSubstring("path: /config/registry/source-gamma/registry.json"))

			// Verify sync intervals
			Expect(configYAML).To(ContainSubstring("interval: 10m"))
			Expect(configYAML).To(ContainSubstring("interval: 15m"))
			Expect(configYAML).To(ContainSubstring("interval: 20m"))

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, configMap1)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, configMap2)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, configMap3)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("multi-cm-volumes-test")
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})
})

// Shared helper functions (extracted from duplication)
// verifyServerConfigVolume verifies the deployment has the server config volume and mount
func (*serverConfigTestHelpers) verifyServerConfigVolume(deployment *appsv1.Deployment, expectedConfigMapName string) {
	// Check volume
	volumeFound := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == registryapi.RegistryServerConfigVolumeName && volume.ConfigMap != nil {
			Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(expectedConfigMapName))
			volumeFound = true
			break
		}
	}
	Expect(volumeFound).To(BeTrue(), "Deployment should have a volume for the registry config ConfigMap")

	// Check mount
	mountFound := false
	for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == registryapi.RegistryServerConfigVolumeName && mount.MountPath == config.RegistryServerConfigFilePath {
			mountFound = true
			break
		}
	}
	Expect(mountFound).To(BeTrue(), "Deployment should have a volume mount for the registry config ConfigMap")
}

func (*serverConfigTestHelpers) verifyStorageVolume(deployment *appsv1.Deployment) {
	// Check volume
	storageVolumeFound := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == "storage-data" && volume.EmptyDir != nil {
			storageVolumeFound = true
			break
		}
	}
	Expect(storageVolumeFound).To(BeTrue(), "Deployment should have an emptyDir volume for storage")

	// Check mount
	storageMountFound := false
	for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == "storage-data" {
			Expect(mount.MountPath).To(Equal("/data"))
			Expect(mount.ReadOnly).To(BeFalse(), "Storage mount should be writable")
			storageMountFound = true
			break
		}
	}
	Expect(storageMountFound).To(BeTrue(), "Deployment should have a volume mount for the storage emptyDir")
}

func (*serverConfigTestHelpers) verifyContainerArguments(deployment *appsv1.Deployment) {
	container := deployment.Spec.Template.Spec.Containers[0]
	Expect(container.Args).To(ContainElement("serve"))

	// Should have --config argument pointing to the server config file
	expectedConfigArg := fmt.Sprintf("--config=%s", filepath.Join(config.RegistryServerConfigFilePath, config.RegistryServerConfigFileName))
	Expect(container.Args).To(ContainElement(expectedConfigArg), "Container should have --config argument pointing to server config file")
}

// verifyConfigMapOwnership verifies the ConfigMap is owned by the MCPRegistry
func (*serverConfigTestHelpers) verifyConfigMapOwnership(configMap *corev1.ConfigMap, registry *mcpv1alpha1.MCPRegistry) {
	Expect(configMap.OwnerReferences).To(HaveLen(1))
	Expect(configMap.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
	Expect(configMap.OwnerReferences[0].Name).To(Equal(registry.Name))
	Expect(configMap.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))
}

// getDeploymentForRegistry gets the deployment for a registry
func (h *serverConfigTestHelpers) getDeploymentForRegistry(registryName string) *appsv1.Deployment {
	updatedRegistry, err := h.registryHelper.GetRegistry(registryName)
	Expect(err).NotTo(HaveOccurred())

	deployment, err := h.k8sHelper.GetDeployment(updatedRegistry.GetAPIResourceName())
	Expect(err).NotTo(HaveOccurred())

	return deployment
}

// verifyNoSourceDataVolume verifies there's no source data ConfigMap volume (for Git/API sources)
func (*serverConfigTestHelpers) verifyNoSourceDataVolume(deployment *appsv1.Deployment, sourceType string) {
	// With the new indexed naming, check that no volumes start with "registry-data-" and have ConfigMap
	sourceDataVolumeFound := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		// Check if this is a registry data volume (starts with "registry-data-" and has ConfigMap)
		if strings.HasPrefix(volume.Name, "registry-data-") && volume.ConfigMap != nil {
			sourceDataVolumeFound = true
			break
		}
	}
	Expect(sourceDataVolumeFound).To(BeFalse(),
		fmt.Sprintf("Deployment should NOT have a ConfigMap volume for the source data when using %s source", sourceType))
}

// verifySourceDataVolume verifies the source data ConfigMap volume for ConfigMap sources
func (*serverConfigTestHelpers) verifySourceDataVolume(deployment *appsv1.Deployment, registry *mcpv1alpha1.MCPRegistry) {
	// With multiple source support, we need to check each ConfigMap source
	for _, registryConfig := range registry.Spec.Registries {
		if registryConfig.ConfigMapRef != nil {
			expectedSourceConfigMapName := registryConfig.ConfigMapRef.Name
			expectedVolumeName := fmt.Sprintf("registry-data-source-%s", registryConfig.Name)
			expectedMountPath := filepath.Join(config.RegistryJSONFilePath, registryConfig.Name)

			// Check volume
			sourceDataVolumeFound := false
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == expectedVolumeName && volume.ConfigMap != nil {
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(expectedSourceConfigMapName))
					// Check that it mounts the correct key as registry.json
					Expect(volume.ConfigMap.Items).To(HaveLen(1))
					Expect(volume.ConfigMap.Items[0].Key).To(Equal(registryConfig.ConfigMapRef.Key))
					Expect(volume.ConfigMap.Items[0].Path).To(Equal("registry.json"))
					sourceDataVolumeFound = true
					break
				}
			}
			Expect(sourceDataVolumeFound).To(BeTrue(),
				fmt.Sprintf("Deployment should have a volume for ConfigMap source %s", registryConfig.Name))

			// Check mount
			sourceDataMountFound := false
			for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
				if mount.Name == expectedVolumeName {
					Expect(mount.MountPath).To(Equal(expectedMountPath))
					Expect(mount.ReadOnly).To(BeTrue())
					sourceDataMountFound = true
					break
				}
			}
			Expect(sourceDataMountFound).To(BeTrue(),
				fmt.Sprintf("Deployment should have a volume mount for ConfigMap source %s", registryConfig.Name))
		}
	}
}

// waitForAndGetServerConfigMap waits for the server config ConfigMap to be created and returns it
func (h *serverConfigTestHelpers) waitForAndGetServerConfigMap(registryName string) *corev1.ConfigMap {
	expectedConfigMapName := fmt.Sprintf("%s-registry-server-config", registryName)

	var serverConfigMap *corev1.ConfigMap
	Eventually(func() error {
		serverConfigMap = &corev1.ConfigMap{}
		return h.k8sClient.Get(h.ctx, client.ObjectKey{
			Name:      expectedConfigMapName,
			Namespace: h.testNamespace,
		}, serverConfigMap)
	}, MediumTimeout, DefaultPollingInterval).
		Should(Succeed(), "Registry server config ConfigMap should be created")

	return serverConfigMap
}

// verifyConfigMapBasics verifies the ConfigMap has required annotations and data
func (*serverConfigTestHelpers) verifyConfigMapBasics(configMap *corev1.ConfigMap) {
	// Verify the ConfigMap has the expected annotations
	Expect(configMap.Annotations).To(HaveKey("toolhive.stacklok.dev/content-checksum"))

	// Verify the ConfigMap has the config.yaml key with the registry configuration
	Expect(configMap.Data).To(HaveKey("config.yaml"))
	Expect(configMap.Data["config.yaml"]).NotTo(BeEmpty())
}

// verifyConfigMapContent verifies source-specific content in the config.yaml
func (*serverConfigTestHelpers) verifyConfigMapContent(configYAML string, registryName string, expectedContent map[string]string) {
	Expect(configYAML).To(ContainSubstring(fmt.Sprintf("registryName: %s", registryName)))
	Expect(configYAML).To(ContainSubstring("registries:"))
	Expect(configYAML).To(ContainSubstring("format: toolhive"))

	for key, value := range expectedContent {
		Expect(configYAML).To(ContainSubstring(fmt.Sprintf("%s: %s", key, value)))
	}
}
