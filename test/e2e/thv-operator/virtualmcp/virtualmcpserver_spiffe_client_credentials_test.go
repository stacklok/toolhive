// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// Serial: every SPIFFE e2e suite deploys its own SPIRE agent, and all agents
// publish their Workload API socket at the same host path on the single kind
// node, so two suites running in parallel break each other's socket.
var _ = ginkgo.Describe("VirtualMCPServer SPIFFE client credentials", ginkgo.Ordered, ginkgo.Serial, func() {
	const (
		timeout           = 5 * time.Minute
		pollInterval      = 2 * time.Second
		clientScope       = "mcp.read"
		vmcpPlainPort     = 4483
		authServerTLSPort = 8443
	)

	var (
		vmcpServerName           string
		vmcpServiceName          string
		mcpGroupName             string
		backendName              string
		clientName               string
		clientServiceAccountName string
		clientID                 string
		clientSPIFFEID           string
		hmacSecretName           string
		listenerSecretName       string
		publicCAConfigMapName    string
		oidcConfigName           string
		signingSecretName        string
		spireName                string
		issuer                   string
		resource                 string
		signingKey               *rsa.PrivateKey
		spireInfo                *SPIREInfo
		cleanupSPIRE             func()
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		vmcpServerName = "spiffe-vmcp-" + suffix
		vmcpServiceName = VMCPServiceName(vmcpServerName)
		mcpGroupName = "spiffe-vmcp-group-" + suffix
		backendName = "spiffe-vmcp-backend-" + suffix
		clientName = "spiffe-vmcp-client-" + suffix
		clientServiceAccountName = "spiffe-vmcp-client-sa-" + suffix
		clientID = "spiffe-vmcp-client-" + suffix
		hmacSecretName = "spiffe-vmcp-hmac-" + suffix
		listenerSecretName = "spiffe-vmcp-listener-" + suffix
		publicCAConfigMapName = "spiffe-vmcp-ca-" + suffix
		oidcConfigName = "spiffe-vmcp-oidc-" + suffix
		signingSecretName = "spiffe-vmcp-signing-" + suffix
		spireName = "spiffe-vmcp-" + suffix
		issuer = fmt.Sprintf("https://%s.%s.svc.cluster.local:%d", vmcpServiceName, defaultNamespace, authServerTLSPort)
		resource = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/mcp", vmcpServiceName, defaultNamespace, vmcpPlainPort)
		clientSPIFFEID = fmt.Sprintf(
			"spiffe://%s/workload/%s/%s/%s",
			spireTrustDomain,
			defaultNamespace,
			clientServiceAccountName,
			clientName,
		)

		ginkgo.By("creating the client ServiceAccount")
		gomega.Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: clientServiceAccountName, Namespace: defaultNamespace},
		})).To(gomega.Succeed())

		ginkgo.By("deploying SPIRE and verifying its published trust bundle")
		spireInfo, cleanupSPIRE = deploySPIRE(ctx, k8sClient, spireName, defaultNamespace, timeout, pollInterval)
		waitForSPIFFEBundle(spireInfo, timeout, pollInterval)

		serverPodName := ""
		gomega.Eventually(func() error {
			var err error
			serverPodName, err = findPodName(
				ctx,
				k8sClient,
				defaultNamespace,
				map[string]string{"app": spireInfo.ServerDeploymentName},
			)
			return err
		}, timeout, pollInterval).Should(gomega.Succeed())
		gomega.Expect(CreateSPIREWorkloadEntries(
			ctx,
			k8sClient,
			defaultNamespace,
			serverPodName,
			spireName+"-agent",
			defaultNamespace,
			clientServiceAccountName,
			clientName,
		)).To(gomega.Succeed())

		ginkgo.By("creating listener TLS material and a client-only public CA bundle")
		caPEM, certPEM, keyPEM, _, _ := createSPIFFEListenerCertificate(vmcpServiceName, defaultNamespace)
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: listenerSecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM},
		})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: publicCAConfigMapName, Namespace: defaultNamespace},
			Data:       map[string]string{"ca.crt": string(caPEM)},
		})).To(gomega.Succeed())

		signingKey = generateSPIFFERSAKey()
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: signingSecretName, Namespace: defaultNamespace},
			Data: map[string][]byte{
				"private-key": pem.EncodeToMemory(&pem.Block{
					Type:  "RSA PRIVATE KEY",
					Bytes: x509.MarshalPKCS1PrivateKey(signingKey),
				}),
			},
		})).To(gomega.Succeed())
		hmac := make([]byte, 32)
		_, err := rand.Read(hmac)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: hmacSecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"hmac": hmac},
		})).To(gomega.Succeed())

		ginkgo.By("creating an MCPOIDCConfig trusting the vMCP's own embedded issuer")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{
					Issuer:  issuer,
					JWKSURL: issuer + "/.well-known/jwks.json",
					CABundleRef: &mcpv1beta1.CABundleSource{
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: publicCAConfigMapName},
						},
					},
					JWKSAllowPrivateIP: true,
				},
			},
		})).To(gomega.Succeed())

		ginkgo.By("creating the MCPGroup and a minimal no-auth backend")
		mcpGroup := &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
			Spec:       mcpv1beta1.MCPGroupSpec{Description: "SPIFFE vMCP client credentials E2E test group"},
		}
		gomega.Expect(k8sClient.Create(ctx, mcpGroup)).To(gomega.Succeed())
		gomega.Eventually(func() mcpv1beta1.MCPGroupPhase {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: mcpGroupName, Namespace: defaultNamespace,
			}, mcpGroup)).To(gomega.Succeed())
			return mcpGroup.Status.Phase
		}, timeout, pollInterval).Should(gomega.Equal(mcpv1beta1.MCPGroupPhaseReady))

		CreateMultipleMCPServersInParallel(ctx, k8sClient, []BackendConfig{
			{
				Name:      backendName,
				Namespace: defaultNamespace,
				GroupRef:  mcpGroupName,
				Image:     images.GofetchServerImage,
				// No ExternalAuthConfigRef - this backend has no auth, so vMCP can
				// come up and discover it without needing an Upstream Provider.
			},
		}, timeout, pollInterval)
		WaitForPodsReady(ctx, k8sClient, defaultNamespace, map[string]string{
			"app.kubernetes.io/name":     "mcpserver",
			"app.kubernetes.io/instance": backendName,
		}, timeout, pollInterval)

		ginkgo.By("creating the VirtualMCPServer with an inline SPIFFE-enabled embedded auth server")
		vmcpServer := v1beta1test.NewVirtualMCPServer(vmcpServerName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(mcpGroupName),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{Group: mcpGroupName}),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "oidc",
				OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
					Name: oidcConfigName, Audience: resource, ResourceURL: resource,
					Scopes: []string{"openid", clientScope},
				},
			}),
			v1beta1test.WithVMCPOutgoingAuth(&mcpv1beta1.OutgoingAuthConfig{Source: "discovered"}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: issuer,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{
					Name: signingSecretName, Key: "private-key",
				}},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				SPIFFETrustDomains: []mcpv1beta1.SPIFFETrustDomainConfig{{
					Name:        "spire",
					TrustDomain: spireInfo.TrustDomain,
					Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{
						mcpv1beta1.SPIFFEAuthenticationMethodX509,
						mcpv1beta1.SPIFFEAuthenticationMethodJWT,
					},
					BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
						Type: mcpv1beta1.SPIFFEBundleSourceTypeFile,
						File: &mcpv1beta1.SPIFFEFileBundleSourceConfig{
							ConfigMapName: spireInfo.BundleConfigMapName,
							ConfigMapKey:  spireInfo.BundleConfigMapKey,
						},
					},
				}},
				InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientConfig{{
					TrustDomainRef:   "spire",
					PrincipalPattern: clientSPIFFEID,
					ClientID:         clientID,
					Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{
						mcpv1beta1.SPIFFEAuthenticationMethodX509,
						mcpv1beta1.SPIFFEAuthenticationMethodJWT,
					},
					Resources:  []string{resource},
					Audiences:  []string{resource},
					Scopes:     []string{clientScope},
					GrantTypes: []string{"client_credentials"},
				}}},
				TLSListener: &mcpv1beta1.TLSListenerConfig{
					CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: listenerSecretName, Key: "tls.crt"},
					PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: listenerSecretName, Key: "tls.key"},
				},
			}),
			v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
				v.Spec.ServiceType = "NodePort"
				// Debug logs make SPIFFE client-auth decisions visible in failure
				// dumps; the operator passes --debug to vmcp for this log level.
				v.Spec.Config.Operational = &vmcpconfig.OperationalConfig{LogLevel: "debug"}
			}),
		)
		gomega.Expect(k8sClient.Create(ctx, vmcpServer)).To(gomega.Succeed())

		ginkgo.By("waiting for the VirtualMCPServer to become ready and discover its backend")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpServerName, defaultNamespace, timeout, pollInterval)
		WaitForCondition(ctx, k8sClient, vmcpServerName, defaultNamespace, "BackendsDiscovered", "True", timeout, pollInterval)
		WaitForPodsReady(ctx, k8sClient, defaultNamespace, map[string]string{
			"app.kubernetes.io/name":     "virtualmcpserver",
			"app.kubernetes.io/instance": vmcpServerName,
		}, timeout, pollInterval)

		ginkgo.By("creating the attested SPIFFE client workload")
		socketVolume, socketMount, socketEnv, err := SPIREWorkloadAPIVolume("spire-socket")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clientTemplate := corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "mcp"},
					{
						Name:            "spiffe-client",
						Image:           spiffeClientE2EImage,
						ImagePullPolicy: corev1.PullNever,
						Args:            []string{"hold"},
						Env:             []corev1.EnvVar{socketEnv},
						VolumeMounts: []corev1.VolumeMount{
							socketMount,
							{Name: "as-ca", MountPath: "/var/run/spiffe-ca", ReadOnly: true},
						},
					},
				},
				Volumes: []corev1.Volume{
					socketVolume,
					{
						Name: "as-ca",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: publicCAConfigMapName},
						}},
					},
				},
			},
		}
		rawTemplate, err := json.Marshal(clientTemplate)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(k8sClient.Create(ctx, newSPIFFEMCPServer(
			clientName,
			&clientServiceAccountName,
			nil,
			&runtime.RawExtension{Raw: rawTemplate},
		))).To(gomega.Succeed())
		waitForSPIFFEMCPServerReady(clientName, timeout, pollInterval)
	})

	ginkgo.AfterAll(func() {
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: clientName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpServerName, defaultNamespace))
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: clientServiceAccountName, Namespace: defaultNamespace},
		})
		for _, name := range []string{hmacSecretName, listenerSecretName, signingSecretName} {
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
			})
		}
		_ = k8sClient.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: publicCAConfigMapName, Namespace: defaultNamespace},
		})
		if cleanupSPIRE != nil {
			cleanupSPIRE()
		}
	})

	ginkgo.It("issues equivalent X.509-SVID and JWT-SVID client credentials tokens from the vMCP issuer", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		assertSPIFFEClientPod(clientPod, publicCAConfigMapName)
		vmcpPod := findVMCPProxyPod(vmcpServerName)
		assertSPIFFEASPod(vmcpPod, spireInfo.BundleConfigMapName)

		identity, err := fetchSPIFFEIdentity(clientPod.Name, spireInfo.AgentSocketPath, issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(identity.SPIFFEID).To(gomega.Equal(clientSPIFFEID))

		x509Claims := verifySPIFFEAccessToken(
			requestSPIFFEToken(clientPod.Name, "x509", spireInfo.AgentSocketPath, issuer, clientID, resource, clientScope),
			&signingKey.PublicKey)
		jwtClaims := verifySPIFFEAccessToken(
			requestSPIFFEToken(clientPod.Name, "jwt", spireInfo.AgentSocketPath, issuer, clientID, resource, clientScope),
			&signingKey.PublicKey)
		assertEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims, issuer, clientSPIFFEID, clientID, resource, clientScope)
	})

	ginkgo.It("exposes both the plain vMCP port and the dedicated https-auth listener port on the Service", func() {
		svc := &corev1.Service{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: vmcpServiceName, Namespace: defaultNamespace,
		}, svc)).To(gomega.Succeed())

		var httpPort, httpsAuthPort *corev1.ServicePort
		for i := range svc.Spec.Ports {
			switch svc.Spec.Ports[i].Name {
			case "http":
				httpPort = &svc.Spec.Ports[i]
			case ctrlutil.AuthServerTLSListenerPortName:
				httpsAuthPort = &svc.Spec.Ports[i]
			}
		}
		gomega.Expect(httpPort).NotTo(gomega.BeNil(), "vMCP Service must still expose a plain HTTP port")
		gomega.Expect(httpPort.Port).To(gomega.Equal(int32(vmcpPlainPort)))
		gomega.Expect(httpsAuthPort).NotTo(gomega.BeNil(), "vMCP Service must expose the dedicated TLS listener port")
		gomega.Expect(httpsAuthPort.Port).To(gomega.Equal(int32(authServerTLSPort)))
		gomega.Expect(httpsAuthPort.AppProtocol).NotTo(gomega.BeNil())
		gomega.Expect(*httpsAuthPort.AppProtocol).To(gomega.Equal("https"))
	})

	ginkgo.It("calls a backend tool through the vMCP using the issued SPIFFE client credentials token", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		token := requestSPIFFEToken(clientPod.Name, "x509", spireInfo.AgentSocketPath, issuer, clientID, resource, clientScope)

		var vmcpNodePort int32
		gomega.Eventually(func() error {
			service := &corev1.Service{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name: vmcpServiceName, Namespace: defaultNamespace,
			}, service); err != nil {
				return err
			}
			for _, port := range service.Spec.Ports {
				if port.Name == "http" && port.NodePort != 0 {
					vmcpNodePort = port.NodePort
					return nil
				}
			}
			return fmt.Errorf("plain HTTP nodePort not assigned yet")
		}, timeout, pollInterval).Should(gomega.Succeed())

		authenticatedHTTPClient := &http.Client{
			Transport: &authRoundTripper{token: token, transport: http.DefaultTransport},
			Timeout:   30 * time.Second,
		}

		expectedToolName := backendName + "_fetch"
		tools, mcpClient := WaitForExpectedToolsWithAuth(
			vmcpNodePort, timeout,
			func(tools []mcp.Tool) error {
				return ToolsContainAll(tools, expectedToolName)
			},
			WithHttpLoggerOption(), transport.WithHTTPBasicClient(authenticatedHTTPClient),
		)
		defer mcpClient.Close()
		gomega.Expect(len(tools.Tools)).To(gomega.BeNumerically(">=", 1))

		toolCallCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		callRequest := mcp.CallToolRequest{}
		callRequest.Params.Name = expectedToolName
		callRequest.Params.Arguments = map[string]any{"url": "https://example.com"}
		result, err := mcpClient.CallTool(toolCallCtx, callRequest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result).NotTo(gomega.BeNil())
	})
})

// findVMCPProxyPod finds the running pod backing the VirtualMCPServer Deployment
// (named after the CR itself, unlike its "vmcp-"-prefixed Service).
func findVMCPProxyPod(vmcpName string) *corev1.Pod {
	pod := &corev1.Pod{}
	gomega.Eventually(func() error {
		deployment := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name: vmcpName, Namespace: defaultNamespace,
		}, deployment); err != nil {
			return err
		}
		pods := &corev1.PodList{}
		if err := k8sClient.List(ctx, pods,
			client.InNamespace(defaultNamespace),
			client.MatchingLabels(deployment.Spec.Selector.MatchLabels),
		); err != nil {
			return err
		}
		for i := range pods.Items {
			candidate := &pods.Items[i]
			if candidate.DeletionTimestamp == nil && hasContainer(candidate, "vmcp") {
				*pod = *candidate
				return nil
			}
		}
		return fmt.Errorf("no proxy pod found for VirtualMCPServer %q", vmcpName)
	}, e2eTimeout, e2ePollInterval).Should(gomega.Succeed())
	return pod
}
