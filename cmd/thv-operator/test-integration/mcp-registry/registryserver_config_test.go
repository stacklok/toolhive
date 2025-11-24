package operator_test

import (
	"context"
	"fmt"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
				"type":     "file", // ConfigMap sources become file type in the server config
				"path":     filepath.Join(fmt.Sprintf("%s/source-0", config.RegistryJSONFilePath), config.RegistryJSONFileName),
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
				"type":       "git",
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
				"type":     "api",
				"endpoint": "http://registry-api.default.svc.cluster.local:8080/api",
				"interval": "30m",
			},
			func(deployment *appsv1.Deployment, _ *mcpv1alpha1.MCPRegistry) {
				// API sources should NOT have the source data volume
				testHelpers.verifyNoSourceDataVolume(deployment, "API")
			},
		),
	)
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
	sourceDataVolumeFound := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == registryapi.RegistryDataVolumeName && volume.ConfigMap != nil {
			sourceDataVolumeFound = true
			break
		}
	}
	Expect(sourceDataVolumeFound).To(BeFalse(),
		fmt.Sprintf("Deployment should NOT have a ConfigMap volume for the source data when using %s source", sourceType))
}

// verifySourceDataVolume verifies the source data ConfigMap volumes for ConfigMap sources
func (*serverConfigTestHelpers) verifySourceDataVolume(deployment *appsv1.Deployment, registry *mcpv1alpha1.MCPRegistry) {
	// Get all ConfigMap sources
	configMapSources := registry.GetConfigMapSources()

	// For each ConfigMap source, verify its volume and mount
	for i, source := range configMapSources {
		if source.ConfigMapRef == nil {
			continue
		}

		expectedVolumeName := fmt.Sprintf("registry-source-%d-%s", i, source.Name)
		expectedMountPath := fmt.Sprintf("%s/source-%d", config.RegistryJSONFilePath, i)

		// Check volume exists
		volumeFound := false
		for _, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.Name == expectedVolumeName && volume.ConfigMap != nil {
				Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(source.ConfigMapRef.Name))
				volumeFound = true
				break
			}
		}
		Expect(volumeFound).To(BeTrue(), fmt.Sprintf("Deployment should have a volume for source %s", source.Name))

		// Check mount exists
		mountFound := false
		for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
			if mount.Name == expectedVolumeName {
				Expect(mount.MountPath).To(Equal(expectedMountPath))
				Expect(mount.ReadOnly).To(BeTrue())
				mountFound = true
				break
			}
		}
		Expect(mountFound).To(BeTrue(), fmt.Sprintf("Deployment should have a volume mount for source %s", source.Name))
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
	Expect(configYAML).To(ContainSubstring("format: toolhive"))

	for key, value := range expectedContent {
		Expect(configYAML).To(ContainSubstring(fmt.Sprintf("%s: %s", key, value)))
	}
}
