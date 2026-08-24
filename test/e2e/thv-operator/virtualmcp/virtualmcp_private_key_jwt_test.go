// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"bytes"
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

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This test exercises private_key_jwt DCR and authentication through the real
// operator-created embedded authorization server. The external issuer's
// wildcard delegation consent is intentional: this test is about binding the
// delegated token to the dynamically registered client, not static allowlists.
var _ = ginkgo.Describe("VirtualMCPServer private_key_jwt DCR delegation", ginkgo.Ordered, func() {
	const (
		timeout      = 5 * time.Minute
		pollInterval = 2 * time.Second
		externalSub  = "private-key-jwt-external-subject"
	)

	var (
		backendName, dexName, groupName, oidcName                 string
		hmacSecretName, signingKeySecretName, dexClientSecretName string
		vmcpName, oidcConfigName, issuer                          string
		dexInfo                                                   *DexInfo
		oidcIssuer                                                string
		oidcLocalPort                                             int
		cleanupDexFn, oidcCleanupFn, oidcPortForwardCleanup       func()
		clientKey                                                 *rsa.PrivateKey
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-pkjwt-backend-" + suffix
		dexName = "e2e-pkjwt-dex-" + suffix
		groupName = "e2e-pkjwt-group-" + suffix
		oidcName = "e2e-pkjwt-oidc-" + suffix
		hmacSecretName = "e2e-pkjwt-hmac-" + suffix
		signingKeySecretName = "e2e-pkjwt-key-" + suffix
		dexClientSecretName = "e2e-pkjwt-dex-secret-" + suffix
		vmcpName = "e2e-pkjwt-vmcp-" + suffix
		oidcConfigName = "e2e-pkjwt-oidccfg-" + suffix
		issuer = fmt.Sprintf("https://vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)

		privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clientKey = privateKey
		hmac := make([]byte, 32)
		_, err = rand.Read(hmac)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: signingKeySecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"private-key": pemEncodePKCS1(privateKey)},
		})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: hmacSecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"hmac": hmac},
		})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: dexClientSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"client-secret": "authserver-secret"},
		})).To(gomega.Succeed())

		dexInfo, cleanupDexFn = deployDex(ctx, k8sClient, dexName, issuer+"/oauth/callback")
		oidcIssuer, _, oidcCleanupFn = DeployParameterizedOIDCServer(ctx, k8sClient, oidcName, defaultNamespace, timeout, pollInterval)
		oidcLocalPort, oidcPortForwardCleanup, err = startRateLimitServicePortForward(oidcName, 80)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: issuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())
		CreateMCPGroupAndWait(ctx, k8sClient, groupName, defaultNamespace, "private_key_jwt e2e group", timeout, pollInterval)
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewMCPServer(backendName, defaultNamespace,
			v1beta1test.WithImage(images.YardstickServerImage), v1beta1test.WithTransport("streamable-http"),
			v1beta1test.WithProxyPort(8080), v1beta1test.WithMCPPort(8080), v1beta1test.WithMCPGroupRef(groupName),
		))).To(gomega.Succeed())
		gomega.Eventually(func() mcpv1beta1.MCPServerPhase {
			server := &mcpv1beta1.MCPServer{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendName, Namespace: defaultNamespace}, server)).To(gomega.Succeed())
			return server.Status.Phase
		}, timeout, pollInterval).Should(gomega.Equal(mcpv1beta1.MCPServerPhaseReady))

		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: issuer, ResourceURL: issuer}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: issuer, AllowPrivateKeyJWTRegistration: true,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{Name: "dex", Type: mcpv1beta1.UpstreamProviderTypeOAuth2, OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
					AuthorizationEndpoint: dexInfo.InClusterBaseURL + "/auth", TokenEndpoint: dexInfo.InClusterBaseURL + "/token", ClientID: "vmcp-authserver",
					Scopes: []string{"openid", "profile", "email", "offline_access"}, ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: dexClientSecretName, Key: "client-secret"}, InsecureAllowHTTP: true, AllowPrivateIPs: true,
				}}},
				TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{IssuerURL: oidcIssuer, ExpectedAudience: issuer, AllowedDelegateClients: []string{"*"}, ActorMatcher: "true", JWKSURL: oidcIssuer + "/jwks", InsecureAllowHTTP: true, AllowPrivateIPs: true}},
			}),
		))).To(gomega.Succeed())
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace, mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollInterval)
	})

	ginkgo.AfterAll(func() {
		_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace))
		_ = k8sClient.Delete(ctx, v1beta1test.NewMCPServer(backendName, defaultNamespace))
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: groupName, Namespace: defaultNamespace}})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace}})
		for _, name := range []string{hmacSecretName, signingKeySecretName, dexClientSecretName} {
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
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, &mcpv1beta1.VirtualMCPServer{}))
		}, timeout, pollInterval).Should(gomega.BeTrue())
	})

	ginkgo.It("registers and exchanges with a private_key_jwt client, then rejects assertion replay", func() {
		port, cleanup, err := startRateLimitServicePortForward("vmcp-"+vmcpName, 4483)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()
		endpoint := fmt.Sprintf("http://localhost:%d", port)

		registered := registerPrivateKeyJWTClient(endpoint, clientKey)
		subjectToken := mintExternalSubjectTokenWithExtraClaim(oidcLocalPort, externalSub, issuer, "azp", "external-agent")
		assertion := signPrivateKeyJWTAssertion(clientKey, registered.ClientID, issuer+"/oauth/token", "private-key-jwt-assertion-1")
		status, body := requestPrivateKeyJWTExchange(endpoint, registered.ClientID, assertion, subjectToken, issuer)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))
		var token struct {
			AccessToken string `json:"access_token"`
		}
		gomega.Expect(json.Unmarshal(body, &token)).To(gomega.Succeed())
		gomega.Expect(token.AccessToken).NotTo(gomega.BeEmpty())
		claims := decodeUnverifiedJWTClaims(token.AccessToken)
		act, ok := claims["act"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(act["sub"]).To(gomega.Equal(registered.ClientID))

		status, body = requestPrivateKeyJWTExchange(endpoint, registered.ClientID, assertion, subjectToken, issuer)
		gomega.Expect(status).NotTo(gomega.Equal(http.StatusOK), "a replayed client assertion must not issue a token: %s", body)
	})
})

func registerPrivateKeyJWTClient(endpoint string, privateKey *rsa.PrivateKey) oauthproto.DynamicClientRegistrationResponse {
	jwks := &josev3.JSONWebKeySet{Keys: []josev3.JSONWebKey{{Key: privateKey.Public(), KeyID: "dcr-client-key", Algorithm: "RS256", Use: "sig"}}}
	requestBody, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://localhost:19999/callback"}, TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		TokenEndpointAuthSigningAlg: "RS256", GrantTypes: []string{oauthproto.GrantTypeTokenExchange}, JWKS: jwks,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	request, err := http.NewRequest(http.MethodPost, endpoint+"/oauth/register", bytes.NewReader(requestBody))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer response.Body.Close()
	var registered oauthproto.DynamicClientRegistrationResponse
	gomega.Expect(json.NewDecoder(response.Body).Decode(&registered)).To(gomega.Succeed())
	gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusCreated))
	gomega.Expect(registered.ClientID).NotTo(gomega.BeEmpty())
	gomega.Expect(registered.ClientSecret).To(gomega.BeEmpty())
	return registered
}

func signPrivateKeyJWTAssertion(privateKey *rsa.PrivateKey, clientID, audience, jti string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: privateKey}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "dcr-client-key"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	now := time.Now()
	assertion, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: clientID, Subject: clientID, Audience: jwt.Audience{audience}, Expiry: jwt.NewNumericDate(now.Add(2 * time.Minute)), IssuedAt: jwt.NewNumericDate(now), ID: jti}).Serialize()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return assertion
}

func requestPrivateKeyJWTExchange(endpoint, clientID, assertion, subjectToken, audience string) (int, []byte) {
	form := url.Values{"grant_type": {oauthproto.GrantTypeTokenExchange}, "subject_token": {subjectToken}, "subject_token_type": {oauthproto.TokenTypeJWT}, "audience": {audience}, "client_id": {clientID}, "client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}, "client_assertion": {assertion}}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/oauth/token", strings.NewReader(form.Encode()))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return response.StatusCode, body
}

func pemEncodePKCS1(privateKey *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
}
