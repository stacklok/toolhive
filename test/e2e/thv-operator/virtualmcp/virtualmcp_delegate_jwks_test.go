// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

// This test drives a private_key_jwt delegate client whose public JWKS lives in
// a ConfigMap (jwksRef) on a VirtualMCPServer's embedded authorization server.
// The client authenticates the way an embedding broker does: an ES256 client
// assertion with a kid header, exchanging a self-issued subject token. Admission
// rules are covered by envtest; these cases prove the runtime behaviour.

// jwksDelegateClientID is the delegate client every case in this suite exchanges as.
const jwksDelegateClientID = "e2e-jwks-delegate"

var _ = ginkgo.Describe("VirtualMCPServer delegate client jwksRef", ginkgo.Ordered, func() {
	const (
		timeout       = 5 * time.Minute
		pollInterval  = 2 * time.Second
		delegateID    = jwksDelegateClientID
		allowedSource = "e2e-jwks-allowed-source"
		foreignSource = "e2e-jwks-foreign-source"
		subject       = "jwks-subject"
		jwksKey       = "jwks.json"
		kidA          = "e2e-key-a"
		kidB          = "e2e-key-b"
	)

	var (
		backendName, groupName, hmacSecretName, oidcConfigName string
		signingKeySecretName, jwksConfigMapName, vmcpName      string
		issuer                                                 string
		serverKey                                              *rsa.PrivateKey
		keyA, keyB, strangerKey                                *ecdsa.PrivateKey
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-jwks-backend-" + suffix
		groupName = "e2e-jwks-group-" + suffix
		hmacSecretName = "e2e-jwks-hmac-" + suffix
		oidcConfigName = "e2e-jwks-oidc-" + suffix
		signingKeySecretName = "e2e-jwks-key-" + suffix
		jwksConfigMapName = "e2e-jwks-cm-" + suffix
		vmcpName = "e2e-jwks-vmcp-" + suffix
		issuer = fmt.Sprintf("https://vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)

		var err error
		serverKey, err = rsa.GenerateKey(rand.Reader, 2048)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		keyA = newDelegateJWKSKey()
		keyB = newDelegateJWKSKey()
		strangerKey = newDelegateJWKSKey()

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
			Data:       map[string]string{jwksKey: delegateJWKSDocument(delegateJWKSEntry{kidA, keyA})},
		})
		createFixture(&mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: issuer, JWKSAllowPrivateIP: true}},
		})

		CreateMCPGroupAndWait(ctx, k8sClient, groupName, defaultNamespace, "delegate jwksRef e2e group", timeout, pollInterval)
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

		ginkgo.By("creating the VirtualMCPServer with a restricted private_key_jwt delegate client")
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
					DelegateClients: []mcpv1beta1.DelegateClientConfig{{
						ClientID:                delegateID,
						TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
						JWKSRef:                 &mcpv1beta1.ConfigMapKeyRef{Name: jwksConfigMapName, Key: jwksKey},
						Scopes:                  []string{"profile"},
						Audiences:               []string{issuer},
						TokenExchange: &mcpv1beta1.DelegateClientTokenExchangeConfig{
							SelfIssuedSubjectTokens: "restricted",
							AllowedSourceClients:    []string{allowedSource},
						},
					}},
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

	// subjectToken is a self-issued subject token whose client_id claim is sourceClient.
	subjectToken := func(sourceClient string) string {
		return signSelfIssuedSubjectToken(serverKey, issuer, sourceClient, subject)
	}

	ginkgo.It("mounts the JWKS read-only and accepts an assertion signed with the matching key", func() {
		deployment := &appsv1.Deployment{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, deployment)).To(gomega.Succeed())

		var volume *corev1.Volume
		for i := range deployment.Spec.Template.Spec.Volumes {
			if deployment.Spec.Template.Spec.Volumes[i].Name == ctrlutil.AuthServerDelegateClientJWKSVolumePrefix+"0" {
				volume = &deployment.Spec.Template.Spec.Volumes[i]
			}
		}
		gomega.Expect(volume).NotTo(gomega.BeNil())
		gomega.Expect(volume.ConfigMap).NotTo(gomega.BeNil())
		gomega.Expect(volume.ConfigMap.Name).To(gomega.Equal(jwksConfigMapName))
		gomega.Expect(volume.ConfigMap.Items).To(gomega.Equal([]corev1.KeyToPath{{Key: jwksKey, Path: ctrlutil.AuthServerDelegateClientJWKSFileName}}))

		const jwksPath = "/etc/toolhive/authserver/delegate-jwks/0/jwks.json"
		var mount *corev1.VolumeMount
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for i := range container.VolumeMounts {
				if container.VolumeMounts[i].MountPath == jwksPath {
					mount = &container.VolumeMounts[i]
				}
			}
		}
		gomega.Expect(mount).NotTo(gomega.BeNil())
		gomega.Expect(mount.Name).To(gomega.Equal(volume.Name))
		gomega.Expect(mount.ReadOnly).To(gomega.BeTrue())
		gomega.Expect(mount.SubPath).To(gomega.Equal(ctrlutil.AuthServerDelegateClientJWKSFileName))

		configMap := &corev1.ConfigMap{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName + "-vmcp-config", Namespace: defaultNamespace}, configMap)).To(gomega.Succeed())
		gomega.Expect(configMap.Data["authserver-config.yaml"]).To(gomega.ContainSubstring(jwksPath))

		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()
		status, body, err := exchangeWithDelegateAssertion(localURL,
			signDelegateAssertion(keyA, kidA, issuer), subjectToken(delegateID), issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))
		var token struct {
			AccessToken string `json:"access_token"`
		}
		gomega.Expect(json.Unmarshal(body, &token)).To(gomega.Succeed())
		claims := verifiedJWTClaims(token.AccessToken, &serverKey.PublicKey)
		gomega.Expect(claims["iss"]).To(gomega.Equal(issuer))
		gomega.Expect(claims["sub"]).To(gomega.Equal(subject))
		act, ok := claims["act"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue(), "token has no act claim object: %v", claims)
		gomega.Expect(act["sub"]).To(gomega.Equal(delegateID))
	})

	ginkgo.It("rejects an assertion signed with a different key under the registered kid", func() {
		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()

		status, body, err := exchangeWithDelegateAssertion(localURL,
			signDelegateAssertion(strangerKey, kidA, issuer), subjectToken(delegateID), issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status).To(gomega.Equal(http.StatusUnauthorized), string(body))
		gomega.Expect(delegateOAuthError(body)).To(gomega.Equal("invalid_client"), string(body))

		ginkgo.By("accepting the same subject token with the registered key")
		status, body, err = exchangeWithDelegateAssertion(localURL,
			signDelegateAssertion(keyA, kidA, issuer), subjectToken(delegateID), issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))
	})

	ginkgo.It("refuses a self-issued subject token from a client that is neither the delegate nor an allowed source", func() {
		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()
		exchange := func(sourceClient string) (int, []byte) {
			status, body, err := exchangeWithDelegateAssertion(localURL,
				signDelegateAssertion(keyA, kidA, issuer), subjectToken(sourceClient), issuer)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return status, body
		}

		status, body := exchange(foreignSource)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), string(body))
		gomega.Expect(delegateOAuthError(body)).To(gomega.Equal("invalid_grant"), string(body))
		gomega.Expect(string(body)).To(gomega.ContainSubstring("not issued to this client or to one of its allowed source clients"))

		ginkgo.By("accepting a token from an allowedSourceClients entry")
		status, body = exchange(allowedSource)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), string(body))
	})

	ginkgo.It("rolls the pod on a JWKS edit and moves trust from the old key to the new one", func() {
		rotate := func(entries ...delegateJWKSEntry) {
			before := delegatePodTemplateChecksum(vmcpName)
			cm := &corev1.ConfigMap{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jwksConfigMapName, Namespace: defaultNamespace}, cm)).To(gomega.Succeed())
			cm.Data[jwksKey] = delegateJWKSDocument(entries...)
			gomega.Expect(k8sClient.Update(ctx, cm)).To(gomega.Succeed())

			gomega.Eventually(func() string {
				return delegatePodTemplateChecksum(vmcpName)
			}, timeout, pollInterval).ShouldNot(gomega.Or(gomega.Equal(before), gomega.BeEmpty()))
			gomega.Eventually(func() bool {
				return delegateDeploymentRolledOut(vmcpName)
			}, timeout, pollInterval).Should(gomega.BeTrue())
			delegateWaitForSingleReadyPod(vmcpName, timeout, pollInterval)
			WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)
		}

		ginkgo.By("publishing the new key next to the old one")
		rotate(delegateJWKSEntry{kidA, keyA}, delegateJWKSEntry{kidB, keyB})
		localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
		defer cleanup()
		// keyB is the load-bearing check: it only succeeds once the new JWKS is loaded.
		eventuallyDelegateExchange(localURL, issuer, subjectToken(delegateID), keyB, kidB, http.StatusOK)
		// keyA is a control: the old key must keep working during the overlap.
		eventuallyDelegateExchange(localURL, issuer, subjectToken(delegateID), keyA, kidA, http.StatusOK)
		cleanup()

		ginkgo.By("retiring the old key")
		rotate(delegateJWKSEntry{kidB, keyB})
		localURL, cleanupAfter := portForwardDelegateAuthServer(vmcpName)
		defer cleanupAfter()
		// keyA rejection is the load-bearing check: it only holds once the retired key is gone.
		// The kid is no longer registered, so fosite rejects the assertion as a malformed
		// request (400 invalid_request, "kid ... could not be found"), not as a wrong key
		// under a registered kid (401 invalid_client, as in the wrong-key case above).
		body := eventuallyDelegateExchange(localURL, issuer, subjectToken(delegateID), keyA, kidA, http.StatusBadRequest)
		gomega.Expect(delegateOAuthError(body)).To(gomega.Equal("invalid_request"), string(body))
		// keyB is a control: the surviving key must still be accepted.
		eventuallyDelegateExchange(localURL, issuer, subjectToken(delegateID), keyB, kidB, http.StatusOK)
	})
})

type delegateJWKSEntry struct {
	kid string
	key *ecdsa.PrivateKey
}

func newDelegateJWKSKey() *ecdsa.PrivateKey {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return key
}

// delegateJWKSDocument serialises the public halves of the given keys as a
// JWKS. Private material never reaches the ConfigMap.
func delegateJWKSDocument(entries ...delegateJWKSEntry) string {
	set := jose.JSONWebKeySet{}
	for _, entry := range entries {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: &entry.key.PublicKey, KeyID: entry.kid, Algorithm: "ES256", Use: "sig"})
	}
	doc, err := json.Marshal(set)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(string(doc)).NotTo(gomega.ContainSubstring(`"d"`))
	return string(doc)
}

func signDelegateAssertion(key *ecdsa.PrivateKey, kid, audience string) string {
	clientID := jwksDelegateClientID
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

// exchangeWithDelegateAssertion returns transport errors instead of asserting
// so callers can poll across a pod roll.
func exchangeWithDelegateAssertion(endpoint, assertion, subjectToken, audience string) (int, []byte, error) {
	clientID := jwksDelegateClientID
	form := url.Values{
		"grant_type": {oauthproto.GrantTypeTokenExchange}, "subject_token": {subjectToken},
		"subject_token_type": {oauthproto.TokenTypeJWT}, "audience": {audience}, "client_id": {clientID},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}, "client_assertion": {assertion},
	}
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

// eventuallyDelegateExchange retries with a fresh assertion each attempt until
// the endpoint answers wantStatus, and returns that response body.
func eventuallyDelegateExchange(
	endpoint, audience, subjectToken string, key *ecdsa.PrivateKey, kid string, wantStatus int,
) []byte {
	var lastBody []byte
	gomega.Eventually(func() int {
		status, body, err := exchangeWithDelegateAssertion(endpoint,
			signDelegateAssertion(key, kid, audience), subjectToken, audience)
		if err != nil {
			return 0
		}
		lastBody = body
		return status
	}, 2*time.Minute, time.Second).Should(gomega.Equal(wantStatus), func() string { return string(lastBody) })
	return lastBody
}

func delegateOAuthError(body []byte) string {
	var parsed struct {
		Error string `json:"error"`
	}
	gomega.Expect(json.Unmarshal(body, &parsed)).To(gomega.Succeed(), string(body))
	return parsed.Error
}

func delegatePodTemplateChecksum(vmcpName string) string {
	deployment := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, deployment); err != nil {
		gomega.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue())
		return ""
	}
	return deployment.Spec.Template.Annotations[ctrlutil.AuthServerConfigMapChecksumAnnotation]
}

// delegateWaitForSingleReadyPod waits until exactly one vMCP pod exists, it is
// Ready, and no pod is terminating. Deployment status does not count
// terminating pods, so a rollout check alone can pass while the old pod is
// still alive.
func delegateWaitForSingleReadyPod(vmcpName string, timeout, pollInterval time.Duration) {
	labels := map[string]string{
		"app.kubernetes.io/name":     "virtualmcpserver",
		"app.kubernetes.io/instance": vmcpName,
	}
	gomega.Eventually(func() error {
		pods := &corev1.PodList{}
		if err := k8sClient.List(ctx, pods, client.InNamespace(defaultNamespace), client.MatchingLabels(labels)); err != nil {
			return err
		}
		for i := range pods.Items {
			if pods.Items[i].DeletionTimestamp != nil {
				return fmt.Errorf("pod %s is still terminating", pods.Items[i].Name)
			}
		}
		if len(pods.Items) != 1 {
			return fmt.Errorf("expected exactly 1 pod, found %d", len(pods.Items))
		}
		return testutil.CheckPodsReadyCount(ctx, k8sClient, defaultNamespace, labels, 1)
	}, timeout, pollInterval).Should(gomega.Succeed())
}

// delegateDeploymentRolledOut reports whether the Deployment has finished
// rolling its current template: no old replicas remain and all new ones are available.
func delegateDeploymentRolledOut(vmcpName string) bool {
	deployment := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, deployment); err != nil {
		return false
	}
	want := int32(1)
	if deployment.Spec.Replicas != nil {
		want = *deployment.Spec.Replicas
	}
	status := deployment.Status
	return status.ObservedGeneration >= deployment.Generation &&
		status.UpdatedReplicas == want && status.Replicas == want && status.AvailableReplicas == want
}
