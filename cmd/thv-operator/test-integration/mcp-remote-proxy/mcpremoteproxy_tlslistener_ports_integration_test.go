// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

var _ = Describe("MCPRemoteProxy TLS listener ports", Label("k8s", "remoteproxy", "tlslistener"), Ordered, func() {
	const (
		proxyName      = "tlslistener-ports"
		authConfigName = "tlslistener-embedded-auth"
		oidcConfigName = "tlslistener-oidc"
	)

	var (
		testCtx     context.Context
		namespace   string
		proxyHelper *MCPRemoteProxyTestHelper

		httpServicePort = corev1.ServicePort{
			Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP,
		}
		authServicePort = corev1.ServicePort{
			Name: "https-auth", Port: 8443, TargetPort: intstr.FromInt32(8443), Protocol: corev1.ProtocolTCP,
			AppProtocol: ptr.To("https"),
		}
		httpContainerPort = corev1.ContainerPort{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}
		authContainerPort = corev1.ContainerPort{Name: "https-auth", ContainerPort: 8443, Protocol: corev1.ProtocolTCP}
	)

	servicePorts := func(g Gomega) []corev1.ServicePort {
		svc := &corev1.Service{}
		g.Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: ServiceName(proxyName), Namespace: namespace}, svc)).To(Succeed())
		return svc.Spec.Ports
	}
	containerPorts := func(g Gomega) []corev1.ContainerPort {
		dep := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: proxyName, Namespace: namespace}, dep)).To(Succeed())
		g.Expect(dep.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		return dep.Spec.Template.Spec.Containers[0].Ports
	}
	// expectNoPortDrift reads both objects back from the API server and asserts
	// the drift checks accept them as stored.
	expectNoPortDrift := func(tlsListener bool) {
		svc := &corev1.Service{}
		Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: ServiceName(proxyName), Namespace: namespace}, svc)).To(Succeed())
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: proxyName, Namespace: namespace}, dep)).To(Succeed())
		Expect(dep.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		owned := ctrlutil.ProxyOwnedPortNames()
		Expect(ctrlutil.ServicePortsDrifted(svc.Spec.Ports, ctrlutil.ProxyServicePorts(8080, tlsListener), owned)).
			To(BeFalse(), "stored Service ports reported as drift")
		Expect(ctrlutil.ContainerPortsDrifted(dep.Spec.Template.Spec.Containers[0].Ports, ctrlutil.ProxyContainerPorts(8080, tlsListener), ctrlutil.ProxyContainerPorts(8080, true))).
			To(BeFalse(), "stored container ports reported as drift")
	}
	setTLSListener := func(on bool) {
		Eventually(func(g Gomega) {
			auth := &mcpv1beta1.MCPExternalAuthConfig{}
			g.Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: authConfigName, Namespace: namespace}, auth)).To(Succeed())
			if on {
				auth.Spec.EmbeddedAuthServer.TLSListener = &mcpv1beta1.TLSListenerConfig{
					CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "tlslistener-cert", Key: "tls.crt"},
					PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "tlslistener-cert", Key: "tls.key"},
				}
			} else {
				auth.Spec.EmbeddedAuthServer.TLSListener = nil
			}
			g.Expect(k8sClient.Update(testCtx, auth)).To(Succeed())
		}, MediumTimeout, DefaultPollingInterval).Should(Succeed())
	}

	BeforeAll(func() {
		testCtx = context.Background()
		namespace = createTestNamespace(testCtx)
		proxyHelper = NewMCPRemoteProxyTestHelper(testCtx, k8sClient, namespace)

		Expect(k8sClient.Create(testCtx, newMCPOIDCConfig(oidcConfigName, namespace))).To(Succeed())
		Expect(k8sClient.Create(testCtx, newEmbeddedAuthConfig(authConfigName, namespace))).To(Succeed())
		proxyHelper.NewRemoteProxyBuilder(proxyName).
			WithAuthServerRef(authConfigName).
			WithOIDCConfigRef(oidcConfigName, "https://test-resource.example.com").
			Create(proxyHelper)
	})

	AfterAll(func() {
		Expect(proxyHelper.CleanupRemoteProxies()).To(Succeed())
		deleteTestNamespace(testCtx, namespace)
	})

	It("writes nothing on a reconcile when the TLS listener is off", func() {
		proxyHelper.WaitForDeployment(proxyName, MediumTimeout)
		proxyHelper.WaitForService(ServiceName(proxyName), MediumTimeout)

		// The objects are asserted as literal shapes, then checked against the
		// drift helpers as read back from the API server.
		Eventually(servicePorts, MediumTimeout, DefaultPollingInterval).Should(Equal([]corev1.ServicePort{httpServicePort}))
		Eventually(containerPorts, MediumTimeout, DefaultPollingInterval).Should(Equal([]corev1.ContainerPort{httpContainerPort}))
		expectNoPortDrift(false)

		svc := &corev1.Service{}
		Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: ServiceName(proxyName), Namespace: namespace}, svc)).To(Succeed())
		svcRV := svc.ResourceVersion

		// A resourceOverrides annotation on the proxy Deployment bumps the CR
		// generation and changes only the Deployment's own metadata: the Service
		// and the ports are untouched. Waiting for observedGeneration and for the
		// annotation on the Deployment proves a reconcile ran and finished before
		// the quiet window starts. The Deployment update rewrites the whole pod
		// template, which may bump the Deployment generation, so the window
		// compares against the values read after that update.
		const marker = "test.toolhive.io/reconcile-marker"
		Eventually(func() error {
			proxy := &mcpv1beta1.MCPRemoteProxy{}
			if err := k8sClient.Get(testCtx, types.NamespacedName{Name: proxyName, Namespace: namespace}, proxy); err != nil {
				return err
			}
			original := proxy.DeepCopy()
			proxy.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
				ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
					ResourceMetadataOverrides: mcpv1beta1.ResourceMetadataOverrides{
						Annotations: map[string]string{marker: "1"},
					},
				},
			}
			return k8sClient.Patch(testCtx, proxy, client.MergeFrom(original))
		}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

		Eventually(func(g Gomega) {
			proxy := &mcpv1beta1.MCPRemoteProxy{}
			g.Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: proxyName, Namespace: namespace}, proxy)).To(Succeed())
			g.Expect(proxy.Status.ObservedGeneration).To(Equal(proxy.Generation))
			d := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: proxyName, Namespace: namespace}, d)).To(Succeed())
			g.Expect(d.Annotations).To(HaveKeyWithValue(marker, "1"))
		}, MediumTimeout, DefaultPollingInterval).Should(Succeed())

		settled := &appsv1.Deployment{}
		Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: proxyName, Namespace: namespace}, settled)).To(Succeed())
		depRV, depGen := settled.ResourceVersion, settled.Generation

		Consistently(func(g Gomega) {
			d := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: proxyName, Namespace: namespace}, d)).To(Succeed())
			s := &corev1.Service{}
			g.Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: ServiceName(proxyName), Namespace: namespace}, s)).To(Succeed())
			g.Expect(d.ResourceVersion).To(Equal(depRV), "Deployment was rewritten again")
			g.Expect(d.Generation).To(Equal(depGen))
			g.Expect(s.ResourceVersion).To(Equal(svcRV), "Service was rewritten")
			g.Expect(s.Spec.Ports).To(Equal([]corev1.ServicePort{httpServicePort}))
			g.Expect(d.Spec.Template.Spec.Containers[0].Ports).To(Equal([]corev1.ContainerPort{httpContainerPort}))
		}, 3*time.Second, 250*time.Millisecond).Should(Succeed())
		expectNoPortDrift(false)
	})

	It("adds https-auth when the listener is turned on and removes it when turned off", func() {
		setTLSListener(true)
		Eventually(servicePorts, MediumTimeout, DefaultPollingInterval).Should(Equal([]corev1.ServicePort{httpServicePort, authServicePort}))
		Eventually(containerPorts, MediumTimeout, DefaultPollingInterval).Should(Equal([]corev1.ContainerPort{httpContainerPort, authContainerPort}))
		expectNoPortDrift(true)

		setTLSListener(false)
		Eventually(servicePorts, MediumTimeout, DefaultPollingInterval).Should(Equal([]corev1.ServicePort{httpServicePort}))
		Eventually(containerPorts, MediumTimeout, DefaultPollingInterval).Should(Equal([]corev1.ContainerPort{httpContainerPort}))
		expectNoPortDrift(false)
	})
})
