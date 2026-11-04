// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
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
	"github.com/redis/go-redis/v9"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/test/e2e/images"
)

// The delegate client is fixed for the whole spec, so the request helper below
// reads it directly.
const (
	delegateRedisClientID     = "e2e-delegate-redis-client"
	delegateRedisClientSecret = "e2e-delegate-redis-client-secret-testing"
)

// A delegate client's scopes live in the auth server's Redis row. Editing them
// in the CRD rolls the Deployment, and the new pod must start against the row
// the old pod wrote instead of refusing to start. The scopes are drawn from the
// default supported set (openid, profile, email, offline_access).
var _ = ginkgo.Describe("VirtualMCPServer delegate clients with Redis storage", ginkgo.Ordered, func() {
	const (
		redisPassword = "e2e-delegate-redis-password"
		keptScope     = "profile"
		droppedScope  = "email"
		addedScope    = "offline_access"
	)

	var (
		backendName, delegateSecretName, redisPasswordSecretName string
		groupName, hmacSecretName, oidcConfigName                string
		signingKeySecretName, vmcpName, redisName, issuer        string
		signingPrivateKey                                        *rsa.PrivateKey
		signingPublicKey                                         *rsa.PublicKey
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		backendName = "e2e-delegate-redis-backend-" + suffix
		delegateSecretName = "e2e-delegate-redis-secret-" + suffix
		redisPasswordSecretName = "e2e-delegate-redis-pw-" + suffix
		groupName = "e2e-delegate-redis-group-" + suffix
		hmacSecretName = "e2e-delegate-redis-hmac-" + suffix
		oidcConfigName = "e2e-delegate-redis-oidc-" + suffix
		signingKeySecretName = "e2e-delegate-redis-key-" + suffix
		vmcpName = "e2e-delegate-redis-vmcp-" + suffix
		redisName = "e2e-delegate-redis-" + suffix

		ginkgo.By("deploying Redis for the auth server's storage")
		deployRedisWithPassword(redisName, redisPassword)
		redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)

		ginkgo.By("creating the secrets")
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: delegateSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"secret": delegateRedisClientSecret},
		})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: redisPasswordSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"password": redisPassword},
		})).To(gomega.Succeed())
		privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		signingPrivateKey = privateKey
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

		issuer = fmt.Sprintf("https://vmcp-%s.%s.svc.cluster.local:4483", vmcpName, defaultNamespace)
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: issuer, JWKSAllowPrivateIP: true}},
		})).To(gomega.Succeed())

		CreateMCPGroupAndWait(ctx, k8sClient, groupName, defaultNamespace, "delegate client redis e2e group", e2eTimeout, e2ePollInterval)
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
		}, e2eTimeout, e2ePollInterval).Should(gomega.Equal(mcpv1beta1.MCPServerPhaseReady))

		// VirtualMCPServer has no authServerRef: MCPExternalAuthConfig embedded auth
		// serves MCPServer and MCPRemoteProxy only, so this uses the inline
		// spec.authServerConfig. Both paths build the same Redis storage RunConfig.
		ginkgo.By("creating a single-replica VirtualMCPServer with a delegate client on Redis storage")
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(groupName),
			v1beta1test.WithVMCPReplicas(1),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name: oidcConfigName, Audience: issuer, ResourceURL: issuer,
			}}),
			v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: issuer, AllowConfidentialClientRegistration: false,
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{Name: signingKeySecretName, Key: "private-key"}},
				HMACSecretRefs:       []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
				DelegateClients: []mcpv1beta1.DelegateClientConfig{{
					ClientID: delegateRedisClientID, ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: delegateSecretName, Key: "secret"},
					Scopes: []string{keptScope, droppedScope}, Audiences: []string{issuer},
				}},
				Storage: &mcpv1beta1.AuthServerStorageConfig{
					Type: mcpv1beta1.AuthServerStorageTypeRedis,
					Redis: &mcpv1beta1.RedisStorageConfig{
						Addr: redisAddr,
						ACLUserConfig: &mcpv1beta1.RedisACLUserConfig{
							PasswordSecretRef: &mcpv1beta1.SecretKeyRef{Name: redisPasswordSecretName, Key: "password"},
						},
					},
				},
			}),
		))).To(gomega.Succeed())
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, e2eTimeout, e2ePollInterval)
		WaitForCondition(ctx, k8sClient, vmcpName, defaultNamespace, mcpv1beta1.ConditionTypeAuthServerConfigValidated, "True", e2eTimeout, e2ePollInterval)
	})

	ginkgo.AfterAll(func() {
		deleteFixture := func(err error) {
			gomega.Expect(err == nil || apierrors.IsNotFound(err)).To(gomega.BeTrue())
		}

		vmcp := v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace)
		deleteFixture(k8sClient.Delete(ctx, vmcp))
		WaitForObjectDeletion(ctx, k8sClient, vmcp, e2eTimeout, e2ePollInterval)

		server := v1beta1test.NewMCPServer(backendName, defaultNamespace)
		deleteFixture(k8sClient.Delete(ctx, server))
		WaitForObjectDeletion(ctx, k8sClient, server, e2eTimeout, e2ePollInterval)

		group := &mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: groupName, Namespace: defaultNamespace}}
		deleteFixture(k8sClient.Delete(ctx, group))
		WaitForObjectDeletion(ctx, k8sClient, group, e2eTimeout, e2ePollInterval)

		deleteFixture(k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
		}))
		for _, name := range []string{delegateSecretName, redisPasswordSecretName, hmacSecretName, signingKeySecretName} {
			deleteFixture(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace}}))
		}
		cleanupRedis(redisName)
	})

	ginkgo.It("rolls a delegate scope change out against the existing Redis row and enforces the new scopes", func() {
		ginkgo.By("exchanging a token for the scope that is about to be dropped")
		status, body := exchangeDelegateForScope(vmcpName, issuer, droppedScope, signingPrivateKey)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), "exchange for %q before the edit: %v", droppedScope, body)
		gomega.Expect(delegatedScopes(body, signingPublicKey)).To(gomega.Equal([]any{droppedScope}))

		ginkgo.By("being refused the scope that is about to be added")
		status, body = exchangeDelegateForScope(vmcpName, issuer, addedScope, signingPrivateKey)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), "exchange for %q before the edit: %v", addedScope, body)
		gomega.Expect(body["error"]).To(gomega.Equal("invalid_scope"))
		gomega.Expect(body["error_description"]).To(gomega.ContainSubstring("not allowed to request scope '" + addedScope + "'"))

		ginkgo.By("confirming the delegate client row is stored in Redis, not in a memory fallback")
		clientKey := storage.DeriveKeyPrefix(defaultNamespace, vmcpName) + "client:" + delegateRedisClientID
		localPort, stopForward, err := portForwardToService(redisName, 6379)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer stopForward()
		rdb := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("localhost:%d", localPort), Password: redisPassword})
		defer func() { _ = rdb.Close() }()
		exists, err := rdb.Exists(ctx, clientKey).Result()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(exists).To(gomega.BeEquivalentTo(1), "Redis key %q must exist", clientKey)

		deployment := &appsv1.Deployment{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, deployment)).To(gomega.Succeed())
		generationBefore := deployment.Generation
		checksumBefore := deployment.Spec.Template.Annotations[checksum.RunConfigChecksumAnnotation]
		gomega.Expect(checksumBefore).NotTo(gomega.BeEmpty(), "the pod template must carry the config checksum")

		ginkgo.By(fmt.Sprintf("editing the delegate's scopes from [%s %s] to [%s %s]", keptScope, droppedScope, keptScope, addedScope))
		gomega.Eventually(func() error {
			vmcp := &mcpv1beta1.VirtualMCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, vmcp); err != nil {
				return err
			}
			vmcp.Spec.AuthServerConfig.DelegateClients[0].Scopes = []string{keptScope, addedScope}
			err := k8sClient.Update(ctx, vmcp)
			if err == nil || apierrors.IsConflict(err) {
				return err
			}
			return gomega.StopTrying("updating VirtualMCPServer delegate scopes").Wrap(err)
		}, e2eTimeout, e2ePollInterval).Should(gomega.Succeed())

		ginkgo.By("waiting for the rollout to complete")
		gomega.Eventually(func() error {
			return vmcpRolloutComplete(vmcpName, generationBefore, checksumBefore)
		}, e2eTimeout, e2ePollInterval).Should(gomega.Succeed(),
			func() string { return "VirtualMCPServer rollout did not complete; pod logs:\n" + vmcpPodLogs(vmcpName) })

		ginkgo.By("exchanging a token for the added scope")
		status, body = exchangeDelegateForScope(vmcpName, issuer, addedScope, signingPrivateKey)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK), "exchange for %q after the edit: %v", addedScope, body)
		gomega.Expect(delegatedScopes(body, signingPublicKey)).To(gomega.Equal([]any{addedScope}))

		ginkgo.By("being refused a token for the dropped scope")
		status, body = exchangeDelegateForScope(vmcpName, issuer, droppedScope, signingPrivateKey)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), "exchange for %q after the edit: %v", droppedScope, body)
		gomega.Expect(body["error"]).To(gomega.Equal("invalid_scope"))
		gomega.Expect(body["error_description"]).To(gomega.ContainSubstring("not allowed to request scope '" + droppedScope + "'"))
	})
})

// vmcpRolloutComplete reports why a VirtualMCPServer's Deployment has not
// finished rolling out a new pod template, or nil once it has: the template's
// config checksum differs from checksumBefore, the Deployment moved past
// generationBefore, the controller observed it, exactly one pod runs the new
// template and is available, and no old pod remains.
func vmcpRolloutComplete(vmcpName string, generationBefore int64, checksumBefore string) error {
	deployment := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: defaultNamespace}, deployment); err != nil {
		return err
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	status := deployment.Status
	switch {
	case deployment.Spec.Template.Annotations[checksum.RunConfigChecksumAnnotation] == checksumBefore:
		return fmt.Errorf("pod template config checksum has not changed from %q", checksumBefore)
	case deployment.Generation <= generationBefore:
		return fmt.Errorf("deployment generation %d has not moved past %d", deployment.Generation, generationBefore)
	case status.ObservedGeneration < deployment.Generation:
		return fmt.Errorf("deployment generation %d not yet observed (observed %d)", deployment.Generation, status.ObservedGeneration)
	case status.UpdatedReplicas != desired:
		return fmt.Errorf("updated replicas %d, want %d", status.UpdatedReplicas, desired)
	case status.Replicas != desired:
		return fmt.Errorf("total replicas %d, want %d (old pod still present)", status.Replicas, desired)
	case status.AvailableReplicas != desired:
		return fmt.Errorf("available replicas %d, want %d", status.AvailableReplicas, desired)
	}
	ready, err := countReadyPods(vmcpName)
	if err != nil {
		return err
	}
	if ready != int(desired) {
		return fmt.Errorf("ready pods %d, want %d", ready, desired)
	}
	return nil
}

// vmcpPodLogs returns the current and previous logs of every pod of the
// VirtualMCPServer, so a startup failure shows up in the test output.
func vmcpPodLogs(vmcpName string) string {
	pods, err := GetVirtualMCPServerPods(ctx, k8sClient, vmcpName, defaultNamespace)
	if err != nil {
		return fmt.Sprintf("listing pods: %v", err)
	}
	var out strings.Builder
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			for _, previous := range []bool{false, true} {
				logs, logErr := getPodLogs(ctx, defaultNamespace, pod.Name, container.Name, previous)
				if logErr != nil {
					logs = fmt.Sprintf("(unavailable: %v)", logErr)
				}
				fmt.Fprintf(&out, "--- pod %s container %s previous=%t phase=%s ---\n%s\n",
					pod.Name, container.Name, previous, pod.Status.Phase, logs)
			}
		}
	}
	return out.String()
}

// exchangeDelegateForScope is separate from the existing delegate-client helpers
// because it takes the requested scope and returns non-200 responses instead of
// asserting success. It port-forwards to the auth server, signs a subject
// token holding every scope the test uses, and requests a delegated token for
// one scope. It returns the HTTP status and decoded JSON body.
func exchangeDelegateForScope(
	vmcpName, issuer, scope string, signingKey *rsa.PrivateKey,
) (int, map[string]any) {
	localURL, cleanup := portForwardDelegateAuthServer(vmcpName)
	defer cleanup()

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signingKey}, (&jose.SignerOptions{}).WithType("JWT"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	now := time.Now()
	subjectToken, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: issuer, Subject: "delegate-redis-subject", Audience: jwt.Audience{issuer},
		Expiry: jwt.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(now),
	}).Claims(map[string]any{
		"client_id": "delegate-redis-original-client",
		"scope":     "profile email offline_access",
	}).Serialize()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	form := url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":      {subjectToken},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
		"audience":           {issuer},
		"scope":              {scope},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, localURL+"/oauth/token", strings.NewReader(form.Encode()))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(delegateRedisClientID, delegateRedisClientSecret)

	response, err := http.DefaultClient.Do(request)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() { _, _ = io.Copy(io.Discard, response.Body); _ = response.Body.Close() }()
	var body map[string]any
	gomega.Expect(json.NewDecoder(response.Body).Decode(&body)).To(gomega.Succeed())
	return response.StatusCode, body
}

// delegatedScopes returns the scp claim of the access token in an exchange response.
func delegatedScopes(body map[string]any, signingKey *rsa.PublicKey) any {
	token, ok := body["access_token"].(string)
	gomega.Expect(ok).To(gomega.BeTrue(), "response has no access_token: %v", body)
	return verifiedJWTClaims(token, signingKey)["scp"]
}
