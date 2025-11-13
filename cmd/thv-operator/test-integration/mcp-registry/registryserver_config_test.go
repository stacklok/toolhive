package operator_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("MCPRegistry Server Config", Label("k8s", "registry", "config"), func() {
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
			// The config ConfigMap name follows the pattern: {registry-name}-configmap
			expectedConfigMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)

			// Wait for the config ConfigMap to be created
			var serverConfigMap *corev1.ConfigMap
			Eventually(func() error {
				serverConfigMap = &corev1.ConfigMap{}
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      expectedConfigMapName,
					Namespace: testNamespace,
				}, serverConfigMap)
			}, MediumTimeout, DefaultPollingInterval).
				Should(Succeed(), "Registry server config ConfigMap should be created")

			By("validating the registry server config ConfigMap contents")
			// Verify the ConfigMap has the expected annotations
			Expect(serverConfigMap.Annotations).To(HaveKey("toolhive.stacklok.dev/content-checksum"))

			// Verify the ConfigMap has the config.yaml key with the registry configuration
			Expect(serverConfigMap.Data).To(HaveKey("config.yaml"))
			Expect(serverConfigMap.Data["config.yaml"]).NotTo(BeEmpty())

			// Verify the config.yaml contains expected registry configuration
			configYAML := serverConfigMap.Data["config.yaml"]
			Expect(configYAML).To(ContainSubstring("registryName: test-registry"))
			Expect(configYAML).To(ContainSubstring("type: file")) // ConfigMap sources become file type in the server config
			Expect(configYAML).To(ContainSubstring("path: /etc/registry/registry.json"))
			Expect(configYAML).To(ContainSubstring("format: toolhive"))
			Expect(configYAML).To(ContainSubstring("interval: 1h"))

			By("verifying the ConfigMap is owned by the MCPRegistry")
			// Verify ownership
			Expect(serverConfigMap.OwnerReferences).To(HaveLen(1))
			Expect(serverConfigMap.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
			Expect(serverConfigMap.OwnerReferences[0].Name).To(Equal(registry.Name))
			Expect(serverConfigMap.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			By("verifying the ConfigMap is referenced correctly in deployment")
			// Get the updated registry to check deployment status
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			// Get the deployment
			deployment, err := k8sHelper.GetDeployment(updatedRegistry.GetAPIResourceName())
			Expect(err).NotTo(HaveOccurred())

			By("checking registry server config ConfigMap volume and mount")
			// Check that the deployment has a volume for the config ConfigMap
			volumeFound := false
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == registryapi.RegistryServerConfigVolumeName && volume.ConfigMap != nil {
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(expectedConfigMapName))
					volumeFound = true
					break
				}
			}
			Expect(volumeFound).To(BeTrue(), "Deployment should have a volume for the registry config ConfigMap")

			mountFound := false
			for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
				if mount.Name == registryapi.RegistryServerConfigVolumeName && mount.MountPath == config.RegistryServerConfigFilePath {
					mountFound = true
					break
				}
			}
			Expect(mountFound).To(BeTrue(), "Deployment should have a volume mount for the registry config ConfigMap")

			By("checking registry source data ConfigMap volume and mount")
			// Since the source is a ConfigMap, verify the source data volume is also present
			sourceDataVolumeFound := false
			expectedSourceConfigMapName := updatedRegistry.GetConfigMapSourceName()
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == registryapi.RegistryDataVolumeName && volume.ConfigMap != nil {
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(expectedSourceConfigMapName))
					sourceDataVolumeFound = true
					break
				}
			}
			Expect(sourceDataVolumeFound).To(BeTrue(), "Deployment should have a volume for the source data ConfigMap")

			// Check that the source data volume mount exists
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

			By("verifying container arguments use the server config")
			// Verify the container has the correct arguments
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Args).To(ContainElement("serve"))
			// Should have --config argument pointing to the server config file
			configArgFound := false
			registryNameArgFound := false
			for _, arg := range container.Args {
				if arg == fmt.Sprintf("--config=%s", config.RegistryServerConfigFilePath) {
					configArgFound = true
				}
				if arg == fmt.Sprintf("--registry-name=%s", registry.Name) {
					registryNameArgFound = true
				}
			}
			Expect(configArgFound).To(BeTrue(), "Container should have --config argument pointing to server config file")
			Expect(registryNameArgFound).To(BeTrue(), "Container should have --registry-name argument pointing to registry name")
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
			// The config ConfigMap name follows the pattern: {registry-name}-registry-server-config
			expectedConfigMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)

			// Wait for the config ConfigMap to be created
			var serverConfigMap *corev1.ConfigMap
			Eventually(func() error {
				serverConfigMap = &corev1.ConfigMap{}
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      expectedConfigMapName,
					Namespace: testNamespace,
				}, serverConfigMap)
			}, MediumTimeout, DefaultPollingInterval).
				Should(Succeed(), "Registry server config ConfigMap should be created")

			By("validating the registry server config ConfigMap contents")
			// Verify the ConfigMap has the expected annotations
			Expect(serverConfigMap.Annotations).To(HaveKey("toolhive.stacklok.dev/content-checksum"))

			// Verify the ConfigMap has the config.yaml key with the registry configuration
			Expect(serverConfigMap.Data).To(HaveKey("config.yaml"))
			Expect(serverConfigMap.Data["config.yaml"]).NotTo(BeEmpty())

			// Verify the config.yaml contains expected Git source configuration
			configYAML := serverConfigMap.Data["config.yaml"]
			Expect(configYAML).To(ContainSubstring("registryName: test-git-registry"))
			Expect(configYAML).To(ContainSubstring("type: git")) // Git source type
			Expect(configYAML).To(ContainSubstring("repository: https://github.com/mcp-servers/example-registry.git"))
			Expect(configYAML).To(ContainSubstring("branch: main"))
			Expect(configYAML).To(ContainSubstring("format: toolhive"))
			Expect(configYAML).To(ContainSubstring("interval: 2h"))

			By("verifying the ConfigMap is owned by the MCPRegistry")
			// Verify ownership
			Expect(serverConfigMap.OwnerReferences).To(HaveLen(1))
			Expect(serverConfigMap.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
			Expect(serverConfigMap.OwnerReferences[0].Name).To(Equal(registry.Name))
			Expect(serverConfigMap.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			By("verifying the ConfigMap is referenced correctly in deployment")
			// Get the updated registry to check deployment status
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			// Get the deployment
			deployment, err := k8sHelper.GetDeployment(updatedRegistry.GetAPIResourceName())
			Expect(err).NotTo(HaveOccurred())

			By("checking registry server config ConfigMap volume and mount")
			// Check that the deployment has a volume for the config ConfigMap
			volumeFound := false
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == registryapi.RegistryServerConfigVolumeName && volume.ConfigMap != nil {
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(expectedConfigMapName))
					volumeFound = true
					break
				}
			}
			Expect(volumeFound).To(BeTrue(), "Deployment should have a volume for the registry config ConfigMap")

			mountFound := false
			for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
				if mount.Name == registryapi.RegistryServerConfigVolumeName && mount.MountPath == config.RegistryServerConfigFilePath {
					mountFound = true
					break
				}
			}
			Expect(mountFound).To(BeTrue(), "Deployment should have a volume mount for the registry config ConfigMap")

			By("verifying Git source doesn't require source data ConfigMap volume")
			// Git source fetches data directly, so there should NOT be a registry data ConfigMap volume
			sourceDataVolumeFound := false
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == registryapi.RegistryDataVolumeName && volume.ConfigMap != nil {
					sourceDataVolumeFound = true
					break
				}
			}
			Expect(sourceDataVolumeFound).To(BeFalse(), "Deployment should NOT have a ConfigMap volume for the source data when using Git source")

			By("verifying container arguments use the server config")
			// Verify the container has the correct arguments
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Args).To(ContainElement("serve"))
			// Should have --config argument pointing to the server config file
			configArgFound := false
			registryNameArgFound := false
			for _, arg := range container.Args {
				if arg == fmt.Sprintf("--config=%s", config.RegistryServerConfigFilePath) {
					configArgFound = true
				}
				if arg == fmt.Sprintf("--registry-name=%s", registry.Name) {
					registryNameArgFound = true
				}
			}
			Expect(configArgFound).To(BeTrue(), "Container should have --config argument pointing to server config file")
			Expect(registryNameArgFound).To(BeTrue(), "Container should have --registry-name argument pointing to registry name")
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
			// The config ConfigMap name follows the pattern: {registry-name}-registry-server-config
			expectedConfigMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)

			// Wait for the config ConfigMap to be created
			var serverConfigMap *corev1.ConfigMap
			Eventually(func() error {
				serverConfigMap = &corev1.ConfigMap{}
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      expectedConfigMapName,
					Namespace: testNamespace,
				}, serverConfigMap)
			}, MediumTimeout, DefaultPollingInterval).
				Should(Succeed(), "Registry server config ConfigMap should be created")

			By("validating the registry server config ConfigMap contents")
			// Verify the ConfigMap has the expected annotations
			Expect(serverConfigMap.Annotations).To(HaveKey("toolhive.stacklok.dev/content-checksum"))

			// Verify the ConfigMap has the config.yaml key with the registry configuration
			Expect(serverConfigMap.Data).To(HaveKey("config.yaml"))
			Expect(serverConfigMap.Data["config.yaml"]).NotTo(BeEmpty())

			// Verify the config.yaml contains expected API source configuration
			configYAML := serverConfigMap.Data["config.yaml"]
			Expect(configYAML).To(ContainSubstring("registryName: test-api-registry"))
			Expect(configYAML).To(ContainSubstring("type: api")) // API source type
			Expect(configYAML).To(ContainSubstring("endpoint: http://registry-api.default.svc.cluster.local:8080/api"))
			Expect(configYAML).To(ContainSubstring("format: toolhive"))
			Expect(configYAML).To(ContainSubstring("interval: 30m"))

			By("verifying the ConfigMap is owned by the MCPRegistry")
			// Verify ownership
			Expect(serverConfigMap.OwnerReferences).To(HaveLen(1))
			Expect(serverConfigMap.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
			Expect(serverConfigMap.OwnerReferences[0].Name).To(Equal(registry.Name))
			Expect(serverConfigMap.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			By("verifying the ConfigMap is referenced correctly in deployment")
			// Get the updated registry to check deployment status
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			// Get the deployment
			deployment, err := k8sHelper.GetDeployment(updatedRegistry.GetAPIResourceName())
			Expect(err).NotTo(HaveOccurred())

			By("checking registry server config ConfigMap volume and mount")
			// Check that the deployment has a volume for the config ConfigMap
			volumeFound := false
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == registryapi.RegistryServerConfigVolumeName && volume.ConfigMap != nil {
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(expectedConfigMapName))
					volumeFound = true
					break
				}
			}
			Expect(volumeFound).To(BeTrue(), "Deployment should have a volume for the registry config ConfigMap")

			mountFound := false
			for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
				if mount.Name == registryapi.RegistryServerConfigVolumeName && mount.MountPath == config.RegistryServerConfigFilePath {
					mountFound = true
					break
				}
			}
			Expect(mountFound).To(BeTrue(), "Deployment should have a volume mount for the registry config ConfigMap")

			By("verifying API source doesn't require source data ConfigMap volume")
			// API source fetches data directly from the endpoint, so there should NOT be a registry data ConfigMap volume
			sourceDataVolumeFound := false
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == registryapi.RegistryDataVolumeName && volume.ConfigMap != nil {
					sourceDataVolumeFound = true
					break
				}
			}
			Expect(sourceDataVolumeFound).To(BeFalse(), "Deployment should NOT have a ConfigMap volume for the source data when using API source")

			By("verifying container arguments use the server config")
			// Verify the container has the correct arguments
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Args).To(ContainElement("serve"))
			// Should have --config argument pointing to the server config file
			configArgFound := false
			registryNameArgFound := false
			for _, arg := range container.Args {
				if arg == fmt.Sprintf("--config=%s", config.RegistryServerConfigFilePath) {
					configArgFound = true
				}
				if arg == fmt.Sprintf("--registry-name=%s", registry.Name) {
					registryNameArgFound = true
				}
			}
			Expect(configArgFound).To(BeTrue(), "Container should have --config argument pointing to server config file")
			Expect(registryNameArgFound).To(BeTrue(), "Container should have --registry-name argument pointing to registry name")
		})
	})
})
