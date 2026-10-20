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
		clientSecret = "e2e-delegate-client-secret-testing"
	)

	var (
		backendName, delegateSecretName           string
		groupName, hmacSecretName, oidcConfigName string
		signingKeySecretName, vmcpName, vmcpHost  string
		issuer                                    string
		signingPublicKey                          *rsa.PublicKey
		signingPrivateKey                         *rsa.PrivateKey
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-delegate-backend-" + suffix
		delegateSecretName = "e2e-delegate-secret-" + suffix
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
		signingPrivateKey = privateKey
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

		vmcpHost = fmt.Sprintf("vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)
		issuer = "https://" + vmcpHost
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: issuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())

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

		ginkgo.By("creating the zero-upstream VirtualMCPServer with a delegate client")
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigName, Audience: issuer, ResourceURL: issuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: issuer, AllowConfidentialClientRegistration: false,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				DelegateClients: []mcpv1beta1.DelegateClientConfig{{
					ClientID: clientID, ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: delegateSecretName, Key: "secret"},
					Scopes: []string{"profile"}, Audiences: []string{issuer},
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
		for _, name := range []string{delegateSecretName, hmacSecretName, signingKeySecretName} {
			deleteFixture(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace}}))
		}
	})

	ginkgo.It("keeps the secret out of the ConfigMap and exchanges a zero-upstream subject token", func() {
		configMap := &corev1.ConfigMap{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName + "-vmcp-config", Namespace: defaultNamespace}, configMap)).To(gomega.Succeed())
		authConfig := configMap.Data["authserver-config.yaml"]
		gomega.Expect(authConfig).To(gomega.ContainSubstring("TOOLHIVE_DELEGATE_CLIENT_SECRET_0"))
		gomega.Expect(authConfig).NotTo(gomega.ContainSubstring("allow_confidential_client_registration: true"))
		gomega.Expect(authConfig).NotTo(gomega.ContainSubstring(delegateSecretName))
		gomega.Expect(authConfig).NotTo(gomega.ContainSubstring(clientSecret))

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

		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()
		subjectToken := signSelfIssuedSubjectToken(signingPrivateKey, issuer, "zero-upstream-client", "zero-upstream-subject")

		exchanged := exchangeDelegateToken(localURL, subjectToken, issuer, clientSecret)
		claims := verifiedJWTClaims(exchanged, signingPublicKey)
		gomega.Expect(claims["iss"]).To(gomega.Equal(issuer))
		gomega.Expect(claims["aud"]).To(gomega.Equal([]any{issuer}))
		gomega.Expect(claims["sub"]).To(gomega.Equal("zero-upstream-subject"))
		gomega.Expect(claims["act"].(map[string]any)["sub"]).To(gomega.Equal(clientID))

		ginkgo.By("returning an OAuth error from the retained authorize route")
		response, err := http.Get(localURL + "/oauth/authorize")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer response.Body.Close()
		var authorizeError struct {
			Error string `json:"error"`
		}
		gomega.Expect(json.NewDecoder(response.Body).Decode(&authorizeError)).To(gomega.Succeed())
		gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusUnauthorized))
		gomega.Expect(authorizeError.Error).To(gomega.Equal("invalid_client"))

		request := tokenExchangeRequest(localURL, subjectToken, issuer)
		request.SetBasicAuth(clientID, "wrong-"+clientSecret)
		response, err = http.DefaultClient.Do(request)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer response.Body.Close()
		gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusUnauthorized))
	})

	ginkgo.It("resolves actor identity from a self-issued actor token with a zero-upstream subject token", func() {
		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()
		subjectToken := signSelfIssuedSubjectToken(signingPrivateKey, issuer, "zero-upstream-client", "zero-upstream-subject")

		actorToken := signSelfIssuedActorToken(signingPrivateKey, issuer, clientID, clientID)
		claims := verifiedJWTClaims(exchangeDelegateTokenWithActor(localURL, subjectToken, actorToken, issuer, clientSecret), signingPublicKey)
		gomega.Expect(claims["act"].(map[string]any)["sub"]).To(gomega.Equal(clientID))

		mismatchedActorToken := signSelfIssuedActorToken(signingPrivateKey, issuer, "someone-elses-client", "someone-else")
		form := url.Values{
			"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
			"subject_token":      {subjectToken},
			"subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
			"actor_token":        {mismatchedActorToken},
			"actor_token_type":   {"urn:ietf:params:oauth:token-type:jwt"},
			"audience":           {issuer},
		}
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, localURL+"/oauth/token", strings.NewReader(form.Encode()))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.SetBasicAuth(clientID, clientSecret)
		response, err := http.DefaultClient.Do(request)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer func() { _, _ = io.Copy(io.Discard, response.Body); _ = response.Body.Close() }()
		gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusBadRequest))
	})
})

func portForwardDelegateAuthServer(vmcpName string) (string, func()) {
	port, cleanup, err := startRateLimitServicePortForward("vmcp-"+vmcpName, 4483)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return fmt.Sprintf("http://localhost:%d", port), cleanup
}

func tokenExchangeRequest(endpoint, subjectToken, audience string) *http.Request {
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "subject_token": {subjectToken}, "subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"}, "audience": {audience}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/oauth/token", strings.NewReader(form.Encode()))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func exchangeDelegateToken(endpoint, subjectToken, audience, secret string) string {
	request := tokenExchangeRequest(endpoint, subjectToken, audience)
	request.SetBasicAuth("e2e-delegate-client", secret)
	return exchangeToken(request)
}

func exchangeDelegateTokenWithActor(endpoint, subjectToken, actorToken, audience, secret string) string {
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "subject_token": {subjectToken}, "subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"}, "actor_token": {actorToken}, "actor_token_type": {"urn:ietf:params:oauth:token-type:jwt"}, "audience": {audience}}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/oauth/token", strings.NewReader(form.Encode()))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth("e2e-delegate-client", secret)
	return exchangeToken(request)
}

func exchangeToken(request *http.Request) string {
	response, err := http.DefaultClient.Do(request)
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

func signSelfIssuedSubjectToken(privateKey *rsa.PrivateKey, issuer, tokenClientID, sub string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: privateKey}, (&jose.SignerOptions{}).WithType("JWT"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: sub, Audience: jwt.Audience{issuer}, Expiry: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now)}).Claims(map[string]any{"client_id": tokenClientID, "scope": "profile"}).Serialize()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return token
}

func signSelfIssuedActorToken(privateKey *rsa.PrivateKey, issuer, tokenClientID, sub string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: privateKey}, (&jose.SignerOptions{}).WithType("JWT"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: sub, Audience: jwt.Audience{issuer}, Expiry: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now)}).Claims(map[string]any{"client_id": tokenClientID}).Serialize()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return token
}

func verifiedJWTClaims(token string, signingKey *rsa.PublicKey) map[string]any {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	claims := map[string]any{}
	gomega.Expect(parsed.Claims(signingKey, &claims)).To(gomega.Succeed())
	return claims
}
