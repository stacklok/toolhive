// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

var _ = Describe("VirtualMCPServer TLS listener ports", Label("k8s", "tlslistener"), func() {
	const (
		timeout   = time.Second * 30
		interval  = time.Millisecond * 250
		namespace = "tlslistener-ports-vmcp"
		groupName = "tlslistener-group"
		oidcName  = "tlslistener-oidc"
	)

	httpServicePort := corev1.ServicePort{
		Name: "http", Port: 4483, TargetPort: intstr.FromInt32(4483), Protocol: corev1.ProtocolTCP,
	}
	authServicePort := corev1.ServicePort{
		Name: "https-auth", Port: 8443, TargetPort: intstr.FromInt32(8443), Protocol: corev1.ProtocolTCP,
		AppProtocol: ptr.To("https"),
	}
	httpContainerPort := corev1.ContainerPort{Name: "http", ContainerPort: 4483, Protocol: corev1.ProtocolTCP}
	authContainerPort := corev1.ContainerPort{Name: "https-auth", ContainerPort: 8443, Protocol: corev1.ProtocolTCP}
	const vmcpPort = int32(4483)

	authServerConfig := func(tlsListener bool) *mcpv1beta1.EmbeddedAuthServerConfig {
		cfg := &mcpv1beta1.EmbeddedAuthServerConfig{
			Issuer: "https://authserver.example.com",
			UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
				Name: "okta",
				Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
				OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://example.com/oauth/authorize",
					TokenEndpoint:         "https://example.com/oauth/token",
					ClientID:              "test-client-id",
				},
			}},
		}
		if tlsListener {
			cfg.TLSListener = &mcpv1beta1.TLSListenerConfig{
				CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "tlslistener-cert", Key: "tls.crt"},
				PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "tlslistener-cert", Key: "tls.key"},
			}
		}
		return cfg
	}

	ensureNamespaceAndGroup := func() {
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		err = k8sClient.Create(ctx, &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: groupName, Namespace: namespace},
			Spec:       mcpv1beta1.MCPGroupSpec{Description: "TLS listener port tests"},
		})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		err = k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcName, Namespace: namespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://authserver.example.com"},
			},
		})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		Eventually(func() (mcpv1beta1.MCPGroupPhase, error) {
			group := &mcpv1beta1.MCPGroup{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: groupName, Namespace: namespace}, group)
			return group.Status.Phase, err
		}, timeout, interval).Should(Equal(mcpv1beta1.MCPGroupPhaseReady))
	}

	newVMCP := func(name string, tlsListener bool, opts ...v1beta1test.VirtualMCPServerOption) *mcpv1beta1.VirtualMCPServer {
		base := []v1beta1test.VirtualMCPServerOption{
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{Group: groupName}),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "oidc",
				OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
					Name:        oidcName,
					Audience:    "https://vmcp.example.com",
					ResourceURL: "https://vmcp.example.com",
				},
			}),
			v1beta1test.WithVMCPAuthServerConfig(authServerConfig(tlsListener)),
		}
		return v1beta1test.NewVirtualMCPServer(name, namespace, append(base, opts...)...)
	}

	keyOf := func(name string) types.NamespacedName { return types.NamespacedName{Name: name, Namespace: namespace} }
	svcKeyOf := func(name string) types.NamespacedName { return keyOf("vmcp-" + name) }

	servicePorts := func(name string) func(Gomega) []corev1.ServicePort {
		return func(g Gomega) []corev1.ServicePort {
			svc := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx, svcKeyOf(name), svc)).To(Succeed())
			return svc.Spec.Ports
		}
	}
	vmcpContainerPorts := func(name string) func(Gomega) []corev1.ContainerPort {
		return func(g Gomega) []corev1.ContainerPort {
			dep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, keyOf(name), dep)).To(Succeed())
			for _, c := range dep.Spec.Template.Spec.Containers {
				if c.Name == "vmcp" {
					return c.Ports
				}
			}
			g.Expect(false).To(BeTrue(), "vmcp container not found")
			return nil
		}
	}
	// storedPorts reads the Service and the vmcp container ports back from the
	// API server.
	storedPorts := func(name string) ([]corev1.ServicePort, []corev1.ContainerPort) {
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, svcKeyOf(name), svc)).To(Succeed())
		return svc.Spec.Ports, vmcpContainerPorts(name)(Default)
	}
	expectServicePortsNotDrifted := func(name string, tlsListener bool) {
		svcPorts, _ := storedPorts(name)
		Expect(ctrlutil.ServicePortsDrifted(svcPorts, ctrlutil.ProxyServicePorts(vmcpPort, tlsListener), ctrlutil.ProxyOwnedPortNames())).
			To(BeFalse(), "stored Service ports reported as drift")
	}
	expectContainerPortsNotDrifted := func(name string, tlsListener bool) {
		_, containerPorts := storedPorts(name)
		Expect(ctrlutil.ContainerPortsDrifted(containerPorts, ctrlutil.ProxyContainerPorts(vmcpPort, tlsListener), ctrlutil.ProxyOwnedPortNames())).
			To(BeFalse(), "stored container ports reported as drift")
	}
	// poke patches an incrementing test-only annotation onto the VirtualMCPServer.
	// The controller's For() has no generation predicate, so this enqueues a
	// reconcile without changing the spec.
	pokes := 0
	poke := func(name string) {
		pokes++
		Eventually(func() error {
			vmcp := &mcpv1beta1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, keyOf(name), vmcp); err != nil {
				return err
			}
			original := vmcp.DeepCopy()
			if vmcp.Annotations == nil {
				vmcp.Annotations = map[string]string{}
			}
			vmcp.Annotations["test.toolhive.io/poke"] = strconv.Itoa(pokes)
			return k8sClient.Patch(ctx, vmcp, client.MergeFrom(original))
		}, timeout, interval).Should(Succeed())
	}
	// updateSpec applies mutate to the VirtualMCPServer and returns the
	// resulting metadata.generation.
	updateSpec := func(name string, mutate func(*mcpv1beta1.VirtualMCPServer)) int64 {
		var gen int64
		Eventually(func() error {
			vmcp := &mcpv1beta1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, keyOf(name), vmcp); err != nil {
				return err
			}
			mutate(vmcp)
			if err := k8sClient.Update(ctx, vmcp); err != nil {
				return err
			}
			gen = vmcp.Generation
			return nil
		}, timeout, interval).Should(Succeed())
		return gen
	}
	waitObserved := func(name string, gen int64) {
		Eventually(func() (int64, error) {
			vmcp := &mcpv1beta1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, keyOf(name), vmcp); err != nil {
				return 0, err
			}
			return vmcp.Status.ObservedGeneration, nil
		}, timeout, interval).Should(BeNumerically(">=", gen))
	}
	deleteVMCP := func(name string) {
		_ = k8sClient.Delete(ctx, &mcpv1beta1.VirtualMCPServer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
	}

	JustAfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		list := &mcpv1beta1.VirtualMCPServerList{}
		if err := k8sClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return
		}
		for i := range list.Items {
			status, _ := json.MarshalIndent(list.Items[i].Status, "", "  ")
			AddReportEntry("VirtualMCPServer status "+list.Items[i].Name, string(status))
		}
	})

	Context("TLS listener off", Ordered, func() {
		const name = "tlslistener-off"

		BeforeAll(func() {
			ensureNamespaceAndGroup()
			Expect(k8sClient.Create(ctx, newVMCP(name, false))).To(Succeed())
		})
		AfterAll(func() { deleteVMCP(name) })

		It("writes nothing on a reconcile", func() {
			Eventually(servicePorts(name), timeout, interval).Should(Equal([]corev1.ServicePort{httpServicePort}))
			Eventually(vmcpContainerPorts(name), timeout, interval).Should(Equal([]corev1.ContainerPort{httpContainerPort}))
			expectServicePortsNotDrifted(name, false)
			expectContainerPortsNotDrifted(name, false)

			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, keyOf(name), dep)).To(Succeed())
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, svcKeyOf(name), svc)).To(Succeed())
			depRV, svcRV, depGen := dep.ResourceVersion, svc.ResourceVersion, dep.Generation

			// An annotation change gives no observedGeneration to wait on, so the
			// window below only detects writes that change the stored objects. The
			// drift assertions above are the pin for false-positive drift, which
			// rewrites identical content that the API server does not persist.
			poke(name)

			Consistently(func(g Gomega) {
				d := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, keyOf(name), d)).To(Succeed())
				s := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, svcKeyOf(name), s)).To(Succeed())
				g.Expect(d.ResourceVersion).To(Equal(depRV), "Deployment was rewritten")
				g.Expect(d.Generation).To(Equal(depGen))
				g.Expect(s.ResourceVersion).To(Equal(svcRV), "Service was rewritten")
			}, 5*time.Second, interval).Should(Succeed())
		})
	})

	Context("TLS listener with NodePort turned into ClusterIP", Ordered, func() {
		const name = "tlslistener-nodeport"

		BeforeAll(func() {
			ensureNamespaceAndGroup()
			Expect(k8sClient.Create(ctx, newVMCP(name, true,
				v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.ServiceType = "NodePort" }),
			))).To(Succeed())
		})
		AfterAll(func() { deleteVMCP(name) })

		It("keeps both ports and the ClusterIP when the type changes", func() {
			var clusterIP string
			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, svcKeyOf(name), svc)).To(Succeed())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
				g.Expect(svc.Spec.Ports).To(HaveLen(2))
				for _, p := range svc.Spec.Ports {
					g.Expect(p.NodePort).NotTo(BeZero(), "port %s has no NodePort", p.Name)
				}
				clusterIP = svc.Spec.ClusterIP
				g.Expect(clusterIP).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
			expectServicePortsNotDrifted(name, true)

			gen := updateSpec(name, func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.ServiceType = "ClusterIP" })
			waitObserved(name, gen)

			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, svcKeyOf(name), svc)).To(Succeed())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
				g.Expect(svc.Spec.Ports).To(Equal([]corev1.ServicePort{httpServicePort, authServicePort}))
				g.Expect(svc.Spec.ClusterIP).To(Equal(clusterIP))
			}, timeout, interval).Should(Succeed())
			expectServicePortsNotDrifted(name, true)
		})
	})

	Context("NodePort preservation when the TLS listener is turned on", Ordered, func() {
		const name = "tlslistener-nodeport-keep"

		BeforeAll(func() {
			ensureNamespaceAndGroup()
			Expect(k8sClient.Create(ctx, newVMCP(name, false,
				v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) { v.Spec.ServiceType = "NodePort" }),
			))).To(Succeed())
		})
		AfterAll(func() { deleteVMCP(name) })

		It("keeps the http NodePort across the whole-list port replace", func() {
			var nodePort int32
			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, svcKeyOf(name), svc)).To(Succeed())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
				g.Expect(svc.Spec.Ports).To(HaveLen(1))
				nodePort = svc.Spec.Ports[0].NodePort
				g.Expect(nodePort).NotTo(BeZero())
			}, timeout, interval).Should(Succeed())

			gen := updateSpec(name, func(v *mcpv1beta1.VirtualMCPServer) {
				v.Spec.AuthServerConfig = authServerConfig(true)
			})
			waitObserved(name, gen)

			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, svcKeyOf(name), svc)).To(Succeed())
				g.Expect(svc.Spec.Ports).To(HaveLen(2))
				g.Expect(svc.Spec.Ports[0].Name).To(Equal("http"))
				g.Expect(svc.Spec.Ports[0].NodePort).To(Equal(nodePort), "http NodePort changed")
				g.Expect(svc.Spec.Ports[1].Name).To(Equal("https-auth"))
				g.Expect(svc.Spec.Ports[1].NodePort).NotTo(BeZero())
			}, timeout, interval).Should(Succeed())
			expectServicePortsNotDrifted(name, true)

			Eventually(vmcpContainerPorts(name), timeout, interval).Should(Equal([]corev1.ContainerPort{httpContainerPort, authContainerPort}))
			expectContainerPortsNotDrifted(name, true)
		})
	})

	Context("TLS listener with a podTemplateSpec that renames the port", Ordered, func() {
		const name = "tlslistener-rename"

		BeforeAll(func() {
			ensureNamespaceAndGroup()
			Expect(k8sClient.Create(ctx, newVMCP(name, true,
				v1beta1test.WithVMCPPodTemplateSpec(&runtime.RawExtension{
					Raw: []byte(`{"spec":{"containers":[{"name":"vmcp","ports":[{"containerPort":8443,"name":"https"}]}]}}`),
				}),
			))).To(Succeed())
		})
		AfterAll(func() { deleteVMCP(name) })

		It("stores the renamed port and does not update the Deployment in a loop", func() {
			Eventually(vmcpContainerPorts(name), timeout, interval).Should(ContainElement(
				corev1.ContainerPort{Name: "https", ContainerPort: 8443, Protocol: corev1.ProtocolTCP},
			))
			Expect(vmcpContainerPorts(name)(Default)).NotTo(ContainElement(HaveField("Name", "https-auth")))
			// The renamed port is matched by number and protocol, not by name.
			expectContainerPortsNotDrifted(name, true)

			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, keyOf(name), dep)).To(Succeed())
			depRV := dep.ResourceVersion

			// As above: the window only detects writes that change the stored
			// Deployment; the drift assertion above pins the false-positive case.
			poke(name)

			Consistently(func(g Gomega) {
				d := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, keyOf(name), d)).To(Succeed())
				g.Expect(d.ResourceVersion).To(Equal(depRV), "Deployment was rewritten")
			}, 5*time.Second, interval).Should(Succeed())
		})
	})
})
