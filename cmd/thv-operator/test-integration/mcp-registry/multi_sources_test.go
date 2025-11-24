package operator_test

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPRegistry Multi-Source Support", func() {
	var (
		ctx              context.Context
		testNamespace    string
		registryHelper   *MCPRegistryTestHelper
		statusHelper     *StatusTestHelper
		configMapHelper  *ConfigMapTestHelper
		timingHelper     *TimingTestHelper
	)

	BeforeEach(func() {
		ctx = context.Background()
		// Create a unique namespace for this test
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-multi-source-",
				Labels: map[string]string{
					"test.toolhive.io/suite": "operator-e2e",
				},
			},
		}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		testNamespace = namespace.Name

		registryHelper = NewMCPRegistryTestHelper(ctx, k8sClient, testNamespace)
		statusHelper = NewStatusTestHelper(ctx, k8sClient, testNamespace)
		configMapHelper = NewConfigMapTestHelper(ctx, k8sClient, testNamespace)
		timingHelper = NewTimingTestHelper(ctx, k8sClient)
	})

	AfterEach(func() {
		// Clean up test resources
		Expect(registryHelper.CleanupRegistries()).To(Succeed())
		Expect(configMapHelper.CleanupConfigMaps()).To(Succeed())

		// Delete namespace
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace,
			},
		}
		_ = k8sClient.Delete(ctx, namespace)
	})

	Describe("Multiple ConfigMap Sources", func() {
		It("should support registry with two ConfigMap sources", func() {
			By("creating two ConfigMap sources with different registry data")
			// First ConfigMap with some servers
			configMap1 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "registry-source-1",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"registry.json": `{
						"version": "1.0",
						"servers": [
							{
								"name": "server1",
								"description": "Server from source 1",
								"image": "example.com/server1:latest"
							}
						]
					}`,
				},
			}
			Expect(k8sClient.Create(ctx, configMap1)).Should(Succeed())

			// Second ConfigMap with different servers
			configMap2 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "registry-source-2",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"registry.json": `{
						"version": "1.0",
						"servers": [
							{
								"name": "server2",
								"description": "Server from source 2",
								"image": "example.com/server2:latest"
							}
						]
					}`,
				},
			}
			Expect(k8sClient.Create(ctx, configMap2)).Should(Succeed())

			By("creating MCPRegistry with two ConfigMap sources")
			registry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-configmap-registry",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
						{
							Name: "primary",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMap1.Name,
									},
									Key: "registry.json",
								},
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "5m",
							},
							Filter: &mcpv1alpha1.RegistryFilter{
								NameFilters: &mcpv1alpha1.NameFilter{
									Include: []string{"server1*"},
								},
							},
						},
						{
							Name: "secondary",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMap2.Name,
									},
									Key: "registry.json",
								},
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "10m",
							},
							Filter: &mcpv1alpha1.RegistryFilter{
								NameFilters: &mcpv1alpha1.NameFilter{
									Include: []string{"server2*"},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, registry)).Should(Succeed())

			By("waiting for registry to become ready")
			statusHelper.WaitForPhase("multi-configmap-registry", mcpv1alpha1.MCPRegistryPhasePending, MediumTimeout)

			By("verifying deployment has volumes for both ConfigMap sources")
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      fmt.Sprintf("%s-api", registry.Name),
					Namespace: testNamespace,
				}, deployment)
			}, QuickTimeout, DefaultPollingInterval).Should(Succeed())

			// Verify volumes for both sources
			Expect(deployment.Spec.Template.Spec.Volumes).To(HaveLen(4)) // 2 source volumes + config + storage

			// Check first source volume
			primaryVolumeFound := false
			secondaryVolumeFound := false
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == "registry-source-0-primary" {
					Expect(volume.ConfigMap).NotTo(BeNil())
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(configMap1.Name))
					primaryVolumeFound = true
				}
				if volume.Name == "registry-source-1-secondary" {
					Expect(volume.ConfigMap).NotTo(BeNil())
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(configMap2.Name))
					secondaryVolumeFound = true
				}
			}
			Expect(primaryVolumeFound).To(BeTrue(), "Primary source volume not found")
			Expect(secondaryVolumeFound).To(BeTrue(), "Secondary source volume not found")

			By("verifying container has volume mounts for both sources")
			container := deployment.Spec.Template.Spec.Containers[0]
			primaryMountFound := false
			secondaryMountFound := false
			for _, mount := range container.VolumeMounts {
				if mount.Name == "registry-source-0-primary" {
					Expect(mount.MountPath).To(Equal("/config/registry/source-0"))
					Expect(mount.ReadOnly).To(BeTrue())
					primaryMountFound = true
				}
				if mount.Name == "registry-source-1-secondary" {
					Expect(mount.MountPath).To(Equal("/config/registry/source-1"))
					Expect(mount.ReadOnly).To(BeTrue())
					secondaryMountFound = true
				}
			}
			Expect(primaryMountFound).To(BeTrue(), "Primary source mount not found")
			Expect(secondaryMountFound).To(BeTrue(), "Secondary source mount not found")

			By("verifying registry server config contains both sources")
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

			// Verify both sources are in the config
			Expect(configYAML).To(ContainSubstring("sources:"))
			Expect(configYAML).To(ContainSubstring("name: primary"))
			Expect(configYAML).To(ContainSubstring("name: secondary"))
			Expect(configYAML).To(ContainSubstring("path: /config/registry/source-0/registry.json"))
			Expect(configYAML).To(ContainSubstring("path: /config/registry/source-1/registry.json"))
			Expect(configYAML).To(ContainSubstring("interval: 5m"))
			Expect(configYAML).To(ContainSubstring("interval: 10m"))

			By("cleaning up the registry")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("multi-configmap-registry")
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Describe("Mixed Source Types", func() {
		It("should support registry with ConfigMap, Git, and API sources", func() {
			By("creating a ConfigMap source")
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mixed-registry-configmap",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"registry.json": `{
						"version": "1.0",
						"servers": [
							{
								"name": "configmap-server",
								"description": "Server from ConfigMap",
								"image": "example.com/configmap-server:latest"
							}
						]
					}`,
				},
			}
			Expect(k8sClient.Create(ctx, configMap)).Should(Succeed())

			By("creating MCPRegistry with mixed source types")
			registry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mixed-sources-registry",
					Namespace: testNamespace,
					Labels: map[string]string{
						"test": "mixed-sources",
					},
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
						// ConfigMap source
						{
							Name: "configmap-source",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMap.Name,
									},
									Key: "registry.json",
								},
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "5m",
							},
							Filter: &mcpv1alpha1.RegistryFilter{
								Tags: &mcpv1alpha1.TagFilter{
									Include: []string{"stable", "production"},
								},
							},
						},
						// Git source
						{
							Name: "git-source",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeGit,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								Git: &mcpv1alpha1.GitSource{
									Repository: "https://github.com/example/registry.git",
									Branch:     "main",
									Path:       "registry.json",
								},
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "30m",
							},
							Filter: &mcpv1alpha1.RegistryFilter{
								NameFilters: &mcpv1alpha1.NameFilter{
									Include: []string{"git-*"},
									Exclude: []string{"git-test-*"},
								},
							},
						},
						// API source
						{
							Name: "api-source",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeAPI,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								API: &mcpv1alpha1.APISource{
									Endpoint: "https://api.example.com/registry",
								},
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "1h",
							},
							Filter: &mcpv1alpha1.RegistryFilter{
								NameFilters: &mcpv1alpha1.NameFilter{
									Include: []string{"api-*"},
								},
								Tags: &mcpv1alpha1.TagFilter{
									Exclude: []string{"deprecated"},
								},
							},
						},
					},
					EnforceServers: false,
				},
			}
			Expect(k8sClient.Create(ctx, registry)).Should(Succeed())

			By("waiting for registry to be created")
			statusHelper.WaitForPhase("mixed-sources-registry", mcpv1alpha1.MCPRegistryPhasePending, MediumTimeout)

			By("verifying deployment exists")
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{
					Name:      fmt.Sprintf("%s-api", registry.Name),
					Namespace: testNamespace,
				}, deployment)
			}, QuickTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying only ConfigMap source has a volume")
			// Only ConfigMap sources should have volumes
			configMapVolumeFound := false
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == "registry-source-0-configmap-source" {
					Expect(volume.ConfigMap).NotTo(BeNil())
					Expect(volume.ConfigMap.LocalObjectReference.Name).To(Equal(configMap.Name))
					configMapVolumeFound = true
				}
				// Git and API sources should not have volumes
				Expect(volume.Name).NotTo(ContainSubstring("git-source"))
				Expect(volume.Name).NotTo(ContainSubstring("api-source"))
			}
			Expect(configMapVolumeFound).To(BeTrue(), "ConfigMap source volume not found")

			By("verifying only ConfigMap source has a volume mount")
			container := deployment.Spec.Template.Spec.Containers[0]
			configMapMountFound := false
			for _, mount := range container.VolumeMounts {
				if mount.Name == "registry-source-0-configmap-source" {
					Expect(mount.MountPath).To(Equal("/config/registry/source-0"))
					Expect(mount.ReadOnly).To(BeTrue())
					configMapMountFound = true
				}
			}
			Expect(configMapMountFound).To(BeTrue(), "ConfigMap source mount not found")

			By("verifying registry server config contains all three sources")
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

			// Verify all three sources are in the config
			Expect(configYAML).To(ContainSubstring("sources:"))

			// ConfigMap source verification
			Expect(configYAML).To(ContainSubstring("name: configmap-source"))
			Expect(configYAML).To(ContainSubstring("type: file")) // ConfigMap becomes file type
			Expect(configYAML).To(ContainSubstring("path: /config/registry/source-0/registry.json"))

			// Git source verification
			Expect(configYAML).To(ContainSubstring("name: git-source"))
			Expect(configYAML).To(ContainSubstring("type: git"))
			Expect(configYAML).To(ContainSubstring("repository: https://github.com/example/registry.git"))
			Expect(configYAML).To(ContainSubstring("branch: main"))

			// API source verification
			Expect(configYAML).To(ContainSubstring("name: api-source"))
			Expect(configYAML).To(ContainSubstring("type: api"))
			Expect(configYAML).To(ContainSubstring("endpoint: https://api.example.com/registry"))

			// Verify different sync policies
			Expect(configYAML).To(ContainSubstring("interval: 5m"))
			Expect(configYAML).To(ContainSubstring("interval: 30m"))
			Expect(configYAML).To(ContainSubstring("interval: 1h"))

			// Verify filters are included
			Expect(configYAML).To(ContainSubstring("include:"))
			Expect(configYAML).To(ContainSubstring("exclude:"))
			Expect(configYAML).To(ContainSubstring("- stable"))
			Expect(configYAML).To(ContainSubstring("- production"))
			Expect(configYAML).To(ContainSubstring("- deprecated"))

			By("verifying registry status")
			updatedRegistry := &mcpv1alpha1.MCPRegistry{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      registry.Name,
					Namespace: testNamespace,
				}, updatedRegistry)
				if err != nil {
					return false
				}
				// Check that status has been updated
				return updatedRegistry.Status.Phase != ""
			}, QuickTimeout, DefaultPollingInterval).Should(BeTrue())

			By("cleaning up the registry")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("mixed-sources-registry")
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Describe("Source Updates and Modifications", func() {
		It("should handle adding and removing sources dynamically", func() {
			By("creating initial ConfigMap")
			configMap1 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dynamic-source-1",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"registry.json": `{"version": "1.0", "servers": []}`,
				},
			}
			Expect(k8sClient.Create(ctx, configMap1)).Should(Succeed())

			By("creating registry with single source")
			registry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dynamic-sources-registry",
					Namespace: testNamespace,
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
						{
							Name: "initial",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMap1.Name,
									},
									Key: "registry.json",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, registry)).Should(Succeed())

			By("waiting for initial deployment")
			statusHelper.WaitForPhase("dynamic-sources-registry", mcpv1alpha1.MCPRegistryPhasePending, MediumTimeout)

			By("creating second ConfigMap")
			configMap2 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dynamic-source-2",
					Namespace: testNamespace,
				},
				Data: map[string]string{
					"registry.json": `{"version": "1.0", "servers": []}`,
				},
			}
			Expect(k8sClient.Create(ctx, configMap2)).Should(Succeed())

			By("updating registry to add a second source")
			Eventually(func() error {
				updatedRegistry := &mcpv1alpha1.MCPRegistry{}
				if err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      registry.Name,
					Namespace: testNamespace,
				}, updatedRegistry); err != nil {
					return err
				}

				// Add a second source
				updatedRegistry.Spec.Sources = append(updatedRegistry.Spec.Sources, mcpv1alpha1.MCPRegistrySourceConfig{
					Name: "additional",
					MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: configMap2.Name,
							},
							Key: "registry.json",
						},
					},
					SyncPolicy: &mcpv1alpha1.SyncPolicy{
						Interval: "15m",
					},
				})

				return k8sClient.Update(ctx, updatedRegistry)
			}, QuickTimeout, DefaultPollingInterval).Should(Succeed())

			By("verifying deployment is updated with new source")
			deployment := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      fmt.Sprintf("%s-api", registry.Name),
					Namespace: testNamespace,
				}, deployment)
				if err != nil {
					return false
				}

				// Check for both source volumes
				source1Found := false
				source2Found := false
				for _, volume := range deployment.Spec.Template.Spec.Volumes {
					if volume.Name == "registry-source-0-initial" {
						source1Found = true
					}
					if volume.Name == "registry-source-1-additional" {
						source2Found = true
					}
				}
				return source1Found && source2Found
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())

			By("verifying updated config contains both sources")
			configMapName := fmt.Sprintf("%s-registry-server-config", registry.Name)
			serverConfig := &corev1.ConfigMap{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{
					Name:      configMapName,
					Namespace: testNamespace,
				}, serverConfig)
				if err != nil {
					return false
				}

				configYAML := serverConfig.Data["config.yaml"]
				return configYAML != "" &&
					strings.Contains(configYAML, "name: initial") &&
					strings.Contains(configYAML, "name: additional")
			}, QuickTimeout, DefaultPollingInterval).Should(BeTrue())

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry("dynamic-sources-registry")
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})
})

// These helper functions are now redundant as we use strings.Contains directly