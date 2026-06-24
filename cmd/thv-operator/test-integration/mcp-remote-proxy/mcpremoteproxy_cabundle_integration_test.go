// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

// newMCPOIDCConfigWithCABundle builds an inline MCPOIDCConfig whose inline config
// references a CA bundle ConfigMap by name and key.
func newMCPOIDCConfigWithCABundle(name, namespace, cmName, key string) *mcpv1beta1.MCPOIDCConfig {
	return &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer: "http://localhost:9090",
				CABundleRef: &mcpv1beta1.CABundleSource{
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
						Key:                  key,
					},
				},
			},
		},
	}
}

func caBundleConfigMap(name, namespace, key string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: map[string]string{
			key: "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		},
	}
}

// waitForOIDCConfigReady waits until the referenced MCPOIDCConfig has been validated
// (ConfigHash populated), so the proxy's OIDCConfigRef handling does not fail-closed.
func waitForOIDCConfigReady(ctx context.Context, name, namespace string) {
	Eventually(func() bool {
		cfg := &mcpv1beta1.MCPOIDCConfig{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cfg); err != nil {
			return false
		}
		return cfg.Status.ConfigHash != ""
	}, MediumTimeout, DefaultPollingInterval).Should(BeTrue())
}

var _ = Describe("MCPRemoteProxy OIDC CA bundle", Label("k8s", "remoteproxy", "cabundle"), func() {
	var (
		testCtx       context.Context
		proxyHelper   *MCPRemoteProxyTestHelper
		statusHelper  *RemoteProxyStatusTestHelper
		testNamespace string
	)

	BeforeEach(func() {
		testCtx = context.Background()
		testNamespace = createTestNamespace(testCtx)
		proxyHelper = NewMCPRemoteProxyTestHelper(testCtx, k8sClient, testNamespace)
		statusHelper = NewRemoteProxyStatusTestHelper(proxyHelper)
	})

	AfterEach(func() {
		Expect(proxyHelper.CleanupRemoteProxies()).To(Succeed())
		deleteTestNamespace(testCtx, testNamespace)
	})

	Context("when the referenced MCPOIDCConfig declares a valid CA bundle", func() {
		It("validates the CABundleRef and mounts the ConfigMap into the Deployment", func() {
			const cmName = "ca-bundle"
			const oidcName = "oidc-ca"

			By("creating the CA bundle ConfigMap")
			Expect(k8sClient.Create(testCtx, caBundleConfigMap(cmName, testNamespace, "ca.crt"))).To(Succeed())

			By("creating the MCPOIDCConfig referencing the CA bundle")
			oidcConfig := newMCPOIDCConfigWithCABundle(oidcName, testNamespace, cmName, "ca.crt")
			Expect(k8sClient.Create(testCtx, oidcConfig)).To(Succeed())
			waitForOIDCConfigReady(testCtx, oidcName, testNamespace)

			By("creating the MCPRemoteProxy referencing the MCPOIDCConfig")
			proxy := proxyHelper.NewRemoteProxyBuilder("proxy-ca-valid").
				WithOIDCConfigRef(oidcName, "https://resource.example.com").
				Create(proxyHelper)

			By("waiting for CABundleRefValidated to be True")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
				metav1.ConditionTrue,
				MediumTimeout,
			)
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
				mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefValid,
				MediumTimeout,
			)

			By("verifying the Deployment mounts the CA bundle ConfigMap")
			deployment := proxyHelper.WaitForDeployment(proxy.Name, MediumTimeout)
			expectedVolume := validation.OIDCCABundleVolumePrefix + cmName
			expectedMountPath := validation.OIDCCABundleMountBasePath + "/" + cmName

			volume := findVolume(deployment, expectedVolume)
			Expect(volume).NotTo(BeNil(), "expected CA bundle volume %q", expectedVolume)
			Expect(volume.ConfigMap).NotTo(BeNil())
			Expect(volume.ConfigMap.Name).To(Equal(cmName))

			mount := findVolumeMount(deployment, expectedVolume)
			Expect(mount).NotTo(BeNil(), "expected CA bundle volume mount %q", expectedVolume)
			Expect(mount.MountPath).To(Equal(expectedMountPath))
			Expect(mount.ReadOnly).To(BeTrue())
		})
	})

	Context("when the referenced CA bundle ConfigMap does not exist", func() {
		It("sets CABundleRefValidated to False with the NotFound reason", func() {
			const oidcName = "oidc-ca-missing"

			By("creating the MCPOIDCConfig referencing a non-existent ConfigMap")
			oidcConfig := newMCPOIDCConfigWithCABundle(oidcName, testNamespace, "missing-cm", "ca.crt")
			Expect(k8sClient.Create(testCtx, oidcConfig)).To(Succeed())
			waitForOIDCConfigReady(testCtx, oidcName, testNamespace)

			By("creating the MCPRemoteProxy referencing the MCPOIDCConfig")
			proxy := proxyHelper.NewRemoteProxyBuilder("proxy-ca-missing").
				WithOIDCConfigRef(oidcName, "https://resource.example.com").
				Create(proxyHelper)

			By("waiting for CABundleRefValidated to be False with NotFound reason")
			statusHelper.WaitForCondition(
				proxy.Name,
				mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
				metav1.ConditionFalse,
				MediumTimeout,
			)
			statusHelper.WaitForConditionReason(
				proxy.Name,
				mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
				mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefNotFound,
				MediumTimeout,
			)
		})
	})
})

func findVolume(dep *appsv1.Deployment, name string) *corev1.Volume {
	for i := range dep.Spec.Template.Spec.Volumes {
		if dep.Spec.Template.Spec.Volumes[i].Name == name {
			return &dep.Spec.Template.Spec.Volumes[i]
		}
	}
	return nil
}

func findVolumeMount(dep *appsv1.Deployment, name string) *corev1.VolumeMount {
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return nil
	}
	mounts := dep.Spec.Template.Spec.Containers[0].VolumeMounts
	for i := range mounts {
		if mounts[i].Name == name {
			return &mounts[i]
		}
	}
	return nil
}
