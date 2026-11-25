// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
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
			setupRegistry func() *mcpv1beta1.MCPRegistry,
			expectedConfigContent map[string]string,
			verifySourceVolume func(*appsv1.Deployment, *mcpv1beta1.MCPRegistry),
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

			service, err := k8sHelper.GetService(apiResourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(service.Name).To(Equal(apiResourceName))
			Expect(service.Namespace).To(Equal(testNamespace))
			Expect(service.Spec.Ports).To(HaveLen(1))
			Expect(service.Spec.Ports[0].Name).To(Equal("http"))

			// Verify the Deployment has correct configuration
			By("verifying the deployment is created")
			deployment := testHelpers.getDeploymentForRegistry(registry.Name)
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
				mcpv1beta1.MCPRegistryPhasePending, // API deployment in progress
				mcpv1beta1.MCPRegistryPhaseReady,   // If somehow API becomes ready
			))

			// Verify ObservedGeneration is set after reconciliation
			Expect(registry.Status.ObservedGeneration).To(Equal(registry.Generation))

			// Verify phase and URL
			if registry.Status.Phase == mcpv1beta1.MCPRegistryPhaseReady {
				Expect(registry.Status.URL).To(Equal(fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", apiResourceName, testNamespace)))
			}

			By("verifying registry server config ConfigMap is created")
			serverConfigMap := testHelpers.waitForAndGetServerConfigMap(registry.Name)

			By("validating the registry server config ConfigMap contents")
			// Verify basic properties
			testHelpers.verifyConfigMapBasics(serverConfigMap)

			// Verify source-specific content: In the new model, the ConfigMap contains
			// the verbatim configYAML, so we verify expected content strings are present
			configYAML := serverConfigMap.Data["config.yaml"]
			testHelpers.verifyConfigMapContent(configYAML, registry.Name, expectedConfigContent)

			// Verify the appropriate source type field is present (file, git, or api)
			// based on the configYAML content
			if strings.Contains(registry.Spec.ConfigYAML, "file:") {
				Expect(configYAML).To(ContainSubstring("file:"), "ConfigMap source should have file field")
			} else if strings.Contains(registry.Spec.ConfigYAML, "git:") {
				Expect(configYAML).To(ContainSubstring("git:"), "Git source should have git field")
			} else if strings.Contains(registry.Spec.ConfigYAML, "api:") {
				Expect(configYAML).To(ContainSubstring("api:"), "API source should have api field")
			}

			By("verifying the ConfigMap is owned by the MCPRegistry")
			testHelpers.verifyConfigMapOwnership(serverConfigMap, registry)

			By("checking registry server config ConfigMap volume and mount")
			testHelpers.verifyServerConfigVolume(deployment, serverConfigMap.Name)

			By("checking source-specific volumes")
			verifySourceVolume(deployment, registry)

			By("verifying container arguments use the server config")
			testHelpers.verifyContainerArguments(deployment)
		},

		Entry("ConfigMap Source",
			"test-config-registry",
			func() *mcpv1beta1.MCPRegistry {
				configMap := configMapHelper.CreateSampleToolHiveRegistry("test-config")
				return registryHelper.NewRegistryBuilder("test-config-registry").
					WithConfigMapSource(configMap.Name, "registry.json").
					WithSyncPolicy("1h").
					WithLabel("app", "test-config-registry").
					WithAnnotation("description", "Test config registry").
					Create(registryHelper)
			},
			map[string]string{
				"path":     "/config/registry/default/registry.json",
				"interval": "1h",
			},
			func(deployment *appsv1.Deployment, registry *mcpv1beta1.MCPRegistry) {
				// ConfigMap sources need the source data volume
				testHelpers.verifySourceDataVolume(deployment, registry)
			},
		),

		Entry("Git Source",
			"test-git-registry",
			func() *mcpv1beta1.MCPRegistry {
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

			func(deployment *appsv1.Deployment, _ *mcpv1beta1.MCPRegistry) {
				// Git sources should NOT have the source data volume
				testHelpers.verifyNoSourceDataVolume(deployment, "Git")
			},
		),

		Entry("API Source",
			"test-api-registry",
			func() *mcpv1beta1.MCPRegistry {
				return registryHelper.NewRegistryBuilder("test-api-registry").
					WithAPISource("http://registry-api.default.svc.cluster.local:8080/api").
					WithSyncPolicy("30m").
					Create(registryHelper)
			},
			map[string]string{
				"endpoint": "http://registry-api.default.svc.cluster.local:8080/api",
				"interval": "30m",
			},
			func(deployment *appsv1.Deployment, _ *mcpv1beta1.MCPRegistry) {
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

			By("creating MCPRegistry with multiple ConfigMap sources via configYAML")
			configYAML := buildConfigYAMLForMultipleSources([]map[string]string{
				{
					"name":       "alpha",
					"sourceType": "file",
					"filePath":   "/config/registry/alpha/registry.json",
					"interval":   "10m",
				},
				{
					"name":       "beta",
					"sourceType": "file",
					"filePath":   "/config/registry/beta/registry.json",
					"interval":   "15m",
				},
				{
					"name":       "gamma",
					"sourceType": "file",
					"filePath":   "/config/registry/gamma/registry.json",
					"interval":   "20m",
				},
			})

			// Build volumes for all three ConfigMap sources
			volumes := []apiextensionsv1.JSON{
				{Raw: mustMarshalJSON(corev1.Volume{
					Name: "registry-data-source-alpha",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: configMap1.Name},
							Items:                []corev1.KeyToPath{{Key: "servers.json", Path: "registry.json"}},
						},
					},
				})},
				{Raw: mustMarshalJSON(corev1.Volume{
					Name: "registry-data-source-beta",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: configMap2.Name},
							Items:                []corev1.KeyToPath{{Key: "data.json", Path: "registry.json"}},
						},
					},
				})},
				{Raw: mustMarshalJSON(corev1.Volume{
					Name: "registry-data-source-gamma",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: configMap3.Name},
							Items:                []corev1.KeyToPath{{Key: "registry.json", Path: "registry.json"}},
						},
					},
				})},
			}

			// Build volume mounts for all three sources
			volumeMounts := []apiextensionsv1.JSON{
				{Raw: mustMarshalJSON(corev1.VolumeMount{
					Name: "registry-data-source-alpha", MountPath: "/config/registry/alpha", ReadOnly: true,
				})},
				{Raw: mustMarshalJSON(corev1.VolumeMount{
					Name: "registry-data-source-beta", MountPath: "/config/registry/beta", ReadOnly: true,
				})},
				{Raw: mustMarshalJSON(corev1.VolumeMount{
					Name: "registry-data-source-gamma", MountPath: "/config/registry/gamma", ReadOnly: true,
				})},
			}

			registry := &mcpv1beta1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-cm-volumes-test",
					Namespace: testNamespace,
				},
				Spec: mcpv1beta1.MCPRegistrySpec{
					ConfigYAML:   configYAML,
					Volumes:      volumes,
					VolumeMounts: volumeMounts,
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
			Expect(volumeNames["registry-data-source-alpha"]).To(BeTrue(), "Volume for source-alpha not found")
			Expect(volumeNames["registry-data-source-beta"]).To(BeTrue(), "Volume for source-beta not found")
			Expect(volumeNames["registry-data-source-gamma"]).To(BeTrue(), "Volume for source-gamma not found")

			// Verify volumes point to correct ConfigMaps
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				switch volume.Name {
				case "registry-data-source-alpha":
					Expect(volume.ConfigMap).NotTo(BeNil())
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(configMap1.Name))
					Expect(volume.ConfigMap.Items).To(HaveLen(1))
					Expect(volume.ConfigMap.Items[0].Key).To(Equal("servers.json"))
					Expect(volume.ConfigMap.Items[0].Path).To(Equal("registry.json"))
				case "registry-data-source-beta":
					Expect(volume.ConfigMap).NotTo(BeNil())
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(configMap2.Name))
					Expect(volume.ConfigMap.Items).To(HaveLen(1))
					Expect(volume.ConfigMap.Items[0].Key).To(Equal("data.json"))
					Expect(volume.ConfigMap.Items[0].Path).To(Equal("registry.json"))
				case "registry-data-source-gamma":
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
			Expect(mounts["registry-data-source-alpha"]).To(Equal("/config/registry/alpha"))
			Expect(mounts["registry-data-source-beta"]).To(Equal("/config/registry/beta"))
			Expect(mounts["registry-data-source-gamma"]).To(Equal("/config/registry/gamma"))

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

			serverConfigYAML := serverConfig.Data["config.yaml"]
			Expect(serverConfigYAML).NotTo(BeEmpty())

			// Verify all three sources are in the config with correct file paths
			Expect(serverConfigYAML).To(ContainSubstring("name: alpha"))
			Expect(serverConfigYAML).To(ContainSubstring("name: beta"))
			Expect(serverConfigYAML).To(ContainSubstring("name: gamma"))

			// Verify file paths are correct
			Expect(serverConfigYAML).To(ContainSubstring("path: /config/registry/alpha/registry.json"))
			Expect(serverConfigYAML).To(ContainSubstring("path: /config/registry/beta/registry.json"))
			Expect(serverConfigYAML).To(ContainSubstring("path: /config/registry/gamma/registry.json"))

			// Verify sync intervals
			Expect(serverConfigYAML).To(ContainSubstring("interval: 10m"))
			Expect(serverConfigYAML).To(ContainSubstring("interval: 15m"))
			Expect(serverConfigYAML).To(ContainSubstring("interval: 20m"))

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

	Describe("Git Authentication", func() {
		It("should mount git auth secret for private repository", func() {
			By("creating a secret for Git authentication")
			gitSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "git-auth-secret",
					Namespace: testNamespace,
				},
				StringData: map[string]string{
					"token": "ghp_test_authentication_token",
				},
			}
			Expect(k8sClient.Create(ctx, gitSecret)).Should(Succeed())

			By("creating MCPRegistry with Git source and authentication via configYAML")
			// Build configYAML with git auth
			gitConfigYAML := buildConfigYAMLForMultipleSources([]map[string]string{
				{
					"name":             "default",
					"sourceType":       "git",
					"repository":       "https://github.com/example/private-repo.git",
					"branch":           "main",
					"path":             "registry.json",
					"authUsername":     "git",
					"authPasswordFile": "/secrets/git-auth-secret/token",
					"interval":         "1h",
				},
			})

			// Build secret volume and mount for git auth
			secretVol := corev1.Volume{
				Name: "git-auth-git-auth-secret",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: "git-auth-secret",
						Items:      []corev1.KeyToPath{{Key: "token", Path: "token"}},
					},
				},
			}
			secretMount := corev1.VolumeMount{
				Name:      "git-auth-git-auth-secret",
				MountPath: "/secrets/git-auth-secret",
				ReadOnly:  true,
			}

			registry := &mcpv1beta1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "git-auth-test",
					Namespace: testNamespace,
				},
				Spec: mcpv1beta1.MCPRegistrySpec{
					ConfigYAML:   gitConfigYAML,
					Volumes:      []apiextensionsv1.JSON{{Raw: mustMarshalJSON(secretVol)}},
					VolumeMounts: []apiextensionsv1.JSON{{Raw: mustMarshalJSON(secretMount)}},
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

			By("verifying git auth volume is mounted")
			verifyGitAuthVolume(deployment, "git-auth-secret", "token")

			By("verifying registry server config contains auth settings")
			configMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)
			serverConfig := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      configMapName,
					Namespace: testNamespace,
				}, serverConfig)
			}, QuickTimeout, DefaultPollingInterval).Should(Succeed())

			serverConfigYAML := serverConfig.Data["config.yaml"]
			Expect(serverConfigYAML).To(ContainSubstring("auth:"))
			Expect(serverConfigYAML).To(ContainSubstring("username: git"))
			Expect(serverConfigYAML).To(ContainSubstring("passwordFile: /secrets/git-auth-secret/token"))

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, gitSecret)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("git-auth-test")
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("should handle multiple git registries with different auth secrets", func() {
			By("creating secrets for Git authentication")
			gitSecret1 := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "git-auth-1",
					Namespace: testNamespace,
				},
				StringData: map[string]string{
					"password": "secret1",
				},
			}
			Expect(k8sClient.Create(ctx, gitSecret1)).Should(Succeed())

			gitSecret2 := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "git-auth-2",
					Namespace: testNamespace,
				},
				StringData: map[string]string{
					"token": "secret2",
				},
			}
			Expect(k8sClient.Create(ctx, gitSecret2)).Should(Succeed())

			By("creating MCPRegistry with multiple Git sources with different auth")
			multiGitConfigYAML := buildConfigYAMLForMultipleSources([]map[string]string{
				{
					"name":             "private-repo-1",
					"sourceType":       "git",
					"repository":       "https://github.com/org/repo1.git",
					"branch":           "main",
					"path":             "registry.json",
					"authUsername":     "user1",
					"authPasswordFile": "/secrets/git-auth-1/password",
					"interval":         "30m",
				},
				{
					"name":             "private-repo-2",
					"sourceType":       "git",
					"repository":       "https://github.com/org/repo2.git",
					"branch":           "develop",
					"path":             "servers.json",
					"authUsername":     "user2",
					"authPasswordFile": "/secrets/git-auth-2/token",
					"interval":         "1h",
				},
			})

			// Build volumes and mounts for both auth secrets
			volumes := []apiextensionsv1.JSON{
				{Raw: mustMarshalJSON(corev1.Volume{
					Name: "git-auth-git-auth-1",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "git-auth-1",
							Items:      []corev1.KeyToPath{{Key: "password", Path: "password"}},
						},
					},
				})},
				{Raw: mustMarshalJSON(corev1.Volume{
					Name: "git-auth-git-auth-2",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "git-auth-2",
							Items:      []corev1.KeyToPath{{Key: "token", Path: "token"}},
						},
					},
				})},
			}

			volumeMounts := []apiextensionsv1.JSON{
				{Raw: mustMarshalJSON(corev1.VolumeMount{
					Name: "git-auth-git-auth-1", MountPath: "/secrets/git-auth-1", ReadOnly: true,
				})},
				{Raw: mustMarshalJSON(corev1.VolumeMount{
					Name: "git-auth-git-auth-2", MountPath: "/secrets/git-auth-2", ReadOnly: true,
				})},
			}

			registry := &mcpv1beta1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-git-auth-test",
					Namespace: testNamespace,
				},
				Spec: mcpv1beta1.MCPRegistrySpec{
					ConfigYAML:   multiGitConfigYAML,
					Volumes:      volumes,
					VolumeMounts: volumeMounts,
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

			By("verifying both git auth volumes are mounted")
			verifyGitAuthVolume(deployment, "git-auth-1", "password")
			verifyGitAuthVolume(deployment, "git-auth-2", "token")

			By("verifying registry server config contains both auth settings")
			configMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)
			serverConfig := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      configMapName,
					Namespace: testNamespace,
				}, serverConfig)
			}, QuickTimeout, DefaultPollingInterval).Should(Succeed())

			serverConfigYAML := serverConfig.Data["config.yaml"]

			// Verify first registry auth
			Expect(serverConfigYAML).To(ContainSubstring("name: private-repo-1"))
			Expect(serverConfigYAML).To(ContainSubstring("username: user1"))
			Expect(serverConfigYAML).To(ContainSubstring("passwordFile: /secrets/git-auth-1/password"))

			// Verify second registry auth
			Expect(serverConfigYAML).To(ContainSubstring("name: private-repo-2"))
			Expect(serverConfigYAML).To(ContainSubstring("username: user2"))
			Expect(serverConfigYAML).To(ContainSubstring("passwordFile: /secrets/git-auth-2/token"))

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, gitSecret1)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, gitSecret2)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("multi-git-auth-test")
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Describe("PodTemplateSpec Customization", func() {
		It("should apply custom service account from PodTemplateSpec", func() {
			By("creating a ConfigMap source")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("podspec-sa-test")

			By("creating MCPRegistry with custom service account in PodTemplateSpec")
			registryObj := registryHelper.NewRegistryBuilder("podspec-sa-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Build()
			registryObj.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"serviceAccountName":"custom-integration-test-sa"}}`),
			}

			Expect(k8sClient.Create(ctx, registryObj)).Should(Succeed())

			By("waiting for deployment to be created")
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      fmt.Sprintf("%s-api", registryObj.Name),
					Namespace: testNamespace,
				}, deployment)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying deployment uses custom service account")
			Expect(deployment.Spec.Template.Spec.ServiceAccountName).To(Equal("custom-integration-test-sa"),
				"Deployment should use the custom service account from PodTemplateSpec")

			By("verifying PodTemplateValid condition is set to True")
			testHelpers.verifyPodTemplateValidCondition("podspec-sa-test", true)

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registryObj)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, configMap)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("podspec-sa-test")
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("should merge user tolerations from PodTemplateSpec", func() {
			By("creating a ConfigMap source")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("podspec-tolerations-test")

			By("creating MCPRegistry with custom tolerations in PodTemplateSpec")
			registryObj := registryHelper.NewRegistryBuilder("podspec-tolerations-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Build()
			registryObj.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"tolerations":[{"key":"special-node","operator":"Equal","value":"true","effect":"NoSchedule"}]}}`),
			}

			Expect(k8sClient.Create(ctx, registryObj)).Should(Succeed())

			By("waiting for deployment to be created")
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      fmt.Sprintf("%s-api", registryObj.Name),
					Namespace: testNamespace,
				}, deployment)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying deployment has custom tolerations")
			Expect(deployment.Spec.Template.Spec.Tolerations).NotTo(BeEmpty(),
				"Deployment should have tolerations from PodTemplateSpec")
			Expect(deployment.Spec.Template.Spec.Tolerations).To(HaveLen(1))

			toleration := deployment.Spec.Template.Spec.Tolerations[0]
			Expect(toleration.Key).To(Equal("special-node"))
			Expect(toleration.Operator).To(Equal(corev1.TolerationOpEqual))
			Expect(toleration.Value).To(Equal("true"))
			Expect(toleration.Effect).To(Equal(corev1.TaintEffectNoSchedule))

			By("verifying PodTemplateValid condition is set to True")
			testHelpers.verifyPodTemplateValidCondition("podspec-tolerations-test", true)

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registryObj)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, configMap)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("podspec-tolerations-test")
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("should fail with invalid PodTemplateSpec and not create deployment", func() {
			By("creating a ConfigMap source")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("podspec-invalid-test")

			By("creating MCPRegistry with invalid JSON in PodTemplateSpec")
			registryObj := registryHelper.NewRegistryBuilder("podspec-invalid-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Build()
			registryObj.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec": "invalid"}`),
			}

			Expect(k8sClient.Create(ctx, registryObj)).Should(Succeed())

			By("waiting for registry status to be updated with failure")
			testHelpers.verifyRegistryFailedWithInvalidPodTemplate("podspec-invalid-test")

			By("verifying PodTemplateValid condition is set to False")
			testHelpers.verifyPodTemplateValidCondition("podspec-invalid-test", false)

			By("verifying deployment was NOT created")
			deployment := &appsv1.Deployment{}
			Consistently(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      fmt.Sprintf("%s-api", registryObj.Name),
					Namespace: testNamespace,
				}, deployment)
				return errors.IsNotFound(err)
			}, QuickTimeout, DefaultPollingInterval).Should(BeTrue(), "Deployment should NOT be created when PodTemplateSpec is invalid")

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registryObj)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, configMap)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("podspec-invalid-test")
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

func (*serverConfigTestHelpers) verifyContainerArguments(deployment *appsv1.Deployment) {
	container := deployment.Spec.Template.Spec.Containers[0]
	Expect(container.Args).To(ContainElement("serve"))

	// Should have --config argument pointing to the server config file
	expectedConfigArg := fmt.Sprintf("--config=%s", filepath.Join(config.RegistryServerConfigFilePath, config.RegistryServerConfigFileName))
	Expect(container.Args).To(ContainElement(expectedConfigArg), "Container should have --config argument pointing to server config file")
}

// verifyConfigMapOwnership verifies the ConfigMap is owned by the MCPRegistry
func (*serverConfigTestHelpers) verifyConfigMapOwnership(configMap *corev1.ConfigMap, registry *mcpv1beta1.MCPRegistry) {
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
// by checking the user-provided Volumes/VolumeMounts on the registry spec.
func (*serverConfigTestHelpers) verifySourceDataVolume(deployment *appsv1.Deployment, registry *mcpv1beta1.MCPRegistry) {
	// Parse volumes from the registry spec to understand expected volume configuration
	userVolumes, err := registry.Spec.ParseVolumes()
	Expect(err).NotTo(HaveOccurred())

	for _, userVol := range userVolumes {
		if !strings.HasPrefix(userVol.Name, "registry-data-source-") {
			continue
		}

		// Check that the volume exists in the deployment
		sourceDataVolumeFound := false
		for _, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.Name == userVol.Name && volume.ConfigMap != nil {
				Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(userVol.ConfigMap.Name))
				sourceDataVolumeFound = true
				break
			}
		}
		Expect(sourceDataVolumeFound).To(BeTrue(),
			fmt.Sprintf("Deployment should have volume %s", userVol.Name))
	}

	// Also check that user-provided mounts exist
	userMounts, err := registry.Spec.ParseVolumeMounts()
	Expect(err).NotTo(HaveOccurred())

	for _, userMount := range userMounts {
		if !strings.HasPrefix(userMount.Name, "registry-data-source-") {
			continue
		}

		sourceDataMountFound := false
		for _, mount := range deployment.Spec.Template.Spec.Containers[0].VolumeMounts {
			if mount.Name == userMount.Name {
				Expect(mount.MountPath).To(Equal(userMount.MountPath))
				Expect(mount.ReadOnly).To(BeTrue())
				sourceDataMountFound = true
				break
			}
		}
		Expect(sourceDataMountFound).To(BeTrue(),
			fmt.Sprintf("Deployment should have volume mount %s", userMount.Name))
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
func (*serverConfigTestHelpers) verifyConfigMapContent(configYAML string, _ string, expectedContent map[string]string) {
	// In the new model, the server config ConfigMap contains the verbatim configYAML.
	// Verify expected key-value pairs are present in the content.
	for key, value := range expectedContent {
		Expect(configYAML).To(ContainSubstring(fmt.Sprintf("%s: %s", key, value)))
	}
}

// verifyPodTemplateValidCondition waits for and verifies the PodTemplateValid condition is set correctly
func (h *serverConfigTestHelpers) verifyPodTemplateValidCondition(registryName string, expectedValid bool) {
	Eventually(func() bool {
		updatedRegistry, err := h.registryHelper.GetRegistry(registryName)
		if err != nil {
			return false
		}
		condition := meta.FindStatusCondition(updatedRegistry.Status.Conditions, mcpv1beta1.ConditionPodTemplateValid)
		if condition == nil {
			return false
		}

		if expectedValid {
			return condition.Status == metav1.ConditionTrue &&
				condition.Reason == mcpv1beta1.ConditionReasonPodTemplateValid
		}
		return condition.Status == metav1.ConditionFalse &&
			condition.Reason == mcpv1beta1.ConditionReasonPodTemplateInvalid
	}, MediumTimeout, DefaultPollingInterval).Should(BeTrue(),
		fmt.Sprintf("PodTemplateValid condition should be %v", expectedValid))
}

// verifyRegistryFailedWithInvalidPodTemplate waits for and verifies the registry is in Failed phase with "Invalid PodTemplateSpec" in the message
func (h *serverConfigTestHelpers) verifyRegistryFailedWithInvalidPodTemplate(registryName string) {
	Eventually(func() bool {
		updatedRegistry, err := h.registryHelper.GetRegistry(registryName)
		if err != nil {
			return false
		}
		return updatedRegistry.Status.Phase == mcpv1beta1.MCPRegistryPhaseFailed &&
			strings.Contains(updatedRegistry.Status.Message, "Invalid PodTemplateSpec")
	}, MediumTimeout, DefaultPollingInterval).Should(BeTrue(),
		"MCPRegistry should be in Failed phase with Invalid PodTemplateSpec message")
}

// verifyGitAuthVolume verifies the deployment has the git auth secret volume and mount
func verifyGitAuthVolume(deployment *appsv1.Deployment, secretName, secretKey string) {
	expectedVolumeName := fmt.Sprintf("git-auth-%s", secretName)
	expectedMountPath := fmt.Sprintf("/secrets/%s", secretName)

	// Check volume exists
	volumeFound := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Name == expectedVolumeName && volume.Secret != nil {
			Expect(volume.Secret.SecretName).To(Equal(secretName),
				"Git auth volume should reference the correct secret")
			Expect(volume.Secret.Items).To(HaveLen(1),
				"Git auth volume should have one item")
			Expect(volume.Secret.Items[0].Key).To(Equal(secretKey),
				"Git auth volume should use the correct secret key")
			Expect(volume.Secret.Items[0].Path).To(Equal(secretKey),
				"Git auth volume should map to the correct path")
			volumeFound = true
			break
		}
	}
	Expect(volumeFound).To(BeTrue(),
		fmt.Sprintf("Deployment should have a git auth volume named %s", expectedVolumeName))

	// Check mount exists
	container := deployment.Spec.Template.Spec.Containers[0]
	mountFound := false
	for _, mount := range container.VolumeMounts {
		if mount.Name == expectedVolumeName {
			Expect(mount.MountPath).To(Equal(expectedMountPath),
				"Git auth mount should be at the expected path")
			Expect(mount.ReadOnly).To(BeTrue(),
				"Git auth mount should be read-only")
			mountFound = true
			break
		}
	}
	Expect(mountFound).To(BeTrue(),
		fmt.Sprintf("Deployment container should have a mount for %s", expectedVolumeName))
}
