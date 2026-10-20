// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = ginkgo.Describe("MCPExternalAuthConfig JWT-bearer grant", ginkgo.Ordered, func() {
	const (
		timeout      = 5 * time.Minute
		pollInterval = 2 * time.Second
		externalSub  = "jwt-bearer-external-subject"
	)

	var (
		serverName, authConfigName, dexName, oidcName, oidcConfigName string
		hmacSecretName, signingKeySecretName                          string
		serverIssuer, oidcIssuer, resource                            string
		dexInfo                                                       *DexInfo
		oidcLocalPort                                                 int
		cleanupDexFn, oidcCleanupFn                                   func()
		oidcPortForwardCleanup                                        func()
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		serverName = "e2e-jwt-bearer-server-" + suffix
		authConfigName = "e2e-jwt-bearer-auth-" + suffix
		dexName = "e2e-jwt-bearer-dex-" + suffix
		oidcName = "e2e-jwt-bearer-oidc-" + suffix
		oidcConfigName = "e2e-jwt-bearer-oidccfg-" + suffix
		hmacSecretName = "e2e-jwt-bearer-hmac-" + suffix
		signingKeySecretName = "e2e-jwt-bearer-key-" + suffix
		serverIssuer = fmt.Sprintf("https://mcp-%s-proxy.%s.svc.cluster.local:8080", serverName, defaultNamespace)
		resource = serverIssuer

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

		dexInfo, cleanupDexFn = deployDex(ctx, k8sClient, dexName, serverIssuer+"/oauth/callback")
		oidcIssuer, _, oidcCleanupFn = DeployParameterizedOIDCServer(ctx, k8sClient, oidcName, defaultNamespace, timeout, pollInterval)
		oidcLocalPort, oidcPortForwardCleanup, err = startRateLimitServicePortForward(oidcName, 80)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("creating an MCPOIDCConfig trusting the embedded auth server's own issuer")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{
					// The proxy's incoming-request validator must trust the SAME
					// issuer the embedded auth server signs tokens as (self-issued
					// tokens, per docs/arch/17-token-exchange-delegation.md) — not
					// the external mock OIDC server, which is only the trusted
					// issuer for JWT-bearer assertions below.
					Issuer:             serverIssuer,
					JWKSURL:            serverIssuer + "/.well-known/jwks.json",
					InsecureAllowHTTP:  true,
					JWKSAllowPrivateIP: true,
				},
			},
		})).To(gomega.Succeed())

		ginkgo.By("creating a grant-only trusted issuer through MCPExternalAuthConfig")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer:               serverIssuer,
					SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
					HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
					// A JWT-bearer-derived session has no upstream IdP session to
					// swap in — the caller never went through the Dex OAuth flow —
					// so upstream token injection must be disabled or every request
					// is rejected with "upstream authentication required". The
					// yardstick backend below doesn't require authentication anyway.
					DisableUpstreamTokenInjection: true,
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
						Name: "dex", Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: dexInfo.InClusterBaseURL + "/auth",
							TokenEndpoint:         dexInfo.InClusterBaseURL + "/token",
							ClientID:              "mcp-authserver",
							Scopes:                []string{"openid", "profile", "email", "offline_access"},
							InsecureAllowHTTP:     true,
							AllowPrivateIPs:       true,
						},
					}},
					TrustedIssuers: []mcpv1beta1.TrustedIssuerConfig{{
						IssuerURL:         oidcIssuer,
						JWKSURL:           oidcIssuer + "/jwks",
						InsecureAllowHTTP: true,
						AllowPrivateIPs:   true,
						JWTBearerGrant: &mcpv1beta1.JWTBearerGrantConfig{
							MaxAssertionAge: &metav1.Duration{Duration: time.Minute},
							SubjectBindings: []mcpv1beta1.JWTBearerSubjectBinding{{
								Subject: externalSub, AllowedResources: []string{resource},
							}},
						},
					}},
				},
			},
		})).To(gomega.Succeed())

		ginkgo.By("deploying an MCPServer proxy using the external auth configuration")
		server := v1beta1test.NewMCPServer(serverName, defaultNamespace,
			v1beta1test.WithImage(images.YardstickServerImage),
			v1beta1test.WithTransport("streamable-http"),
			v1beta1test.WithProxyPort(8080),
			v1beta1test.WithMCPPort(8080),
			v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", authConfigName),
		)
		server.Spec.OIDCConfigRef = &mcpv1beta1.MCPOIDCConfigReference{
			Name:        oidcConfigName,
			Audience:    resource,
			ResourceURL: resource,
		}
		gomega.Expect(k8sClient.Create(ctx, server)).To(gomega.Succeed())
		gomega.Eventually(func() mcpv1beta1.MCPServerPhase {
			server := &mcpv1beta1.MCPServer{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: serverName, Namespace: defaultNamespace}, server)).To(gomega.Succeed())
			return server.Status.Phase
		}, timeout, pollInterval).Should(gomega.Equal(mcpv1beta1.MCPServerPhaseReady))
	})

	ginkgo.AfterAll(func() {
		_ = k8sClient.Delete(ctx, v1beta1test.NewMCPServer(serverName, defaultNamespace))
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: defaultNamespace}})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace}})
		for _, name := range []string{hmacSecretName, signingKeySecretName} {
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
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: serverName, Namespace: defaultNamespace}, &mcpv1beta1.MCPServer{}))
		}, timeout, pollInterval).Should(gomega.BeTrue())
	})

	ginkgo.It("issues an access token bound to the exact subject and resource, usable against the proxy", func() {
		port, cleanup, err := startRateLimitServicePortForward("mcp-"+serverName+"-proxy", 8080)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()

		endpoint := fmt.Sprintf("http://localhost:%d", port)

		ginkgo.By("waiting for the proxy's /mcp route to be reachable before minting a short-lived assertion")
		gomega.Eventually(func() error {
			resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(endpoint + "/mcp")
			if err != nil {
				return err
			}
			return resp.Body.Close()
		}, timeout, pollInterval).Should(gomega.Succeed(),
			"the proxy's /mcp route must be reachable before minting an assertion — /health readiness alone "+
				"does not guarantee the backend MCP server behind it has finished registering, and the "+
				"assertion's short expiry must not be spent waiting on that warm-up")

		issuedAt := time.Now()
		assertionExpiry := issuedAt.Add(30 * time.Second)
		assertion := mintJWTBearerAssertion(oidcLocalPort, externalSub, tokenEndpointAudience(serverIssuer), "success-jti", issuedAt)
		status, body := requestJWTBearerGrant(endpoint, assertion, resource)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))
		var response struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		gomega.Expect(json.Unmarshal(body, &response)).To(gomega.Succeed())
		gomega.Expect(response.AccessToken).NotTo(gomega.BeEmpty())

		ginkgo.By("decoding the issued token and checking its audience, expiry, and synthetic identity")
		claims := decodeUnverifiedJWTClaims(response.AccessToken)
		gomega.Expect(claims["aud"]).To(gomega.ConsistOf(resource),
			"the issued token must be scoped to exactly the requested resource")
		gomega.Expect(claims["sub"]).To(gomega.Equal(oidcIssuer+"#"+externalSub),
			"the synthetic subject must bind the assertion issuer and subject, not a bare client identity")
		clientID, ok := claims["client_id"].(string)
		gomega.Expect(ok).To(gomega.BeTrue(), "client_id claim must be a string: %#v", claims["client_id"])
		gomega.Expect(clientID).To(gomega.HavePrefix("synthetic:"),
			"a clientless grant must carry a synthetic client identity, never a real registered client")
		expFloat, ok := claims["exp"].(float64)
		gomega.Expect(ok).To(gomega.BeTrue(), "exp claim must be a number: %#v", claims["exp"])
		expiry := time.Unix(int64(expFloat), 0)
		gomega.Expect(expiry).To(gomega.BeTemporally("<=", assertionExpiry.Add(5*time.Second)),
			"the token lifetime must be bounded by the source assertion expiry (with clock and transport tolerance)")
		gomega.Expect(expiry).To(gomega.BeTemporally(">", issuedAt),
			"the issued token must not already be expired")

		ginkgo.By("using the issued token to make a real authenticated MCP request through the proxy")
		mcpURL := endpoint + "/mcp"
		rawClient, err := e2e.NewRawMCPClient(30 * time.Second)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var sessionID string
		gomega.Eventually(func() error {
			var initErr error
			sessionID, initErr = legacySessionInit(rawClient, mcpURL, "jwt-bearer-e2e", bearerHeader(response.AccessToken))
			return initErr
		}, timeout, pollInterval).Should(gomega.Succeed(),
			"the proxy must accept the JWT-bearer-issued token as a normal bearer token")

		toolCallReq, err := e2e.NewLegacyRequest("tools/call", map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "jwtbearere2e"},
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		toolCallReq.WithSessionID(sessionID)
		toolCallReq.SetHeader("Authorization", "Bearer "+response.AccessToken)
		toolCallReq.SetHeader("Accept", "application/json, text/event-stream")
		toolResp, err := rawClient.Send(context.Background(), mcpURL, toolCallReq)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(toolResp.StatusCode).To(gomega.Equal(http.StatusOK), string(toolResp.Body))

		var toolResult struct {
			IsError bool `json:"isError"`
		}
		gomega.Expect(json.Unmarshal(toolResp.Result, &toolResult)).To(gomega.Succeed())
		gomega.Expect(toolResult.IsError).To(gomega.BeFalse(),
			"the echo tool call must succeed once authenticated with the JWT-bearer-issued token: %s", toolResp.Body)
	})

	ginkgo.It("rejects an assertion requesting an unauthorized resource with invalid_target", func() {
		port, cleanup, err := startRateLimitServicePortForward("mcp-"+serverName+"-proxy", 8080)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()

		assertion := mintJWTBearerAssertion(oidcLocalPort, externalSub, tokenEndpointAudience(serverIssuer), "wrong-resource-jti", time.Now())
		status, body := requestJWTBearerGrant(fmt.Sprintf("http://localhost:%d", port), assertion, "https://resource.example/forbidden")
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))
		gomega.Expect(decodeOAuthError(body)).To(gomega.Equal("invalid_target"))
	})

	ginkgo.It("rejects replay of a JWT-bearer assertion with invalid_grant", func() {
		port, cleanup, err := startRateLimitServicePortForward("mcp-"+serverName+"-proxy", 8080)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer cleanup()

		endpoint := fmt.Sprintf("http://localhost:%d", port)
		assertion := mintJWTBearerAssertion(oidcLocalPort, externalSub, tokenEndpointAudience(serverIssuer), "replayed-jti", time.Now())
		status, body := requestJWTBearerGrant(endpoint, assertion, resource)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))
		status, body = requestJWTBearerGrant(endpoint, assertion, resource)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))
		gomega.Expect(decodeOAuthError(body)).To(gomega.Equal("invalid_grant"))
	})
})

// bearerHeader builds the extra-header map that carries a bearer token on a
// raw Legacy MCP request.
func bearerHeader(token string) map[string]string {
	// This MCPServer uses the streamable-http transport, which requires an
	// Accept header listing both media types (test/e2e/mcp_raw_client.go's
	// RawMCPClient.Send sets no Accept header by default; see #6104).
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json, text/event-stream",
	}
}

// decodeOAuthError extracts the RFC 6749 "error" field from an OAuth token
// endpoint error response, failing the test with a clear message if the body
// isn't a JSON object with that field.
func decodeOAuthError(body []byte) string {
	var oauthErr struct {
		Error string `json:"error"`
	}
	gomega.ExpectWithOffset(1, json.Unmarshal(body, &oauthErr)).To(gomega.Succeed(), "error body: %s", body)
	gomega.ExpectWithOffset(1, oauthErr.Error).NotTo(gomega.BeEmpty(), "error body: %s", body)
	return oauthErr.Error
}

// decodeUnverifiedJWTClaims decodes a JWT's claims without verifying its
// signature. The issued access token's own authenticity is already
// established by having come back from a 200 response on this server's own
// /oauth/token — this only inspects its content.
func decodeUnverifiedJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	gomega.ExpectWithOffset(1, parts).To(gomega.HaveLen(3), "access token must be a JWT: %s", token)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	claims := map[string]any{}
	gomega.ExpectWithOffset(1, json.Unmarshal(payload, &claims)).To(gomega.Succeed())
	return claims
}

func tokenEndpointAudience(issuer string) string {
	return issuer + "/oauth/token"
}

func mintJWTBearerAssertion(oidcLocalPort int, subject, audience, jti string, issuedAt time.Time) string {
	params := url.Values{
		"subject": {subject},
		"aud":     {audience},
		"jti":     {jti},
		"iat":     {fmt.Sprintf("%d", issuedAt.Unix())},
		"exp":     {fmt.Sprintf("%d", issuedAt.Add(30*time.Second).Unix())},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		fmt.Sprintf("http://localhost:%d/token?%s", oidcLocalPort, params.Encode()), nil)
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

func requestJWTBearerGrant(endpoint, assertion, resource string) (int, []byte) {
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
		"resource":   {resource},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/oauth/token", strings.NewReader(form.Encode()))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return resp.StatusCode, body
}
