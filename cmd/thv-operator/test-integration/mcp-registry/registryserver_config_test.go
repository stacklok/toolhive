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

var _ = Describe("MCPRegistry Server Config", Label("k8s", "registry", "config"), func() {
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

	Context("Registry Server Config ConfigMap Creation - ConfigMap Source", func() {
		It("should create a registry server config ConfigMap when MCPRegistry is created", func() {
			By("creating a test ConfigMap with sample registry data")
			configMap, _ := configMapHelper.CreateSampleToolHiveRegistry("test-config")

			By("creating an MCPRegistry resource")
			registry := registryHelper.NewRegistryBuilder("test-registry").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			// Verify registry was created
			Expect(registry.Name).To(Equal("test-registry"))
			Expect(registry.Namespace).To(Equal(testNamespace))

			By("waiting for registry initialization")
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			By("verifying registry server config ConfigMap is created")
			serverConfigMap := testHelpers.waitForAndGetServerConfigMap(registry.Name)

			By("validating the registry server config ConfigMap contents")
			testHelpers.verifyConfigMapBasics(serverConfigMap)

			// Verify source-specific content
			configYAML := serverConfigMap.Data["config.yaml"]
			testHelpers.verifyConfigMapContent(configYAML, registry.Name, map[string]string{
				"type":     "file", // ConfigMap sources become file type in the server config
				"path":     filepath.Join(config.RegistryJSONFilePath, config.RegistryJSONFileName),
				"interval": "1h",
			})

			By("verifying the ConfigMap is owned by the MCPRegistry")
			testHelpers.verifyConfigMapOwnership(serverConfigMap, registry)

			By("verifying the ConfigMap is referenced correctly in deployment")
			deployment := testHelpers.getDeploymentForRegistry(registry.Name)

			By("checking registry server config ConfigMap volume and mount")
			expectedConfigMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)
			testHelpers.verifyServerConfigVolume(deployment, expectedConfigMapName)

			By("checking registry source data ConfigMap volume and mount")
			testHelpers.verifySourceDataVolume(deployment, registry)

			By("checking storage emptyDir volume and mount")
			testHelpers.verifyStorageVolume(deployment)

			By("verifying container arguments use the server config")
			testHelpers.verifyContainerArguments(deployment)
		})
	})

	Context("Registry Server Config ConfigMap Creation - Git Source", func() {
		It("should create a registry server config ConfigMap when MCPRegistry with Git source is created", func() {
			By("creating an MCPRegistry resource with Git source")
			registry := registryHelper.NewRegistryBuilder("test-git-registry").
				WithGitSource(
					"https://github.com/mcp-servers/example-registry.git",
					"main",
					"registry.json",
				).
				WithSyncPolicy("2h").
				Create(registryHelper)

			// Verify registry was created
			Expect(registry.Name).To(Equal("test-git-registry"))
			Expect(registry.Namespace).To(Equal(testNamespace))

			By("waiting for registry initialization")
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			By("verifying registry server config ConfigMap is created")
			serverConfigMap := testHelpers.waitForAndGetServerConfigMap(registry.Name)

			By("validating the registry server config ConfigMap contents")
			testHelpers.verifyConfigMapBasics(serverConfigMap)

			// Verify source-specific content
			configYAML := serverConfigMap.Data["config.yaml"]
			testHelpers.verifyConfigMapContent(configYAML, registry.Name, map[string]string{
				"type":       "git",
				"repository": "https://github.com/mcp-servers/example-registry.git",
				"branch":     "main",
				"interval":   "2h",
			})

			By("verifying the ConfigMap is owned by the MCPRegistry")
			testHelpers.verifyConfigMapOwnership(serverConfigMap, registry)

			By("verifying the ConfigMap is referenced correctly in deployment")
			deployment := testHelpers.getDeploymentForRegistry(registry.Name)

			By("checking registry server config ConfigMap volume and mount")
			expectedConfigMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)
			testHelpers.verifyServerConfigVolume(deployment, expectedConfigMapName)

			By("verifying Git source doesn't require source data ConfigMap volume")
			testHelpers.verifyNoSourceDataVolume(deployment, "Git")

			By("checking storage emptyDir volume and mount")
			testHelpers.verifyStorageVolume(deployment)

			By("verifying container arguments use the server config")
			testHelpers.verifyContainerArguments(deployment)
		})
	})

	Context("Registry Server Config ConfigMap Creation - API Source", func() {
		It("should create a registry server config ConfigMap when MCPRegistry with API source is created", func() {
			By("creating an MCPRegistry resource with API source")
			registry := registryHelper.NewRegistryBuilder("test-api-registry").
				WithAPISource("http://registry-api.default.svc.cluster.local:8080/api").
				WithSyncPolicy("30m").
				Create(registryHelper)

			// Verify registry was created
			Expect(registry.Name).To(Equal("test-api-registry"))
			Expect(registry.Namespace).To(Equal(testNamespace))

			By("waiting for registry initialization")
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)

			By("verifying registry server config ConfigMap is created")
			serverConfigMap := testHelpers.waitForAndGetServerConfigMap(registry.Name)

			By("validating the registry server config ConfigMap contents")
			testHelpers.verifyConfigMapBasics(serverConfigMap)

			// Verify source-specific content
			configYAML := serverConfigMap.Data["config.yaml"]
			testHelpers.verifyConfigMapContent(configYAML, registry.Name, map[string]string{
				"type":     "api",
				"endpoint": "http://registry-api.default.svc.cluster.local:8080/api",
				"interval": "30m",
			})

			By("verifying the ConfigMap is owned by the MCPRegistry")
			testHelpers.verifyConfigMapOwnership(serverConfigMap, registry)

			By("verifying the ConfigMap is referenced correctly in deployment")
			deployment := testHelpers.getDeploymentForRegistry(registry.Name)

			By("checking registry server config ConfigMap volume and mount")
			expectedConfigMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)
			testHelpers.verifyServerConfigVolume(deployment, expectedConfigMapName)

			By("verifying API source doesn't require source data ConfigMap volume")
			testHelpers.verifyNoSourceDataVolume(deployment, "API")

			By("checking storage emptyDir volume and mount")
			testHelpers.verifyStorageVolume(deployment)

			By("verifying container arguments use the server config")
			testHelpers.verifyContainerArguments(deployment)
		})
	})
})

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
func (h *serverConfigTestHelpers) verifyConfigMapBasics(configMap *corev1.ConfigMap) {
	// Verify the ConfigMap has the expected annotations
	Expect(configMap.Annotations).To(HaveKey("toolhive.stacklok.dev/content-checksum"))

	// Verify the ConfigMap has the config.yaml key with the registry configuration
	Expect(configMap.Data).To(HaveKey("config.yaml"))
	Expect(configMap.Data["config.yaml"]).NotTo(BeEmpty())
}

// verifyConfigMapOwnership verifies the ConfigMap is owned by the MCPRegistry
func (h *serverConfigTestHelpers) verifyConfigMapOwnership(configMap *corev1.ConfigMap, registry *mcpv1alpha1.MCPRegistry) {
	Expect(configMap.OwnerReferences).To(HaveLen(1))
	Expect(configMap.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
	Expect(configMap.OwnerReferences[0].Name).To(Equal(registry.Name))
	Expect(configMap.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))
}

// verifyConfigMapContent verifies source-specific content in the config.yaml
func (h *serverConfigTestHelpers) verifyConfigMapContent(configYAML string, registryName string, expectedContent map[string]string) {
	Expect(configYAML).To(ContainSubstring(fmt.Sprintf("registryName: %s", registryName)))
	Expect(configYAML).To(ContainSubstring("format: toolhive"))

	for key, value := range expectedContent {
		Expect(configYAML).To(ContainSubstring(fmt.Sprintf("%s: %s", key, value)))
	}
}

// verifyServerConfigVolume verifies the deployment has the server config volume and mount
func (h *serverConfigTestHelpers) verifyServerConfigVolume(deployment *appsv1.Deployment, expectedConfigMapName string) {
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

// verifyStorageVolume verifies the deployment has the storage emptyDir volume and mount
func (h *serverConfigTestHelpers) verifyStorageVolume(deployment *appsv1.Deployment) {
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

// verifySourceDataVolume verifies the source data ConfigMap volume for ConfigMap sources
func (h *serverConfigTestHelpers) verifySourceDataVolume(deployment *appsv1.Deployment, registry *mcpv1alpha1.MCPRegistry) {
	expectedSourceConfigMapName := registry.GetConfigMapSourceName()

	// Check volume
	sourceDataVolumeFound := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == registryapi.RegistryDataVolumeName && volume.ConfigMap != nil {
			Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(expectedSourceConfigMapName))
			sourceDataVolumeFound = true
			break
		}
	}
	Expect(sourceDataVolumeFound).To(BeTrue(), "Deployment should have a volume for the source data ConfigMap")

	// Check mount
	sourceDataMountFound := false
	for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == registryapi.RegistryDataVolumeName {
			Expect(mount.MountPath).To(Equal(config.RegistryJSONFilePath))
			Expect(mount.ReadOnly).To(BeTrue())
			sourceDataMountFound = true
			break
		}
	}
	Expect(sourceDataMountFound).To(BeTrue(), "Deployment should have a volume mount for the source data ConfigMap")
}

// verifyNoSourceDataVolume verifies there's no source data ConfigMap volume (for Git/API sources)
func (h *serverConfigTestHelpers) verifyNoSourceDataVolume(deployment *appsv1.Deployment, sourceType string) {
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

// verifyContainerArguments verifies the container has the correct arguments
func (h *serverConfigTestHelpers) verifyContainerArguments(deployment *appsv1.Deployment) {
	container := deployment.Spec.Template.Spec.Containers[0]
	Expect(container.Args).To(ContainElement("serve"))

	// Should have --config argument pointing to the server config file
	expectedConfigArg := fmt.Sprintf("--config=%s", filepath.Join(config.RegistryServerConfigFilePath, config.RegistryServerConfigFileName))
	Expect(container.Args).To(ContainElement(expectedConfigArg), "Container should have --config argument pointing to server config file")
}

// getDeploymentForRegistry gets the deployment for a registry
func (h *serverConfigTestHelpers) getDeploymentForRegistry(registryName string) *appsv1.Deployment {
	updatedRegistry, err := h.registryHelper.GetRegistry(registryName)
	Expect(err).NotTo(HaveOccurred())

	deployment, err := h.k8sHelper.GetDeployment(updatedRegistry.GetAPIResourceName())
	Expect(err).NotTo(HaveOccurred())

	return deployment
}
