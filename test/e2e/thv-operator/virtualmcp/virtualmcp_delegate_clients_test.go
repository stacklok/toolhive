// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = ginkgo.Describe("VirtualMCPServer delegate clients", ginkgo.Ordered, func() {
	const (
		timeout      = 5 * time.Minute
		pollInterval = 2 * time.Second
		clientID     = "e2e-delegate-client"
		clientSecret = "e2e-delegate-client-secret"
	)

	var (
		backendName, delegateSecretName, dexClientSecretName string
		dexName, groupName, hmacSecretName, oidcConfigName   string
		signingKeySecretName, vmcpName, vmcpHost             string
		cleanupDexFn                                         func()
		dexInfo                                              *DexInfo
		dexCleanup                                           func()
		issuer                                               string
		signingPublicKey                                     *rsa.PublicKey
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-delegate-backend-" + suffix
		delegateSecretName = "e2e-delegate-secret-" + suffix
		dexClientSecretName = "e2e-delegate-dex-secret-" + suffix
		dexName = "e2e-delegate-dex-" + suffix
		groupName = "e2e-delegate-group-" + suffix
		hmacSecretName = "e2e-delegate-hmac-" + suffix
		oidcConfigName = "e2e-delegate-oidc-" + suffix
		signingKeySecretName = "e2e-delegate-key-" + suffix
		vmcpName = "e2e-delegate-vmcp-" + suffix

		ginkgo.By("creating the namespace-local delegate client secret")
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: delegateSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"secret": clientSecret},
		})).To(gomega.Succeed())

		privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		signingPublicKey = &privateKey.PublicKey
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
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: dexClientSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"client-secret": "authserver-secret"},
		})).To(gomega.Succeed())

		vmcpHost = fmt.Sprintf("vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)
		issuer = "https://" + vmcpHost
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: issuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())

		ginkgo.By("deploying Dex as the embedded authorization server upstream")
		dexInfo, dexCleanup = deployDex(ctx, k8sClient, dexName, defaultNamespace,
			issuer+"/oauth/callback", timeout, pollInterval)
		cleanupDexFn = dexCleanup

		CreateMCPGroupAndWait(ctx, k8sClient, groupName, defaultNamespace, "delegate client e2e group", timeout, pollInterval)
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

		ginkgo.By("creating the VirtualMCPServer with an HTTPS logical issuer and delegate client")
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigName, Audience: issuer, ResourceURL: issuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:                              issuer,
				AllowConfidentialClientRegistration: false,
				SigningKeySecretRefs:                []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:                      []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
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
		_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace))
		_ = k8sClient.Delete(ctx, v1beta1test.NewMCPServer(backendName, defaultNamespace))
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: groupName, Namespace: defaultNamespace}})
		for _, name := range []string{delegateSecretName, dexClientSecretName, hmacSecretName, signingKeySecretName} {
			_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace}})
		}
		if cleanupDexFn != nil {
			cleanupDexFn()
		}
		gomega.Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{}))
		}, timeout, pollInterval).Should(gomega.BeTrue())
	})

	ginkgo.It("keeps the secret out of the ConfigMap and exchanges a self-issued subject token", func() {
		ginkgo.By("verifying the generated ConfigMap contains only the reserved environment reference")
		configMap := &corev1.ConfigMap{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName + "-vmcp-config", Namespace: defaultNamespace}, configMap)).To(gomega.Succeed())
		authConfig := configMap.Data["authserver-config.yaml"]
		gomega.Expect(authConfig).To(gomega.ContainSubstring("TOOLHIVE_DELEGATE_CLIENT_SECRET_0"))
		gomega.Expect(authConfig).NotTo(gomega.ContainSubstring("allow_confidential_client_registration: true"))
		gomega.Expect(authConfig).NotTo(gomega.ContainSubstring(delegateSecretName))
		gomega.Expect(authConfig).NotTo(gomega.ContainSubstring(clientSecret))

		ginkgo.By("verifying the Deployment injects the delegate secret through SecretKeyRef")
		deployment := &appsv1.Deployment{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, deployment)).To(gomega.Succeed())
		var injected *corev1.EnvVar
		for i := range deployment.Spec.Template.Spec.Containers[0].Env {
			env := &deployment.Spec.Template.Spec.Containers[0].Env[i]
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == delegateSecretName && env.ValueFrom.SecretKeyRef.Key == "secret" {
				injected = env
				break
			}
		}
		gomega.Expect(injected).NotTo(gomega.BeNil())
		gomega.Expect(injected.Value).To(gomega.BeEmpty())

		port, cleanup, err := startRateLimitServicePortForward("vmcp-"+vmcpName, 4483)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()
		localURL := fmt.Sprintf("http://localhost:%d", port)
		// The issuer remains HTTPS so the shared API permits confidential delegate clients.
		// Port-forward is an existing local test transport to the pod's HTTP listener.
		subjectToken, err := getEmbeddedASToken(
			localURL,
			dexInfo.LocalURL,
			fmt.Sprintf("%s.%s.svc.cluster.local:5556", dexName, defaultNamespace),
			vmcpHost,
			issuer,
		)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		exchanged := exchangeDelegateToken(localURL, subjectToken, issuer, clientSecret)
		claims := verifiedJWTClaims(exchanged, signingPublicKey)
		gomega.Expect(claims["iss"]).To(gomega.Equal(issuer))
		gomega.Expect(claims["aud"]).To(gomega.Equal([]any{issuer}))
		gomega.Expect(claims["sub"]).NotTo(gomega.BeEmpty())
		act, ok := claims["act"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(act["sub"]).To(gomega.Equal(clientID))

		request := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "subject_token": {subjectToken}, "subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"}, "audience": {issuer}}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, localURL+"/oauth/token", strings.NewReader(request.Encode()))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(clientID, "wrong-"+clientSecret)
		response, err := http.DefaultClient.Do(req)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer response.Body.Close()
		gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusUnauthorized))
	})
})

func exchangeDelegateToken(endpoint, subjectToken, audience, secret string) string {
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "subject_token": {subjectToken}, "subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"}, "audience": {audience}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/oauth/token", strings.NewReader(form.Encode()))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("e2e-delegate-client", secret)
	response, err := http.DefaultClient.Do(req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(response.Body)
		gomega.Expect(readErr).NotTo(gomega.HaveOccurred())
		gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusOK), string(body))
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	gomega.Expect(json.NewDecoder(response.Body).Decode(&token)).To(gomega.Succeed())
	gomega.Expect(token.AccessToken).NotTo(gomega.BeEmpty())
	return token.AccessToken
}

func unverifiedJWTClaims(token string) map[string]any {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	claims := map[string]any{}
	gomega.Expect(parsed.UnsafeClaimsWithoutVerification(&claims)).To(gomega.Succeed())
	return claims
}

func verifiedJWTClaims(token string, signingKey *rsa.PublicKey) map[string]any {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	claims := map[string]any{}
	gomega.Expect(parsed.Claims(signingKey, &claims)).To(gomega.Succeed())
	return claims
}
