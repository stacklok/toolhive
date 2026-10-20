// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = ginkgo.Describe("VirtualMCPServer delegate clients with an upstream", ginkgo.Ordered, func() {
	const (
		timeout      = 5 * time.Minute
		pollInterval = 2 * time.Second
		clientID     = "e2e-up-delegate-client"
		clientSecret = "e2e-up-delegate-client-secret-testing"
	)

	var (
		backendName, delegateSecretName, dexClientSecretName string
		dexName, groupName, hmacSecretName, oidcConfigName   string
		signingKeySecretName, vmcpName, vmcpHost             string
		issuer                                               string
		dexInfo                                              *DexInfo
		dexCleanup                                           func()
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-up-delegate-backend-" + suffix
		delegateSecretName = "e2e-up-delegate-secret-" + suffix
		dexClientSecretName = "e2e-up-delegate-dex-secret-" + suffix
		dexName = "e2e-up-delegate-dex-" + suffix
		groupName = "e2e-up-delegate-group-" + suffix
		hmacSecretName = "e2e-up-delegate-hmac-" + suffix
		oidcConfigName = "e2e-up-delegate-oidc-" + suffix
		signingKeySecretName = "e2e-up-delegate-key-" + suffix
		vmcpName = "e2e-up-delegate-vmcp-" + suffix
		vmcpHost = fmt.Sprintf("vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)
		issuer = "https://" + vmcpHost

		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: delegateSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"secret": clientSecret},
		})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: dexClientSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"client-secret": "authserver-secret"},
		})).To(gomega.Succeed())
		privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: signingKeySecretName, Namespace: defaultNamespace},
			Data: map[string][]byte{"private-key": pem.EncodeToMemory(&pem.Block{
				Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
			})},
		})).To(gomega.Succeed())
		hmac := make([]byte, 32)
		_, err = rand.Read(hmac)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: hmacSecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"hmac": hmac},
		})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: issuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())

		ginkgo.By("deploying Dex as the embedded authorization server upstream")
		dexInfo, dexCleanup = deployDex(ctx, k8sClient, dexName, issuer+"/oauth/callback")
		CreateMCPGroupAndWait(ctx, k8sClient, groupName, defaultNamespace, "upstream delegate client e2e group", timeout, pollInterval)
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewMCPServer(backendName, defaultNamespace,
			v1beta1test.WithImage(images.YardstickServerImage),
			v1beta1test.WithTransport("streamable-http"),
			v1beta1test.WithProxyPort(8080),
			v1beta1test.WithMCPPort(8080),
			v1beta1test.WithMCPGroupRef(groupName),
		))).To(gomega.Succeed())
		gomega.Eventually(func() mcpv1beta1.MCPServerPhase {
			server := &mcpv1beta1.MCPServer{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendName, Namespace: defaultNamespace}, server)).To(gomega.Succeed())
			return server.Status.Phase
		}, timeout, pollInterval).Should(gomega.Equal(mcpv1beta1.MCPServerPhaseReady))

		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigName, Audience: issuer, ResourceURL: issuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               issuer,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				DelegateClients: []mcpv1beta1.DelegateClientConfig{{
					ClientID: clientID, ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: delegateSecretName, Key: "secret"},
					Scopes: []string{"profile"}, Audiences: []string{issuer},
				}},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{Name: "dex", Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
					OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{AuthorizationEndpoint: dexInfo.InClusterBaseURL + "/auth", TokenEndpoint: dexInfo.InClusterBaseURL + "/token", ClientID: "vmcp-authserver", Scopes: []string{"openid", "profile", "email", "offline_access"}, ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: dexClientSecretName, Key: "client-secret"}, InsecureAllowHTTP: true, AllowPrivateIPs: true},
				}},
			}),
		))).To(gomega.Succeed())
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace, mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollInterval)
	})

	ginkgo.AfterAll(func() {
		deleteFixture := func(err error) {
			gomega.Expect(err == nil || apierrors.IsNotFound(err)).To(gomega.BeTrue())
		}

		vmcp := v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace)
		deleteFixture(k8sClient.Delete(ctx, vmcp))
		WaitForObjectDeletion(ctx, k8sClient, vmcp, timeout, pollInterval)

		server := v1beta1test.NewMCPServer(backendName, defaultNamespace)
		deleteFixture(k8sClient.Delete(ctx, server))
		WaitForObjectDeletion(ctx, k8sClient, server, timeout, pollInterval)

		group := &mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: groupName, Namespace: defaultNamespace}}
		deleteFixture(k8sClient.Delete(ctx, group))
		WaitForObjectDeletion(ctx, k8sClient, group, timeout, pollInterval)

		oidcConfig := &mcpv1beta1.MCPOIDCConfig{ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace}}
		deleteFixture(k8sClient.Delete(ctx, oidcConfig))
		for _, name := range []string{delegateSecretName, dexClientSecretName, hmacSecretName, signingKeySecretName} {
			deleteFixture(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace}}))
		}
		if dexCleanup != nil {
			dexCleanup()
		}
	})

	ginkgo.It("exchanges a Dex-backed subject token", func() {
		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()
		subjectToken, err := getEmbeddedASToken(localURL, dexInfo.LocalURL,
			fmt.Sprintf("%s.%s.svc.cluster.local:5556", dexName, defaultNamespace), vmcpHost, issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		request := tokenExchangeRequest(localURL, subjectToken, issuer)
		request.SetBasicAuth(clientID, clientSecret)
		accessToken := exchangeToken(request)
		gomega.Expect(accessToken).NotTo(gomega.BeEmpty())
	})
})
