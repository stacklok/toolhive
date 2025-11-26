package operator_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPRegistry Multi-ConfigMap Volume Mounts", func() {
	var (
		ctx              context.Context
		testNamespace    string
		registryHelper   *MCPRegistryTestHelper
		configMapHelper  *ConfigMapTestHelper
		timingHelper     *TimingTestHelper
	)

	BeforeEach(func() {
		ctx = context.Background()
		// Create a unique namespace for this test
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-multi-configmap-",
				Labels: map[string]string{
					"test.toolhive.io/suite": "operator-e2e",
				},
			},
		}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		testNamespace = namespace.Name

		registryHelper = NewMCPRegistryTestHelper(ctx, k8sClient, testNamespace)
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
		Expect(mounts["registry-data-0-source-alpha"]).To(Equal("/config/registry/source-alpha"))
		Expect(mounts["registry-data-1-source-beta"]).To(Equal("/config/registry/source-beta"))
		Expect(mounts["registry-data-2-source-gamma"]).To(Equal("/config/registry/source-gamma"))

		// Verify all mounts are read-only
		for _, mount := range container.VolumeMounts {
			if mount.Name == "registry-data-0-source-alpha" ||
				mount.Name == "registry-data-1-source-beta" ||
				mount.Name == "registry-data-2-source-gamma" {
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
		timingHelper.WaitForControllerReconciliation(func() interface{} {
			_, err := registryHelper.GetRegistry("multi-cm-volumes-test")
			return errors.IsNotFound(err)
		}).Should(BeTrue())
	})
})