// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// This test drives refresh-token issuance through RFC 8693 token exchange on a
// VirtualMCPServer's embedded authorization server. Two private_key_jwt
// delegate clients share one policy; only one sets exchangeRefreshTokens, so
// the other proves the feature, not a default, issues the token.
//
// The refresh token comes back the RFC 8693 way: in the access_token member,
// with token_type N_A and issued_token_type set to the refresh-token URN. It is
// opaque. The delegate client has only the token-exchange grant, so it cannot
// present the token at the refresh_token grant.

const (
	// exchangeGrantDetailsType is the authorization_details type that asks for a
	// refresh token through token exchange.
	exchangeGrantDetailsType = "https://toolhive.dev/exchange_grant"
	clientAssertionTypeJWT   = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
)

var _ = ginkgo.Describe("VirtualMCPServer delegate client exchangeRefreshTokens", ginkgo.Ordered, func() {
	const (
		timeout        = 5 * time.Minute
		pollInterval   = 2 * time.Second
		refreshClient  = "e2e-xrt-refresh-delegate"
		controlClient  = "e2e-xrt-control-delegate"
		allowedSource  = "e2e-xrt-allowed-source"
		foreignSource  = "e2e-xrt-foreign-source"
		subject        = "xrt-subject"
		jwksKey        = "jwks.json"
		kid            = "e2e-xrt-key"
		refreshMaxLife = 24 * time.Hour
	)

	var (
		backendName, groupName, hmacSecretName, oidcConfigName string
		signingKeySecretName, jwksConfigMapName, vmcpName      string
		issuer                                                 string
		serverKey                                              *rsa.PrivateKey
		delegateKey                                            *ecdsa.PrivateKey
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-xrt-backend-" + suffix
		groupName = "e2e-xrt-group-" + suffix
		hmacSecretName = "e2e-xrt-hmac-" + suffix
		oidcConfigName = "e2e-xrt-oidc-" + suffix
		signingKeySecretName = "e2e-xrt-key-" + suffix
		jwksConfigMapName = "e2e-xrt-cm-" + suffix
		vmcpName = "e2e-xrt-vmcp-" + suffix
		issuer = fmt.Sprintf("https://vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)

		var err error
		serverKey, err = rsa.GenerateKey(rand.Reader, 2048)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		delegateKey = newDelegateJWKSKey()

		hmac := make([]byte, 32)
		_, err = rand.Read(hmac)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		createFixture := func(obj client.Object) {
			gomega.Expect(k8sClient.Create(ctx, obj)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(gomega.Succeed())
			})
		}

		ginkgo.By("creating the signing key, HMAC secret and the public-only JWKS ConfigMap")
		createFixture(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: signingKeySecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"private-key": pemEncodePKCS1(serverKey)},
		})
		createFixture(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: hmacSecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"hmac": hmac},
		})
		createFixture(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: jwksConfigMapName, Namespace: defaultNamespace},
			Data:       map[string]string{jwksKey: delegateJWKSDocument(delegateJWKSEntry{kid, delegateKey})},
		})
		createFixture(&mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: issuer, JWKSAllowPrivateIP: true}},
		})

		CreateMCPGroupAndWait(ctx, k8sClient, groupName, defaultNamespace, "exchange refresh tokens e2e group", timeout, pollInterval)
		ginkgo.DeferCleanup(func() {
			group := &mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: groupName, Namespace: defaultNamespace}}
			gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, group))).To(gomega.Succeed())
			WaitForObjectDeletion(ctx, k8sClient, group, timeout, pollInterval)
		})
		createFixture(v1beta1test.NewMCPServer(backendName, defaultNamespace,
			v1beta1test.WithImage(images.YardstickServerImage),
			v1beta1test.WithTransport("streamable-http"),
			v1beta1test.WithProxyPort(8080),
			v1beta1test.WithMCPPort(8080),
			v1beta1test.WithMCPGroupRef(groupName),
		))
		gomega.Eventually(func() mcpv1beta1.MCPServerPhase {
			server := &mcpv1beta1.MCPServer{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: backendName, Namespace: defaultNamespace}, server)).To(gomega.Succeed())
			return server.Status.Phase
		}, timeout, pollInterval).Should(gomega.Equal(mcpv1beta1.MCPServerPhaseReady))

		ginkgo.By("creating the VirtualMCPServer with one delegate that enables exchangeRefreshTokens and one that does not")
		// The server's only allowed audience is the OIDC resource URL, which is the
		// issuer, so the client API audience has to be the issuer too.
		delegate := func(clientID string, refresh *mcpv1beta1.DelegateClientExchangeRefreshTokensConfig) mcpv1beta1.DelegateClientConfig {
			return mcpv1beta1.DelegateClientConfig{
				ClientID:                clientID,
				TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
				JWKSRef:                 &mcpv1beta1.ConfigMapKeyRef{Name: jwksConfigMapName, Key: jwksKey},
				Scopes:                  []string{"profile"},
				Audiences:               []string{issuer},
				TokenExchange: &mcpv1beta1.DelegateClientTokenExchangeConfig{
					SelfIssuedSubjectTokens: "restricted",
					AllowedSourceClients:    []string{allowedSource},
					APIAudience:             issuer,
					ExchangeRefreshTokens:   refresh,
				},
			}
		}
		vmcp := v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigName, Audience: issuer, ResourceURL: issuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               issuer,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				InboundGrants: &mcpv1beta1.InboundGrantsConfig{TokenExchange: &mcpv1beta1.TokenExchangeInboundGrantConfig{
					DelegateClients: []mcpv1beta1.DelegateClientConfig{
						delegate(refreshClient, &mcpv1beta1.DelegateClientExchangeRefreshTokensConfig{
							RefreshTokenMaxLifetime: &metav1.Duration{Duration: refreshMaxLife},
						}),
						delegate(controlClient, nil),
					},
				}},
			}),
		)
		gomega.Expect(k8sClient.Create(ctx, vmcp)).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, vmcp))).To(gomega.Succeed())
			WaitForObjectDeletion(ctx, k8sClient, vmcp, timeout, pollInterval)
		})
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace, mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", timeout, pollInterval)
	})

	// exchange posts a token-exchange request as clientID. Each call signs a
	// fresh client assertion and a fresh subject token, because the auth server
	// accepts a login subject token for one refresh-token exchange only.
	exchange := func(endpoint, clientID, sourceClient string, form url.Values) (int, []byte) {
		return refreshExchangePost(endpoint, clientID, signRefreshAssertion(delegateKey, kid, clientID, issuer),
			refreshSubjectToken(serverKey, issuer, sourceClient, subject), form)
	}
	refreshTokenRequest := func() url.Values {
		return url.Values{
			"grant_type":           {oauthproto.GrantTypeTokenExchange},
			"subject_token_type":   {oauthproto.TokenTypeAccessToken},
			"requested_token_type": {oauthproto.TokenTypeRefreshToken},
			"authorization_details": {fmt.Sprintf(`[{"type":%q,"issuable_token_types":[%q]}]`,
				exchangeGrantDetailsType, oauthproto.TokenTypeRefreshToken)},
		}
	}

	ginkgo.It("issues a refresh token to a delegate client that enables exchangeRefreshTokens", func() {
		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()

		before := time.Now()
		status, body := exchange(localURL, refreshClient, allowedSource, refreshTokenRequest())
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))

		var response struct {
			AccessToken          string            `json:"access_token"`
			TokenType            string            `json:"token_type"`
			IssuedTokenType      string            `json:"issued_token_type"`
			ExpiresIn            int64             `json:"expires_in"`
			RefreshToken         *string           `json:"refresh_token"`
			AuthorizationDetails []json.RawMessage `json:"authorization_details"`
		}
		gomega.Expect(json.Unmarshal(body, &response)).To(gomega.Succeed(), string(body))
		gomega.Expect(response.AccessToken).NotTo(gomega.BeEmpty(), "the refresh token is the access_token member: %s", body)
		gomega.Expect(response.TokenType).To(gomega.Equal("N_A"))
		gomega.Expect(response.IssuedTokenType).To(gomega.Equal(oauthproto.TokenTypeRefreshToken))
		gomega.Expect(response.RefreshToken).To(gomega.BeNil(), "RFC 8693 has no refresh_token member: %s", body)
		// The configured 24h must reach the server: expires_in is the cap, within a minute.
		gomega.Expect(response.ExpiresIn).To(gomega.BeNumerically("~", int64(refreshMaxLife/time.Second), 60))

		gomega.Expect(response.AuthorizationDetails).To(gomega.HaveLen(1), string(body))
		var granted struct {
			Type               string   `json:"type"`
			IssuableTokenTypes []string `json:"issuable_token_types"`
			ExpiresAt          int64    `json:"expires_at"`
		}
		gomega.Expect(json.Unmarshal(response.AuthorizationDetails[0], &granted)).To(gomega.Succeed())
		gomega.Expect(granted.Type).To(gomega.Equal(exchangeGrantDetailsType))
		gomega.Expect(granted.IssuableTokenTypes).To(gomega.Equal([]string{oauthproto.TokenTypeRefreshToken}))
		// The grant is capped at the configured 24h: it must not outlive it, and it must
		// reach the full cap, which shows the duration from the CRD reached the server.
		gomega.Expect(granted.ExpiresAt).To(gomega.BeNumerically("<=", before.Add(refreshMaxLife+time.Minute).Unix()),
			"the grant must not outlive refreshTokenMaxLifetime")
		gomega.Expect(granted.ExpiresAt).To(gomega.BeNumerically(">=", before.Add(refreshMaxLife).Unix()),
			"the grant must reach the configured refreshTokenMaxLifetime")
	})

	ginkgo.It("does not let the issued token be redeemed at the refresh_token grant, and spends the subject token", func() {
		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()

		subjectToken := refreshSubjectToken(serverKey, issuer, allowedSource, subject)
		send := func(form url.Values) (int, []byte) {
			return refreshExchangePost(localURL, refreshClient,
				signRefreshAssertion(delegateKey, kid, refreshClient, issuer), subjectToken, form)
		}

		status, body := send(refreshTokenRequest())
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))
		var issued struct {
			AccessToken string `json:"access_token"`
		}
		gomega.Expect(json.Unmarshal(body, &issued)).To(gomega.Succeed(), string(body))
		gomega.Expect(issued.AccessToken).NotTo(gomega.BeEmpty())

		ginkgo.By("refusing a second exchange of the same subject token")
		status, body = send(refreshTokenRequest())
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))
		gomega.Expect(delegateOAuthError(body)).To(gomega.Equal("invalid_request"), string(body))
		gomega.Expect(refreshOAuthDescription(body)).To(gomega.Equal("token_used"), string(body))

		ginkgo.By("refusing the refresh_token grant to a client that has only token exchange")
		status, body, err := refreshGrantPost(localURL, refreshClient,
			signRefreshAssertion(delegateKey, kid, refreshClient, issuer), issued.AccessToken)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))
		gomega.Expect(delegateOAuthError(body)).To(gomega.Equal("unauthorized_client"), string(body))
		gomega.Expect(string(body)).NotTo(gomega.ContainSubstring("access_token"))
	})

	ginkgo.DescribeTable("refuses a subject token from a client that is not an allowed source",
		func(sourceClient string) {
			localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
			defer cleanup()

			status, body := exchange(localURL, refreshClient, sourceClient, refreshTokenRequest())
			gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))
			gomega.Expect(delegateOAuthError(body)).To(gomega.Equal("invalid_request"), string(body))
			gomega.Expect(refreshOAuthDescription(body)).To(gomega.Equal("subject_token_source_client"), string(body))
			gomega.Expect(string(body)).NotTo(gomega.ContainSubstring("access_token"))
			gomega.Expect(string(body)).NotTo(gomega.ContainSubstring("issued_token_type"))
		},
		ginkgo.Entry("a client outside allowedSourceClients", foreignSource),
		ginkgo.Entry("the delegate client itself", refreshClient),
	)

	ginkgo.It("issues no refresh token to a delegate client without exchangeRefreshTokens", func() {
		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()

		ginkgo.By("refusing a request for a refresh token")
		status, body := exchange(localURL, controlClient, allowedSource, refreshTokenRequest())
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))
		gomega.Expect(delegateOAuthError(body)).To(gomega.Equal("invalid_request"), string(body))
		gomega.Expect(refreshOAuthDescription(body)).To(gomega.ContainSubstring("is not supported for this client"), string(body))
		gomega.Expect(string(body)).NotTo(gomega.ContainSubstring("issued_token_type"))

		ginkgo.By("still issuing an ordinary access token, with no refresh_token member")
		status, body = exchange(localURL, controlClient, allowedSource, url.Values{
			"grant_type":         {oauthproto.GrantTypeTokenExchange},
			"subject_token_type": {oauthproto.TokenTypeJWT},
			"audience":           {issuer},
		})
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))
		var response map[string]any
		gomega.Expect(json.Unmarshal(body, &response)).To(gomega.Succeed(), string(body))
		gomega.Expect(response).NotTo(gomega.HaveKey("refresh_token"))
		gomega.Expect(response["issued_token_type"]).To(gomega.Equal(oauthproto.TokenTypeAccessToken))
		accessToken, ok := response["access_token"].(string)
		gomega.Expect(ok).To(gomega.BeTrue(), string(body))
		claims := verifiedJWTClaims(accessToken, &serverKey.PublicKey)
		gomega.Expect(claims["sub"]).To(gomega.Equal(subject))
		act, ok := claims["act"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue(), "token has no act claim object: %v", claims)
		gomega.Expect(act["sub"]).To(gomega.Equal(controlClient))
	})
})

// refreshSubjectToken is a login-style self-issued access token whose client_id
// claim is sourceClient. It carries a fresh jti, which the refresh-token
// exchange requires and consumes.
func refreshSubjectToken(key *rsa.PrivateKey, issuer, sourceClient, sub string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: issuer, Subject: sub, Audience: jwt.Audience{issuer},
		Expiry: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now), ID: rand.Text(),
	}).Claims(map[string]any{"client_id": sourceClient, "scope": "profile"}).Serialize()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return token
}

func signRefreshAssertion(key *ecdsa.PrivateKey, kid, clientID, audience string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("client-authentication+jwt").WithHeader("kid", kid))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	now := time.Now()
	assertion, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: clientID, Subject: clientID, Audience: jwt.Audience{audience},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)), ID: rand.Text(),
	}).Serialize()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return assertion
}

// refreshExchangePost sends a token-exchange request authenticated with a
// private_key_jwt assertion. form carries the request-specific parameters; the
// client, assertion and subject token are added here.
func refreshExchangePost(endpoint, clientID, assertion, subjectToken string, form url.Values) (int, []byte) {
	merged := url.Values{}
	for name, values := range form {
		merged[name] = values
	}
	merged.Set("subject_token", subjectToken)
	merged.Set("client_id", clientID)
	merged.Set("client_assertion_type", clientAssertionTypeJWT)
	merged.Set("client_assertion", assertion)
	status, body, err := refreshTokenEndpointPost(endpoint, merged)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return status, body
}

// refreshGrantPost presents token as grant_type=refresh_token.
func refreshGrantPost(endpoint, clientID, assertion, token string) (int, []byte, error) {
	return refreshTokenEndpointPost(endpoint, url.Values{
		"grant_type": {oauthproto.GrantTypeRefreshToken}, "refresh_token": {token}, "client_id": {clientID},
		"client_assertion_type": {clientAssertionTypeJWT}, "client_assertion": {assertion},
	})
}

func refreshTokenEndpointPost(endpoint string, form url.Values) (int, []byte, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := privateKeyJWTHTTPClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return response.StatusCode, body, err
}

func refreshOAuthDescription(body []byte) string {
	var parsed struct {
		Description string `json:"error_description"`
	}
	gomega.Expect(json.Unmarshal(body, &parsed)).To(gomega.Succeed(), string(body))
	return parsed.Description
}
