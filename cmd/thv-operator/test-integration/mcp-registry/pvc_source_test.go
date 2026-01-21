// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPRegistry PVC Source", Label("k8s", "registry", "pvc"), func() {
	var (
		ctx             context.Context
		registryHelper  *MCPRegistryTestHelper
		configMapHelper *ConfigMapTestHelper
		statusHelper    *StatusTestHelper
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
		k8sHelper := NewK8sResourceTestHelper(ctx, k8sClient, testNamespace)
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

	Context("PVC Source Functionality", func() {
		It("Should configure PVC volume and mount correctly", func() {
			pvcName := "test-registry-data"

			By("Creating a PVC for registry data")
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pvcName,
					Namespace: testNamespace,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("100Mi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			By("Creating MCPRegistry with PVC source")
			registry := registryHelper.NewRegistryBuilder("test-pvc-registry").
				WithRegistryName("pvc-source").
				WithPVCSource(pvcName, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			By("Waiting for registry to initialize")
			statusHelper.WaitForPhaseAny(registry.Name, []mcpv1alpha1.MCPRegistryPhase{
				mcpv1alpha1.MCPRegistryPhaseReady,
				mcpv1alpha1.MCPRegistryPhasePending,
			}, MediumTimeout)

			By("Verifying registry API deployment has PVC volume")
			deployment := testHelpers.getDeploymentForRegistry(registry.Name)

			// Verify PVC volume exists
			var pvcVolume *corev1.Volume
			for i := range deployment.Spec.Template.Spec.Volumes {
				if deployment.Spec.Template.Spec.Volumes[i].PersistentVolumeClaim != nil {
					pvcVolume = &deployment.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(pvcVolume).ToNot(BeNil(), "PVC volume should be configured")
			Expect(pvcVolume.PersistentVolumeClaim.ClaimName).To(Equal(pvcName))
			Expect(pvcVolume.PersistentVolumeClaim.ReadOnly).To(BeTrue())

			By("Verifying container has PVC volume mount")
			container := deployment.Spec.Template.Spec.Containers[0]
			var pvcMount *corev1.VolumeMount
			for i := range container.VolumeMounts {
				if container.VolumeMounts[i].Name == pvcVolume.Name {
					pvcMount = &container.VolumeMounts[i]
					break
				}
			}
			Expect(pvcMount).ToNot(BeNil(), "PVC volume mount should be configured")
			Expect(pvcMount.MountPath).To(Equal("/config/registry/pvc-source"))
			Expect(pvcMount.ReadOnly).To(BeTrue())

			By("Verifying registry server config ConfigMap is created")
			serverConfigMap := testHelpers.waitForAndGetServerConfigMap(registry.Name)

			By("Validating config includes registry name in path")
			configYAML, exists := serverConfigMap.Data["config.yaml"]
			Expect(exists).To(BeTrue())
			// Path should be /config/registry/{registryName}/{PVCRef.Path}
			expectedPath := "/config/registry/pvc-source/registry.json"
			Expect(configYAML).To(ContainSubstring(expectedPath))
		})
	})

	Context("Single PVC with Multiple Registries", func() {
		It("Should support multiple registries from a single shared PVC", func() {
			pvcName := "shared-registry-data"

			By("Creating a single PVC for multiple registry sources")
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pvcName,
					Namespace: testNamespace,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("100Mi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

			By("Creating MCPRegistry with two registries from the same PVC")
			registry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-shared-pvc-registry",
					Namespace: testNamespace,
					Labels: map[string]string{
						"test.toolhive.io/suite": "operator-e2e",
					},
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Registries: []mcpv1alpha1.MCPRegistryConfig{
						{
							Name:   "production",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							PVCRef: &mcpv1alpha1.PVCSource{
								ClaimName: pvcName,
								Path:      "production/registry.json",
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "2h",
							},
						},
						{
							Name:   "development",
							Format: mcpv1alpha1.RegistryFormatToolHive,
							PVCRef: &mcpv1alpha1.PVCSource{
								ClaimName: pvcName,
								Path:      "development/registry.json",
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "30m",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, registry)).To(Succeed())

			By("Waiting for registry to initialize")
			statusHelper.WaitForPhaseAny(registry.Name, []mcpv1alpha1.MCPRegistryPhase{
				mcpv1alpha1.MCPRegistryPhaseReady,
				mcpv1alpha1.MCPRegistryPhasePending,
			}, MediumTimeout)

			By("Verifying registry server config ConfigMap contains both registries")
			serverConfigMap := testHelpers.waitForAndGetServerConfigMap(registry.Name)

			configYAML, exists := serverConfigMap.Data["config.yaml"]
			Expect(exists).To(BeTrue())
			Expect(configYAML).To(ContainSubstring("production"))
			Expect(configYAML).To(ContainSubstring("development"))

			// Verify file paths use registry names as subdirectories to prevent conflicts
			// Pattern: /config/registry/{registryName}/{pvcRef.path}
			expectedProdPath := "/config/registry/production/production/registry.json"
			expectedDevPath := "/config/registry/development/development/registry.json"
			Expect(configYAML).To(ContainSubstring(expectedProdPath))
			Expect(configYAML).To(ContainSubstring(expectedDevPath))

			By("Verifying deployment has TWO PVC volumes (one per registry)")
			deployment := testHelpers.getDeploymentForRegistry(registry.Name)

			// Find PVC volumes - should have 2 volumes (one per registry), both pointing to same PVC
			pvcVolumes := make(map[string]string) // volume name -> PVC claim name
			for _, vol := range deployment.Spec.Template.Spec.Volumes {
				if vol.PersistentVolumeClaim != nil {
					pvcVolumes[vol.Name] = vol.PersistentVolumeClaim.ClaimName
				}
			}

			// Should have 2 PVC volumes (one per registry), both pointing to same PVC
			Expect(pvcVolumes).To(HaveLen(2), "Should have 2 PVC volumes (one per registry)")
			Expect(pvcVolumes).To(HaveKey("registry-data-source-production"))
			Expect(pvcVolumes).To(HaveKey("registry-data-source-development"))
			Expect(pvcVolumes["registry-data-source-production"]).To(Equal(pvcName))
			Expect(pvcVolumes["registry-data-source-development"]).To(Equal(pvcName))

			By("Verifying each registry has its own mount point")
			container := deployment.Spec.Template.Spec.Containers[0]
			mountPaths := make(map[string]string) // volume name -> mount path
			for _, mount := range container.VolumeMounts {
				if _, isPVC := pvcVolumes[mount.Name]; isPVC {
					mountPaths[mount.Name] = mount.MountPath
				}
			}

			// Verify each registry mounted at its own subdirectory (prevents path conflicts)
			Expect(mountPaths["registry-data-source-production"]).To(Equal("/config/registry/production"))
			Expect(mountPaths["registry-data-source-development"]).To(Equal("/config/registry/development"))
		})
	})
})
