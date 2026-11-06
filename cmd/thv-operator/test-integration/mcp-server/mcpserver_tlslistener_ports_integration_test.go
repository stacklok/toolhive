// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

var _ = Describe("MCPServer TLS listener ports", Label("k8s", "tlslistener"), func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("TLS listener port reconciliation against a real API server", Ordered, func() {
		var (
			namespace      = "tlslistener-ports-mcpserver"
			serverName     = "tlslistener-ports"
			authConfigName = "tlslistener-embedded-auth"
			oidcConfigName = "tlslistener-oidc"

			serverKey     = types.NamespacedName{Name: serverName, Namespace: namespace}
			deploymentKey = types.NamespacedName{Name: serverName, Namespace: namespace}
			serviceKey    = types.NamespacedName{Name: "mcp-" + serverName + "-proxy", Namespace: namespace}
			authKey       = types.NamespacedName{Name: authConfigName, Namespace: namespace}

			httpServicePort = corev1.ServicePort{
				Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP,
			}
			authServicePort = corev1.ServicePort{
				Name: "https-auth", Port: 8443, TargetPort: intstr.FromInt32(8443), Protocol: corev1.ProtocolTCP,
				AppProtocol: ptr.To("https"),
			}
			metricsServicePort = corev1.ServicePort{
				Name: "metrics", Port: 9090, TargetPort: intstr.FromInt32(9090), Protocol: corev1.ProtocolTCP,
			}
			httpContainerPort    = corev1.ContainerPort{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}
			authContainerPort    = corev1.ContainerPort{Name: "https-auth", ContainerPort: 8443, Protocol: corev1.ProtocolTCP}
			metricsContainerPort = corev1.ContainerPort{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP}
		)

		getDeployment := func() (*appsv1.Deployment, error) {
			d := &appsv1.Deployment{}
			return d, k8sClient.Get(ctx, deploymentKey, d)
		}
		getService := func() (*corev1.Service, error) {
			s := &corev1.Service{}
			return s, k8sClient.Get(ctx, serviceKey, s)
		}
		servicePorts := func(g Gomega) []corev1.ServicePort {
			s, err := getService()
			g.Expect(err).NotTo(HaveOccurred())
			return s.Spec.Ports
		}
		containerPorts := func(g Gomega) []corev1.ContainerPort {
			d, err := getDeployment()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(d.Spec.Template.Spec.Containers).NotTo(BeEmpty())
			return d.Spec.Template.Spec.Containers[0].Ports
		}
		// expectNoPortDrift reads both objects back from the API server and asserts
		// the drift checks accept them as stored.
		expectNoPortDrift := func(tlsListener bool) {
			svc, err := getService()
			Expect(err).NotTo(HaveOccurred())
			dep, err := getDeployment()
			Expect(err).NotTo(HaveOccurred())
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
				g.Expect(k8sClient.Get(ctx, authKey, auth)).To(Succeed())
				if on {
					auth.Spec.EmbeddedAuthServer.TLSListener = &mcpv1beta1.TLSListenerConfig{
						CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "tlslistener-cert", Key: "tls.crt"},
						PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "tlslistener-cert", Key: "tls.key"},
					}
				} else {
					auth.Spec.EmbeddedAuthServer.TLSListener = nil
				}
				g.Expect(k8sClient.Update(ctx, auth)).To(Succeed())
			}, timeout, interval).Should(Succeed())
		}

		BeforeAll(func() {
			err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: namespace},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer:            "http://localhost:9090",
						InsecureAllowHTTP: true,
					},
				},
			})).To(Succeed())

			Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: namespace},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
						Issuer: "http://localhost:9090",
						UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
							Name: "test-provider",
							Type: mcpv1beta1.UpstreamProviderTypeOIDC,
							OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
								IssuerURL: "https://accounts.google.com",
								ClientID:  "test-client-id",
							},
						}},
					},
				},
			})).To(Succeed())

			Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:v1.0.0",
					Transport: "streamable-http",
					ProxyPort: 8080,
					AuthServerRef: &mcpv1beta1.AuthServerRef{
						Kind: "MCPExternalAuthConfig",
						Name: authConfigName,
					},
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
						Name:        oidcConfigName,
						Audience:    "https://test-resource.example.com",
						ResourceURL: "https://test-resource.example.com",
					},
				},
			})).To(Succeed())
		})

		AfterAll(func() {
			server := &mcpv1beta1.MCPServer{}
			if err := k8sClient.Get(ctx, serverKey, server); err == nil && len(server.Finalizers) > 0 {
				original := server.DeepCopy()
				server.Finalizers = nil
				// Test-only teardown: no concurrent writers.
				_ = k8sClient.Patch(ctx, server, client.MergeFrom(original))
			}
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: serverName, Namespace: namespace}})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: namespace}})
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: namespace}})
		})

		It("writes nothing on a reconcile when the TLS listener is off", func() {
			Eventually(func(g Gomega) {
				_, err := getDeployment()
				g.Expect(err).NotTo(HaveOccurred())
				_, err = getService()
				g.Expect(err).NotTo(HaveOccurred())
			}, timeout, interval).Should(Succeed())

			// The objects are asserted as literal shapes, then checked against the
			// drift helpers as read back from the API server.
			Eventually(servicePorts, timeout, interval).Should(Equal([]corev1.ServicePort{httpServicePort}))
			Eventually(containerPorts, timeout, interval).Should(Equal([]corev1.ContainerPort{httpContainerPort}))
			expectNoPortDrift(false)

			svc, err := getService()
			Expect(err).NotTo(HaveOccurred())
			svcRV := svc.ResourceVersion

			// A resourceOverrides annotation on the proxy Deployment bumps the CR
			// generation and changes nothing the operator renders into the Service
			// or the ports. Waiting for observedGeneration and for the annotation
			// on the Deployment proves a reconcile ran and finished before the
			// quiet window starts. The generation is also stamped on the pod
			// template, so the Deployment may be rewritten for this change; the
			// window shows it is not rewritten again afterwards.
			const marker = "test.toolhive.io/reconcile-marker"
			Eventually(func() error {
				server := &mcpv1beta1.MCPServer{}
				if err := k8sClient.Get(ctx, serverKey, server); err != nil {
					return err
				}
				original := server.DeepCopy()
				server.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						ResourceMetadataOverrides: mcpv1beta1.ResourceMetadataOverrides{
							Annotations: map[string]string{marker: "1"},
						},
					},
				}
				return k8sClient.Patch(ctx, server, client.MergeFrom(original))
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				server := &mcpv1beta1.MCPServer{}
				g.Expect(k8sClient.Get(ctx, serverKey, server)).To(Succeed())
				g.Expect(server.Status.ObservedGeneration).To(Equal(server.Generation))
				d, err := getDeployment()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(d.Annotations).To(HaveKeyWithValue(marker, "1"))
			}, timeout, interval).Should(Succeed())

			dep, err := getDeployment()
			Expect(err).NotTo(HaveOccurred())
			depRV, depGen := dep.ResourceVersion, dep.Generation

			Consistently(func(g Gomega) {
				d, err := getDeployment()
				g.Expect(err).NotTo(HaveOccurred())
				s, err := getService()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(d.ResourceVersion).To(Equal(depRV), "Deployment was rewritten again")
				g.Expect(d.Generation).To(Equal(depGen))
				g.Expect(s.ResourceVersion).To(Equal(svcRV), "Service was rewritten")
				g.Expect(s.Spec.Ports).To(Equal([]corev1.ServicePort{httpServicePort}))
				g.Expect(d.Spec.Template.Spec.Containers[0].Ports).To(Equal([]corev1.ContainerPort{httpContainerPort}))
			}, 3*time.Second, interval).Should(Succeed())
			expectNoPortDrift(false)
		})

		It("leaves ports added by another writer alone", func() {
			Eventually(func(g Gomega) {
				svc, err := getService()
				g.Expect(err).NotTo(HaveOccurred())
				svc.Spec.Ports = append(svc.Spec.Ports, metricsServicePort)
				g.Expect(k8sClient.Update(ctx, svc)).To(Succeed())
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				dep, err := getDeployment()
				g.Expect(err).NotTo(HaveOccurred())
				c := &dep.Spec.Template.Spec.Containers[0]
				c.Ports = append(c.Ports, metricsContainerPort)
				g.Expect(k8sClient.Update(ctx, dep)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			dep, err := getDeployment()
			Expect(err).NotTo(HaveOccurred())
			svc, err := getService()
			Expect(err).NotTo(HaveOccurred())
			depRV, svcRV := dep.ResourceVersion, svc.ResourceVersion

			Consistently(func(g Gomega) {
				d, err := getDeployment()
				g.Expect(err).NotTo(HaveOccurred())
				s, err := getService()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(d.ResourceVersion).To(Equal(depRV), "Deployment was rewritten")
				g.Expect(s.ResourceVersion).To(Equal(svcRV), "Service was rewritten")
				g.Expect(d.Spec.Template.Spec.Containers[0].Ports).To(ContainElement(metricsContainerPort))
				g.Expect(s.Spec.Ports).To(ContainElement(metricsServicePort))
			}, 5*time.Second, interval).Should(Succeed())
			expectNoPortDrift(false)
		})

		It("adds https-auth when the listener is turned on and removes it when turned off", func() {
			setTLSListener(true)

			// Whole-list assertions: the Service update replaces the port list,
			// so the earlier metrics port is gone.
			Eventually(servicePorts, timeout, interval).Should(Equal([]corev1.ServicePort{httpServicePort, authServicePort}))
			Eventually(containerPorts, timeout, interval).Should(Equal([]corev1.ContainerPort{httpContainerPort, authContainerPort}))
			expectNoPortDrift(true)

			setTLSListener(false)

			Eventually(servicePorts, timeout, interval).Should(Equal([]corev1.ServicePort{httpServicePort}))
			Eventually(containerPorts, timeout, interval).Should(Equal([]corev1.ContainerPort{httpContainerPort}))
			expectNoPortDrift(false)
		})
	})
})
