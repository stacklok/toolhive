// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi"
)

var _ = Describe("MCPRegistry Deployment Updates", Label("k8s", "registry", "deployment-update"), func() {
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

		registryHelper = NewMCPRegistryTestHelper(ctx, k8sClient, testNamespace)
		configMapHelper = NewConfigMapTestHelper(ctx, k8sClient, testNamespace)
		statusHelper = NewStatusTestHelper(ctx, k8sClient, testNamespace)
		timingHelper = NewTimingTestHelper(ctx, k8sClient)
		k8sHelper = NewK8sResourceTestHelper(ctx, k8sClient, testNamespace)
	})

	AfterEach(func() {
		Expect(registryHelper.CleanupRegistries()).To(Succeed())
		Expect(configMapHelper.CleanupConfigMaps()).To(Succeed())
		deleteTestNamespace(ctx, testNamespace)
	})

	// waitForDeployment waits for the registry API deployment to exist and returns it
	waitForDeployment := func(registryName string) *appsv1.Deployment {
		deploymentName := fmt.Sprintf("%s-api", registryName)
		deployment := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{
				Name:      deploymentName,
				Namespace: testNamespace,
			}, deployment)
		}, MediumTimeout, DefaultPollingInterval).Should(Succeed(),
			"Deployment %s should be created", deploymentName)
		return deployment
	}

	Context("PodTemplateSpec updates to existing deployments", func() {
		It("should apply imagePullSecrets when PodTemplateSpec is added after initial creation", func() {
			By("creating a registry without PodTemplateSpec")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("update-ips-config")
			registry := registryHelper.NewRegistryBuilder("update-ips-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			By("waiting for deployment to be created")
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)
			deployment := waitForDeployment(registry.Name)

			By("verifying deployment has no imagePullSecrets initially")
			Expect(deployment.Spec.Template.Spec.ImagePullSecrets).To(BeEmpty())

			By("updating the MCPRegistry to add PodTemplateSpec with imagePullSecrets")
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			updatedRegistry.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"imagePullSecrets":[{"name":"registry-creds"}]}}`),
			}
			Expect(registryHelper.UpdateRegistry(updatedRegistry)).To(Succeed())

			By("waiting for deployment to be updated with imagePullSecrets")
			Eventually(func() []corev1.LocalObjectReference {
				d, err := k8sHelper.GetDeployment(fmt.Sprintf("%s-api", registry.Name))
				if err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "registry-creds"}),
				"Deployment should have imagePullSecrets after PodTemplateSpec update",
			)

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registry.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("should apply container env vars when PodTemplateSpec is added", func() {
			By("creating a registry without PodTemplateSpec")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("update-env-config")
			registry := registryHelper.NewRegistryBuilder("update-env-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			By("waiting for deployment to be created")
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)
			_ = waitForDeployment(registry.Name)

			By("updating the MCPRegistry to add container env via PodTemplateSpec")
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())

			ptsJSON, err := json.Marshal(corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "registry-api",
							Env: []corev1.EnvVar{
								{Name: "CUSTOM_VAR", Value: "custom-value"},
							},
						},
					},
				},
			})
			Expect(err).NotTo(HaveOccurred())
			updatedRegistry.Spec.PodTemplateSpec = &runtime.RawExtension{Raw: ptsJSON}
			Expect(registryHelper.UpdateRegistry(updatedRegistry)).To(Succeed())

			By("waiting for deployment to be updated with env var")
			Eventually(func() bool {
				d, err := k8sHelper.GetDeployment(fmt.Sprintf("%s-api", registry.Name))
				if err != nil || len(d.Spec.Template.Spec.Containers) == 0 {
					return false
				}
				for _, env := range d.Spec.Template.Spec.Containers[0].Env {
					if env.Name == "CUSTOM_VAR" && env.Value == "custom-value" {
						return true
					}
				}
				return false
			}, MediumTimeout, DefaultPollingInterval).Should(BeTrue(),
				"Deployment container should have CUSTOM_VAR env after update")

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registry.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("should update deployment when PodTemplateSpec imagePullSecrets changes", func() {
			By("creating a registry with initial imagePullSecrets")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("update-change-ips-config")
			registryObj := registryHelper.NewRegistryBuilder("update-change-ips-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Build()
			registryObj.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"imagePullSecrets":[{"name":"creds-a"}]}}`),
			}
			registry := registryObj
			Expect(k8sClient.Create(ctx, registry)).Should(Succeed())

			By("waiting for deployment with initial imagePullSecrets")
			Eventually(func() []corev1.LocalObjectReference {
				d, err := k8sHelper.GetDeployment("update-change-ips-test-api")
				if err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "creds-a"}),
			)

			By("changing the imagePullSecrets to a different secret")
			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			updatedRegistry.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"imagePullSecrets":[{"name":"creds-b"}]}}`),
			}
			Expect(registryHelper.UpdateRegistry(updatedRegistry)).To(Succeed())

			By("waiting for deployment to be updated with new imagePullSecrets")
			Eventually(func() []corev1.LocalObjectReference {
				d, err := k8sHelper.GetDeployment("update-change-ips-test-api")
				if err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "creds-b"}),
				"Deployment should have updated imagePullSecrets",
			)

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registry.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Context("spec.imagePullSecrets is the SA-aware path for image pull credentials", func() {
		It("sets imagePullSecrets on the Deployment when only spec.imagePullSecrets is provided", func() {
			By("creating a registry with only spec.imagePullSecrets")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("explicit-ips-deploy-config")
			registryObj := registryHelper.NewRegistryBuilder("explicit-ips-deploy-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Build()
			registryObj.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "explicit-creds"}}
			Expect(k8sClient.Create(ctx, registryObj)).Should(Succeed())

			By("waiting for deployment to be created")
			registryHelper.WaitForRegistryInitialization(registryObj.Name, timingHelper, statusHelper)
			deployment := waitForDeployment(registryObj.Name)

			By("verifying Deployment pod spec carries the explicit imagePullSecrets")
			Expect(deployment.Spec.Template.Spec.ImagePullSecrets).To(ContainElement(
				corev1.LocalObjectReference{Name: "explicit-creds"},
			))

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registryObj)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registryObj.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("sets imagePullSecrets on the ServiceAccount when only spec.imagePullSecrets is provided", func() {
			By("creating a registry with only spec.imagePullSecrets")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("explicit-ips-sa-config")
			registryObj := registryHelper.NewRegistryBuilder("explicit-ips-sa-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Build()
			registryObj.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "sa-creds"}}
			Expect(k8sClient.Create(ctx, registryObj)).Should(Succeed())

			By("waiting for the registry to start reconciling")
			registryHelper.WaitForRegistryInitialization(registryObj.Name, timingHelper, statusHelper)

			By("verifying the operator-managed ServiceAccount has the imagePullSecrets")
			saName := registryapi.GetServiceAccountName(registryObj)
			Eventually(func() []corev1.LocalObjectReference {
				sa := &corev1.ServiceAccount{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      saName,
					Namespace: testNamespace,
				}, sa); err != nil {
					return nil
				}
				return sa.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "sa-creds"}),
				"ServiceAccount should carry imagePullSecrets from spec.imagePullSecrets",
			)

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registryObj)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registryObj.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("propagates updates to spec.imagePullSecrets to both Deployment and ServiceAccount", func() {
			By("creating a registry with an initial spec.imagePullSecrets value")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("explicit-ips-update-config")
			registryObj := registryHelper.NewRegistryBuilder("explicit-ips-update-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Build()
			registryObj.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "creds-initial"}}
			Expect(k8sClient.Create(ctx, registryObj)).Should(Succeed())

			By("waiting for the initial Deployment with the original imagePullSecrets")
			registryHelper.WaitForRegistryInitialization(registryObj.Name, timingHelper, statusHelper)
			Eventually(func() []corev1.LocalObjectReference {
				d, err := k8sHelper.GetDeployment(fmt.Sprintf("%s-api", registryObj.Name))
				if err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "creds-initial"}),
			)

			By("waiting for the ServiceAccount to carry the original imagePullSecrets")
			saName := registryapi.GetServiceAccountName(registryObj)
			Eventually(func() []corev1.LocalObjectReference {
				sa := &corev1.ServiceAccount{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      saName,
					Namespace: testNamespace,
				}, sa); err != nil {
					return nil
				}
				return sa.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "creds-initial"}),
			)

			By("changing spec.imagePullSecrets to a different secret")
			updatedRegistry, err := registryHelper.GetRegistry(registryObj.Name)
			Expect(err).NotTo(HaveOccurred())
			updatedRegistry.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "creds-rotated"}}
			Expect(registryHelper.UpdateRegistry(updatedRegistry)).To(Succeed())

			By("waiting for Deployment pod spec to be updated to the new imagePullSecrets")
			Eventually(func() []corev1.LocalObjectReference {
				d, err := k8sHelper.GetDeployment(fmt.Sprintf("%s-api", registryObj.Name))
				if err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "creds-rotated"}),
				"Deployment should pick up the rotated imagePullSecrets",
			)

			By("waiting for ServiceAccount to be updated to the new imagePullSecrets")
			Eventually(func() []corev1.LocalObjectReference {
				sa := &corev1.ServiceAccount{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      saName,
					Namespace: testNamespace,
				}, sa); err != nil {
					return nil
				}
				return sa.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				ContainElement(corev1.LocalObjectReference{Name: "creds-rotated"}),
				"ServiceAccount should pick up the rotated imagePullSecrets",
			)

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registryObj)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registryObj.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("lets podTemplateSpec.imagePullSecrets override Deployment while SA still tracks spec.imagePullSecrets", func() {
			By("creating a registry that sets both spec.imagePullSecrets and podTemplateSpec.imagePullSecrets")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("explicit-ips-override-config")
			registryObj := registryHelper.NewRegistryBuilder("explicit-ips-override-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Build()
			registryObj.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "sa-creds"}}
			registryObj.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"imagePullSecrets":[{"name":"deployment-override"}]}}`),
			}
			Expect(k8sClient.Create(ctx, registryObj)).Should(Succeed())

			By("waiting for the Deployment to be created")
			registryHelper.WaitForRegistryInitialization(registryObj.Name, timingHelper, statusHelper)

			By("verifying the Deployment uses the PodTemplateSpec override (atomic replacement)")
			Eventually(func() []corev1.LocalObjectReference {
				d, err := k8sHelper.GetDeployment(fmt.Sprintf("%s-api", registryObj.Name))
				if err != nil {
					return nil
				}
				return d.Spec.Template.Spec.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				And(
					ContainElement(corev1.LocalObjectReference{Name: "deployment-override"}),
					Not(ContainElement(corev1.LocalObjectReference{Name: "sa-creds"})),
				),
				"Deployment should use the PodTemplateSpec override and drop the spec.imagePullSecrets default",
			)

			By("verifying the ServiceAccount still uses spec.imagePullSecrets (PodTemplateSpec does not affect the SA)")
			saName := registryapi.GetServiceAccountName(registryObj)
			Eventually(func() []corev1.LocalObjectReference {
				sa := &corev1.ServiceAccount{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      saName,
					Namespace: testNamespace,
				}, sa); err != nil {
					return nil
				}
				return sa.ImagePullSecrets
			}, MediumTimeout, DefaultPollingInterval).Should(
				And(
					ContainElement(corev1.LocalObjectReference{Name: "sa-creds"}),
					Not(ContainElement(corev1.LocalObjectReference{Name: "deployment-override"})),
				),
				"ServiceAccount should reflect spec.imagePullSecrets, not the PodTemplateSpec override",
			)

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registryObj)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registryObj.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Context("Spec changes trigger deployment updates", func() {
		It("should update deployment config-hash when registry spec changes", func() {
			By("creating a registry")
			configMap := configMapHelper.CreateSampleToolHiveRegistry("spec-change-config")
			registry := registryHelper.NewRegistryBuilder("spec-change-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				WithSyncPolicy("1h").
				Create(registryHelper)

			By("waiting for deployment to be created")
			registryHelper.WaitForRegistryInitialization(registry.Name, timingHelper, statusHelper)
			deployment := waitForDeployment(registry.Name)

			By("capturing the original config-hash")
			originalHash := deployment.Spec.Template.Annotations["toolhive.stacklok.dev/config-hash"]
			Expect(originalHash).NotTo(BeEmpty(), "config-hash should be set on initial deployment")

			By("updating the registry configYAML to include a second source")
			_ = configMapHelper.CreateSampleToolHiveRegistry("spec-change-config-2")

			updatedRegistry, err := registryHelper.GetRegistry(registry.Name)
			Expect(err).NotTo(HaveOccurred())
			// Replace the configYAML with one that has two sources
			updatedRegistry.Spec.ConfigYAML = buildConfigYAMLForMultipleSources([]map[string]string{
				{
					"name":       "default",
					"sourceType": "file",
					"filePath":   "/config/registry/default/registry.json",
					"interval":   "1h",
				},
				{
					"name":       "extra",
					"sourceType": "file",
					"filePath":   "/config/registry/extra/registry.json",
					"interval":   "30m",
				},
			})
			Expect(registryHelper.UpdateRegistry(updatedRegistry)).To(Succeed())

			By("waiting for deployment config-hash to change")
			Eventually(func() string {
				d, err := k8sHelper.GetDeployment(fmt.Sprintf("%s-api", registry.Name))
				if err != nil {
					return ""
				}
				return d.Spec.Template.Annotations["toolhive.stacklok.dev/config-hash"]
			}, MediumTimeout, DefaultPollingInterval).ShouldNot(Equal(originalHash),
				"config-hash should change after spec update")

			By("cleaning up")
			Expect(k8sClient.Delete(ctx, registry)).Should(Succeed())
			timingHelper.WaitForControllerReconciliation(func() interface{} {
				_, err := registryHelper.GetRegistry(registry.Name)
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})
})
