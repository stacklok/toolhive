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
	"sigs.k8s.io/controller-runtime/pkg/client"
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
