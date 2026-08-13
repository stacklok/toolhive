// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// This test proves that TrustedIssuerConfig.AllowMayAct is reachable and
// enforced through the CRD/operator surface: a VirtualMCPServer with
// spec.authServerConfig.trustedIssuers[].allowMayAct unset (the default)
// rejects a may_act-bearing external subject token, and one with it set to
// true accepts the same shaped token and produces a delegated token whose
// "act" claim names the delegate client.
var _ = ginkgo.Describe("VirtualMCPServer trusted issuer allowMayAct", ginkgo.Ordered, func() {
	const (
		timeout      = 5 * time.Minute
		pollInterval = 2 * time.Second
		clientID     = "e2e-mayact-delegate-client"
		clientSecret = "e2e-mayact-delegate-client-secret-testing" // 35 chars, above the 32-char minimum
		externalSub  = "external-agent"
	)

	var (
		backendName, delegateSecretName, dexClientSecretName string
		dexName, groupName, hmacSecretName                   string
		signingKeySecretName, oidcName                       string
		cleanupDexFn, oidcCleanupFn                          func()
		dexInfo                                              *DexInfo
		oidcIssuer                                           string
		oidcLocalPort                                        int
		oidcPortForwardCleanup                               func()

		// One VirtualMCPServer per AllowMayAct value under test.
		vmcpDeniedName, vmcpDeniedIssuer            string
		vmcpAllowedName, vmcpAllowedIssuer          string
		oidcConfigDeniedName, oidcConfigAllowedName string
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-mayact-backend-" + suffix
		delegateSecretName = "e2e-mayact-secret-" + suffix
		dexClientSecretName = "e2e-mayact-dex-secret-" + suffix
		dexName = "e2e-mayact-dex-" + suffix
		groupName = "e2e-mayact-group-" + suffix
		hmacSecretName = "e2e-mayact-hmac-" + suffix
		oidcName = "e2e-mayact-oidc-" + suffix
		signingKeySecretName = "e2e-mayact-key-" + suffix
		vmcpDeniedName = "e2e-mayact-denied-" + suffix
		vmcpAllowedName = "e2e-mayact-allowed-" + suffix
		oidcConfigDeniedName = "e2e-mayact-oidccfg-denied-" + suffix
		oidcConfigAllowedName = "e2e-mayact-oidccfg-allowed-" + suffix

		vmcpDeniedIssuer = fmt.Sprintf("https://vmcp-%s.%s.svc.cluster.local:4483", vmcpDeniedName, defaultNamespace)
		vmcpAllowedIssuer = fmt.Sprintf("https://vmcp-%s.%s.svc.cluster.local:4483", vmcpAllowedName, defaultNamespace)

		ginkgo.By("creating shared secrets (delegate client secret, signing key, HMAC, Dex client secret)")
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: delegateSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"secret": clientSecret},
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
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: dexClientSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"client-secret": "authserver-secret"},
		})).To(gomega.Succeed())

		ginkgo.By("deploying Dex as the (unexercised) embedded authorization server upstream")
		dexInfo, cleanupDexFn = deployDex(ctx, k8sClient, dexName, defaultNamespace,
			vmcpDeniedIssuer+"/oauth/callback", timeout, pollInterval)

		ginkgo.By("deploying the parameterized OIDC server as the trusted external issuer")
		oidcIssuer, _, oidcCleanupFn = DeployParameterizedOIDCServer(ctx, k8sClient, oidcName, defaultNamespace, timeout, pollInterval)
		oidcLocalPort, oidcPortForwardCleanup, err = startRateLimitServicePortForward(oidcName, 80)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		CreateMCPGroupAndWait(ctx, k8sClient, groupName, defaultNamespace, "trusted issuer mayact e2e group", timeout, pollInterval)
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

		upstream := mcpv1beta1.UpstreamProviderConfig{
			Name: "dex", Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
			OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
				AuthorizationEndpoint: dexInfo.InClusterBaseURL + "/auth",
				TokenEndpoint:         dexInfo.InClusterBaseURL + "/token",
				ClientID:              "vmcp-authserver",
				Scopes:                []string{"openid", "profile", "email", "offline_access"},
				ClientSecretRef:       &mcpv1beta1.SecretKeyRef{Name: dexClientSecretName, Key: "client-secret"},
				InsecureAllowHTTP:     true,
				AllowPrivateIPs:       true,
			},
		}
		delegate := mcpv1beta1.DelegateClientConfig{
			ClientID: clientID, ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: delegateSecretName, Key: "secret"},
			Scopes: []string{"profile"},
		}

		ginkgo.By("creating the MCPOIDCConfig and VirtualMCPServer with allowMayAct=false (the default)")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigDeniedName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: vmcpDeniedIssuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())
		delegate.Audiences = []string{vmcpDeniedIssuer}
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpDeniedName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigDeniedName, Audience: vmcpDeniedIssuer, ResourceURL: vmcpDeniedIssuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               vmcpDeniedIssuer,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				DelegateClients:      []mcpv1beta1.DelegateClientConfig{delegate},
				UpstreamProviders:    []mcpv1beta1.UpstreamProviderConfig{upstream},
				TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{
					IssuerURL:              oidcIssuer,
					ExpectedAudience:       vmcpDeniedIssuer,
					AllowedDelegateClients: []string{clientID},
					JWKSURL:                oidcIssuer + "/jwks",
					InsecureAllowHTTP:      true,
					AllowPrivateIPs:        true,
					// AllowMayAct intentionally omitted: defaults to false.
				}},
			}),
		))).To(gomega.Succeed())

		ginkgo.By("creating the MCPOIDCConfig and VirtualMCPServer with allowMayAct=true")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigAllowedName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: vmcpAllowedIssuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())
		delegate.Audiences = []string{vmcpAllowedIssuer}
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpAllowedName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigAllowedName, Audience: vmcpAllowedIssuer, ResourceURL: vmcpAllowedIssuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               vmcpAllowedIssuer,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				DelegateClients:      []mcpv1beta1.DelegateClientConfig{delegate},
				UpstreamProviders:    []mcpv1beta1.UpstreamProviderConfig{upstream},
				TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{
					IssuerURL:              oidcIssuer,
					ExpectedAudience:       vmcpAllowedIssuer,
					AllowedDelegateClients: []string{clientID},
					JWKSURL:                oidcIssuer + "/jwks",
					InsecureAllowHTTP:      true,
					AllowPrivateIPs:        true,
					AllowMayAct:            true,
				}},
			}),
		))).To(gomega.Succeed())

		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpDeniedName, defaultNamespace, timeout, pollInterval)
		WaitForCondition(ctx, k8sClient, vmcpDeniedName, defaultNamespace, mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollInterval)
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpAllowedName, defaultNamespace, timeout, pollInterval)
		WaitForCondition(ctx, k8sClient, vmcpAllowedName, defaultNamespace, mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollInterval)
	})

	ginkgo.AfterAll(func() {
		_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpDeniedName, defaultNamespace))
		_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpAllowedName, defaultNamespace))
		_ = k8sClient.Delete(ctx, v1beta1test.NewMCPServer(backendName, defaultNamespace))
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: groupName, Namespace: defaultNamespace}})
		for _, name := range []string{delegateSecretName, dexClientSecretName, hmacSecretName, signingKeySecretName} {
			_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace}})
		}
		if oidcPortForwardCleanup != nil {
			oidcPortForwardCleanup()
		}
		if oidcCleanupFn != nil {
			oidcCleanupFn()
		}
		if cleanupDexFn != nil {
			cleanupDexFn()
		}
		gomega.Eventually(func() bool {
			deniedGone := apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpDeniedName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{}))
			allowedGone := apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpAllowedName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{}))
			return deniedGone && allowedGone
		}, timeout, pollInterval).Should(gomega.BeTrue())
	})

	ginkgo.It("rejects a may_act subject token when allowMayAct is unset (the default)", func() {
		port, cleanup, err := startRateLimitServicePortForward("vmcp-"+vmcpDeniedName, 4483)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()
		localURL := fmt.Sprintf("http://localhost:%d", port)

		subjectToken := mintExternalSubjectToken(oidcLocalPort, externalSub, vmcpDeniedIssuer, clientID, vmcpDeniedIssuer)
		status, body := exchangeExternalSubjectToken(localURL, subjectToken, vmcpDeniedIssuer, clientID, clientSecret)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))

		var tokenError struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		gomega.Expect(json.Unmarshal(body, &tokenError)).To(gomega.Succeed())
		gomega.Expect(tokenError.Error).To(gomega.Equal("invalid_request"))
		gomega.Expect(tokenError.ErrorDescription).To(gomega.ContainSubstring("invalid or could not be verified"))
	})

	ginkgo.It("accepts a may_act subject token and delegates when allowMayAct is true", func() {
		port, cleanup, err := startRateLimitServicePortForward("vmcp-"+vmcpAllowedName, 4483)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()
		localURL := fmt.Sprintf("http://localhost:%d", port)

		subjectToken := mintExternalSubjectToken(oidcLocalPort, externalSub, vmcpAllowedIssuer, clientID, vmcpAllowedIssuer)
		status, body := exchangeExternalSubjectToken(localURL, subjectToken, vmcpAllowedIssuer, clientID, clientSecret)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))

		var token struct {
			AccessToken string `json:"access_token"`
		}
		gomega.Expect(json.Unmarshal(body, &token)).To(gomega.Succeed())
		gomega.Expect(token.AccessToken).NotTo(gomega.BeEmpty())

		// Decode without verification: the point of this assertion is only to
		// confirm the delegated token's "act" claim names the delegate client
		// that presented the may_act-bearing subject token, proving the
		// exchange actually delegated rather than merely returning 200.
		parts := strings.Split(token.AccessToken, ".")
		gomega.Expect(parts).To(gomega.HaveLen(3))
		payload, err := jwtBase64URLDecode(parts[1])
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var claims struct {
			Act struct {
				Sub string `json:"sub"`
			} `json:"act"`
		}
		gomega.Expect(json.Unmarshal(payload, &claims)).To(gomega.Succeed())
		gomega.Expect(claims.Act.Sub).To(gomega.Equal(clientID))
	})
})

// mintExternalSubjectToken requests a may_act-bearing JWT from the
// parameterized OIDC server (reachable at localhost:oidcLocalPort via
// port-forward), acting as the trusted external issuer. issuer is used both
// as the token's "aud" (matching TrustedIssuerConfig.ExpectedAudience) and as
// the may_act claim's "iss" (the authorization server's own issuer, required
// on the external path — see validateMayActShape).
func mintExternalSubjectToken(oidcLocalPort int, subject, audience, mayActClientID, mayActIssuer string) string {
	reqURL := fmt.Sprintf("http://localhost:%d/token?subject=%s&aud=%s&may_act_sub=%s&may_act_iss=%s",
		oidcLocalPort, url.QueryEscape(subject), url.QueryEscape(audience),
		url.QueryEscape(mayActClientID), url.QueryEscape(mayActIssuer))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, reqURL, nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer resp.Body.Close()
	gomega.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK))
	var body struct {
		AccessToken string `json:"access_token"`
	}
	gomega.Expect(json.NewDecoder(resp.Body).Decode(&body)).To(gomega.Succeed())
	gomega.Expect(body.AccessToken).NotTo(gomega.BeEmpty())
	return body.AccessToken
}

// exchangeExternalSubjectToken performs an RFC 8693 token-exchange request
// against endpoint's /oauth/token, authenticating as the delegate client.
// Returns the raw HTTP status and body so both success and failure paths can
// be asserted without the helper deciding what "success" means.
func exchangeExternalSubjectToken(endpoint, subjectToken, audience, clientID, clientSecret string) (int, []byte) {
	form := url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":      {subjectToken},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
		"audience":           {audience},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/oauth/token", strings.NewReader(form.Encode()))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)
	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return resp.StatusCode, body
}

// jwtBase64URLDecode decodes an unpadded base64url JWT segment.
func jwtBase64URLDecode(segment string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(segment)
}
