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

// This test proves that TrustedIssuerConfig.ActorMatcher is reachable and
// enforced through the CRD/operator surface: a VirtualMCPServer whose
// trustedIssuers entry has NO allowedActors and only an actorMatcher CEL
// expression still authorizes a matching subject token, and a VirtualMCPServer
// whose actorMatcher does not match the token's claims still rejects it —
// deliberately keyed on "appid" rather than the default ActorClaim ("azp"),
// proving the matcher evaluates the token's complete claims map rather than
// re-checking whatever the allowlist path already looks at.
var _ = ginkgo.Describe("VirtualMCPServer trusted issuer actorMatcher", ginkgo.Ordered, func() {
	const (
		timeout          = 5 * time.Minute
		pollInterval     = 2 * time.Second
		clientID         = "e2e-actormatcher-delegate-client"
		clientSecret     = "e2e-actormatcher-delegate-client-secret-testing" // above the 32-char minimum
		externalSub      = "external-agent"
		matchingAppID    = "trusted-app"
		mismatchedAppID  = "some-other-app"
		actorClaimTarget = "appid"
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

		// One VirtualMCPServer per actorMatcher outcome under test.
		vmcpMatchedName, vmcpMatchedIssuer              string
		vmcpMismatchedName, vmcpMismatchedIssuer        string
		oidcConfigMatchedName, oidcConfigMismatchedName string
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-actormatcher-backend-" + suffix
		delegateSecretName = "e2e-actormatcher-secret-" + suffix
		dexClientSecretName = "e2e-actormatcher-dex-secret-" + suffix
		dexName = "e2e-actormatcher-dex-" + suffix
		groupName = "e2e-actormatcher-group-" + suffix
		hmacSecretName = "e2e-actormatcher-hmac-" + suffix
		oidcName = "e2e-actormatcher-oidc-" + suffix
		signingKeySecretName = "e2e-actormatcher-key-" + suffix
		vmcpMatchedName = "e2e-actormatcher-matched-" + suffix
		vmcpMismatchedName = "e2e-actormatcher-mismatched-" + suffix
		oidcConfigMatchedName = "e2e-actormatcher-oidccfg-matched-" + suffix
		oidcConfigMismatchedName = "e2e-actormatcher-oidccfg-mismatched-" + suffix

		vmcpMatchedIssuer = fmt.Sprintf("https://vmcp-%s.%s.svc.cluster.local:4483", vmcpMatchedName, defaultNamespace)
		vmcpMismatchedIssuer = fmt.Sprintf("https://vmcp-%s.%s.svc.cluster.local:4483", vmcpMismatchedName, defaultNamespace)

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
		dexInfo, cleanupDexFn = deployDex(ctx, k8sClient, dexName,
			vmcpMatchedIssuer+"/oauth/callback")

		ginkgo.By("deploying the parameterized OIDC server as the trusted external issuer")
		oidcIssuer, _, oidcCleanupFn = DeployParameterizedOIDCServer(ctx, k8sClient, oidcName, defaultNamespace, timeout, pollInterval)
		oidcLocalPort, oidcPortForwardCleanup, err = startRateLimitServicePortForward(oidcName, 80)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		CreateMCPGroupAndWait(ctx, k8sClient, groupName, defaultNamespace, "trusted issuer actorMatcher e2e group", timeout, pollInterval)
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

		ginkgo.By("creating the MCPOIDCConfig and VirtualMCPServer whose actorMatcher matches the token")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigMatchedName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: vmcpMatchedIssuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())
		delegate.Audiences = []string{vmcpMatchedIssuer}
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpMatchedName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigMatchedName, Audience: vmcpMatchedIssuer, ResourceURL: vmcpMatchedIssuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               vmcpMatchedIssuer,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				DelegateClients:      []mcpv1beta1.DelegateClientConfig{delegate},
				UpstreamProviders:    []mcpv1beta1.UpstreamProviderConfig{upstream},
				TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{
					IssuerURL:              oidcIssuer,
					ExpectedAudience:       vmcpMatchedIssuer,
					AllowedDelegateClients: []string{clientID},
					JWKSURL:                oidcIssuer + "/jwks",
					InsecureAllowHTTP:      true,
					AllowPrivateIPs:        true,
					// AllowedActors intentionally omitted: the matcher is the
					// sole consent signal for this issuer.
					ActorMatcher: fmt.Sprintf("claims.%s == %q", actorClaimTarget, matchingAppID),
				}},
			}),
		))).To(gomega.Succeed())

		ginkgo.By("creating the MCPOIDCConfig and VirtualMCPServer whose actorMatcher does not match")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigMismatchedName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: vmcpMismatchedIssuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())
		delegate.Audiences = []string{vmcpMismatchedIssuer}
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpMismatchedName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigMismatchedName, Audience: vmcpMismatchedIssuer, ResourceURL: vmcpMismatchedIssuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               vmcpMismatchedIssuer,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				DelegateClients:      []mcpv1beta1.DelegateClientConfig{delegate},
				UpstreamProviders:    []mcpv1beta1.UpstreamProviderConfig{upstream},
				TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{
					IssuerURL:              oidcIssuer,
					ExpectedAudience:       vmcpMismatchedIssuer,
					AllowedDelegateClients: []string{clientID},
					JWKSURL:                oidcIssuer + "/jwks",
					InsecureAllowHTTP:      true,
					AllowPrivateIPs:        true,
					// No AllowedActors either: with the matcher false, there
					// is no other consent signal, so this must fail closed.
					ActorMatcher: fmt.Sprintf("claims.%s == %q", actorClaimTarget, mismatchedAppID),
				}},
			}),
		))).To(gomega.Succeed())

		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpMatchedName, defaultNamespace, timeout, pollInterval)
		WaitForCondition(ctx, k8sClient, vmcpMatchedName, defaultNamespace, mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollInterval)
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpMismatchedName, defaultNamespace, timeout, pollInterval)
		WaitForCondition(ctx, k8sClient, vmcpMismatchedName, defaultNamespace, mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollInterval)
	})

	ginkgo.AfterAll(func() {
		_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpMatchedName, defaultNamespace))
		_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpMismatchedName, defaultNamespace))
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
			matchedGone := apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpMatchedName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{}))
			mismatchedGone := apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpMismatchedName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{}))
			return matchedGone && mismatchedGone
		}, timeout, pollInterval).Should(gomega.BeTrue())
	})

	ginkgo.It("grants an exchange with no allowedActors when actorMatcher matches", func() {
		port, cleanup, err := startRateLimitServicePortForward("vmcp-"+vmcpMatchedName, 4483)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()
		localURL := fmt.Sprintf("http://localhost:%d", port)

		subjectToken := mintExternalSubjectTokenWithExtraClaim(
			oidcLocalPort, externalSub, vmcpMatchedIssuer, actorClaimTarget, matchingAppID)
		status, body := exchangeExternalSubjectToken(localURL, subjectToken, vmcpMatchedIssuer, clientID, clientSecret)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))

		var token struct {
			AccessToken string `json:"access_token"`
		}
		gomega.Expect(json.Unmarshal(body, &token)).To(gomega.Succeed())
		gomega.Expect(token.AccessToken).NotTo(gomega.BeEmpty())

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
		gomega.Expect(claims.Act.Sub).To(gomega.Equal(clientID),
			"act.sub must be the ToolHive delegate client, proving genuine delegation rather than a false-positive 200")
	})

	ginkgo.It("rejects the exchange when actorMatcher does not match and there is no allowedActors fallback", func() {
		port, cleanup, err := startRateLimitServicePortForward("vmcp-"+vmcpMismatchedName, 4483)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()
		localURL := fmt.Sprintf("http://localhost:%d", port)

		// This token's appid does not equal mismatchedAppID's matcher target
		// (it carries matchingAppID instead), so the matcher must evaluate
		// false with no other consent signal configured for this issuer.
		subjectToken := mintExternalSubjectTokenWithExtraClaim(
			oidcLocalPort, externalSub, vmcpMismatchedIssuer, actorClaimTarget, matchingAppID)
		status, body := exchangeExternalSubjectToken(localURL, subjectToken, vmcpMismatchedIssuer, clientID, clientSecret)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))

		var tokenError struct {
			Error string `json:"error"`
		}
		gomega.Expect(json.Unmarshal(body, &tokenError)).To(gomega.Succeed())
		gomega.Expect(tokenError.Error).To(gomega.Equal("invalid_request"))
	})
})

// mintExternalSubjectTokenWithExtraClaim requests a JWT from the
// parameterized OIDC server (reachable at localhost:oidcLocalPort via
// port-forward) carrying one arbitrary extra top-level claim, for exercising
// TrustedIssuer.ActorMatcher against a claim other than the default
// ActorClaim ("azp"). audience is used as the token's "aud", matching
// TrustedIssuerConfig.ExpectedAudience.
func mintExternalSubjectTokenWithExtraClaim(oidcLocalPort int, subject, audience, claimName, claimValue string) string {
	reqURL := fmt.Sprintf("http://localhost:%d/token?subject=%s&aud=%s&extra_claim_name=%s&extra_claim_value=%s",
		oidcLocalPort, url.QueryEscape(subject), url.QueryEscape(audience),
		url.QueryEscape(claimName), url.QueryEscape(claimValue))
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
