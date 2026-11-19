// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

const (
	// spiffeClientScope is the scope every SPIFFE e2e association grants and
	// every SPIFFE token request asks for.
	spiffeClientScope = "mcp.read"
)

// Serial: each SPIFFE e2e suite deploys its own SPIRE instance with its own
// Workload API socket directory, but the suites stay serial until a parallel
// run has been verified on kind.
var _ = ginkgo.Describe("MCPServer SPIFFE client credentials", ginkgo.Ordered, ginkgo.Serial, func() {
	const (
		timeout           = 5 * time.Minute
		pollInterval      = 2 * time.Second
		proxyPort         = 8080
		authServerTLSPort = 8443
	)

	var (
		asName                            string
		asServiceName                     string
		authConfigName                    string
		clientName                        string
		unmatchedClientName               string
		noAssociationClientName           string
		clientServiceAccountName          string
		unmatchedClientServiceAccountName string
		noAssociationServiceAccountName   string
		clientID                          string
		clientSPIFFEID                    string
		dexName                           string
		dexSecretName                     string
		hmacSecretName                    string
		listenerSecretName                string
		publicCAConfigMapName             string
		oidcConfigName                    string
		signingSecretName                 string
		spireName                         string
		issuer                            string
		resource                          string
		signingKey                        *rsa.PrivateKey
		listenerCA                        *x509.Certificate
		listenerCAKey                     *rsa.PrivateKey
		spireInfo                         *SPIREInfo
		cleanupDex                        func()
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		asName = "spiffe-as-" + suffix
		asServiceName = "mcp-" + asName + "-proxy"
		authConfigName = "spiffe-auth-" + suffix
		clientName = "spiffe-client-" + suffix
		unmatchedClientName = "spiffe-unmatched-client-" + suffix
		noAssociationClientName = "spiffe-noassoc-client-" + suffix
		clientServiceAccountName = "spiffe-client-sa-" + suffix
		unmatchedClientServiceAccountName = "spiffe-unmatched-client-sa-" + suffix
		noAssociationServiceAccountName = "spiffe-noassoc-client-sa-" + suffix
		clientID = "spiffe-client-" + suffix
		dexName = "spiffe-dex-" + suffix
		dexSecretName = "spiffe-dex-secret-" + suffix
		hmacSecretName = "spiffe-hmac-" + suffix
		listenerSecretName = "spiffe-listener-" + suffix
		publicCAConfigMapName = "spiffe-ca-" + suffix
		oidcConfigName = "spiffe-oidc-" + suffix
		signingSecretName = "spiffe-signing-" + suffix
		spireName = "spiffe-" + suffix
		issuer = fmt.Sprintf("https://%s.%s.svc.cluster.local:%d", asServiceName, defaultNamespace, authServerTLSPort)
		resource = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/mcp", asServiceName, defaultNamespace, proxyPort)
		clientSPIFFEID = fmt.Sprintf(
			"spiffe://%s/workload/%s/%s",
			spireTrustDomain,
			defaultNamespace,
			clientServiceAccountName,
		)

		ginkgo.By("creating the attested, unmatched and association-less client ServiceAccounts")
		for _, serviceAccountName := range []string{
			clientServiceAccountName, unmatchedClientServiceAccountName, noAssociationServiceAccountName,
		} {
			gomega.Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: defaultNamespace},
			})).To(gomega.Succeed())
		}

		ginkgo.By("deploying SPIRE and verifying its published trust bundle")
		spireInfo = deploySPIRE(ctx, k8sClient, spireName, defaultNamespace, timeout, pollInterval)
		waitForSPIFFEBundle(spireInfo, timeout, pollInterval)

		serverPodName := ""
		gomega.Eventually(func() error {
			var err error
			serverPodName, err = findPodName(
				ctx,
				k8sClient,
				defaultNamespace,
				map[string]string{"app": spireInfo.ServerDeploymentName},
			)
			return err
		}, timeout, pollInterval).Should(gomega.Succeed())
		// The association-less workload is registered too, so it holds a valid
		// SVID and its token requests fail at the authorization server.
		for _, serviceAccountName := range []string{clientServiceAccountName, noAssociationServiceAccountName} {
			gomega.Expect(CreateSPIREWorkloadEntries(
				ctx,
				k8sClient,
				defaultNamespace,
				serverPodName,
				spireName+"-agent",
				defaultNamespace,
				serviceAccountName,
			)).To(gomega.Succeed())
		}

		ginkgo.By("creating listener TLS material and a client-only public CA bundle")
		var caPEM, certPEM, keyPEM []byte
		caPEM, certPEM, keyPEM, listenerCA, listenerCAKey = createSPIFFEListenerCertificate(asServiceName, defaultNamespace)
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: listenerSecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM},
		})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: publicCAConfigMapName, Namespace: defaultNamespace},
			Data:       map[string]string{"ca.crt": string(caPEM)},
		})).To(gomega.Succeed())

		signingKey = generateSPIFFERSAKey()
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: signingSecretName, Namespace: defaultNamespace},
			Data: map[string][]byte{
				"private-key": pem.EncodeToMemory(&pem.Block{
					Type:  "RSA PRIVATE KEY",
					Bytes: x509.MarshalPKCS1PrivateKey(signingKey),
				}),
			},
		})).To(gomega.Succeed())
		hmac := make([]byte, 32)
		_, err := rand.Read(hmac)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: hmacSecretName, Namespace: defaultNamespace},
			Data:       map[string][]byte{"hmac": hmac},
		})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: dexSecretName, Namespace: defaultNamespace},
			StringData: map[string]string{"client-secret": "authserver-secret"},
		})).To(gomega.Succeed())

		ginkgo.By("deploying Dex as the embedded authorization server upstream")
		_, cleanupDex = deployDex(ctx, k8sClient, dexName, resource+"/oauth/callback")

		ginkgo.By("creating the file-backed embedded authorization server configuration")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer: issuer,
					// With upstream swap enabled the proxy answers 401 to client
					// credentials tokens (they carry no upstream session), so the
					// proxy specs run with injection disabled.
					DisableUpstreamTokenInjection: true,
					SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{{
						Name: signingSecretName,
						Key:  "private-key",
					}},
					HMACSecretRefs: []mcpv1beta1.SecretKeyRef{{Name: hmacSecretName, Key: "hmac"}},
					SPIFFETrustDomains: []mcpv1beta1.SPIFFETrustDomainConfig{{
						Name:        "spire",
						TrustDomain: spireInfo.TrustDomain,
						Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{
							mcpv1beta1.SPIFFEAuthenticationMethodX509,
							mcpv1beta1.SPIFFEAuthenticationMethodJWT,
						},
						BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
							Type: mcpv1beta1.SPIFFEBundleSourceTypeFile,
							File: &mcpv1beta1.SPIFFEFileBundleSourceConfig{
								ConfigMapName: spireInfo.BundleConfigMapName,
								ConfigMapKey:  spireInfo.BundleConfigMapKey,
							},
						},
					}},
					InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientConfig{{
						TrustDomainRef:   "spire",
						PrincipalPattern: clientSPIFFEID,
						ClientID:         clientID,
						Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{
							mcpv1beta1.SPIFFEAuthenticationMethodX509,
							mcpv1beta1.SPIFFEAuthenticationMethodJWT,
						},
						Resources:  []string{resource},
						Scopes:     []string{spiffeClientScope},
						GrantTypes: []string{"client_credentials"},
					}}},
					TLSListener: &mcpv1beta1.TLSListenerConfig{
						CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: listenerSecretName, Key: "tls.crt"},
						PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: listenerSecretName, Key: "tls.key"},
					},
					UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{{
						Name: "dex",
						Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
						OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
							AuthorizationEndpoint: fmt.Sprintf("http://%s.%s.svc.cluster.local:5556/auth", dexName, defaultNamespace),
							TokenEndpoint:         fmt.Sprintf("http://%s.%s.svc.cluster.local:5556/token", dexName, defaultNamespace),
							ClientID:              "vmcp-authserver",
							ClientSecretRef:       &mcpv1beta1.SecretKeyRef{Name: dexSecretName, Key: "client-secret"},
							Scopes:                []string{"openid"},
							InsecureAllowHTTP:     true,
							AllowPrivateIPs:       true,
						},
					}},
				},
			},
		})).To(gomega.Succeed())

		ginkgo.By("creating an MCPOIDCConfig trusting the embedded auth server's own issuer")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{
					// The proxy's incoming-request validator must trust the SAME
					// self-issued issuer the embedded auth server signs tokens as.
					// The issuer is HTTPS (X.509-SVID auth requires listener TLS),
					// so JWKS fetch needs the same CA the client already trusts.
					Issuer:  issuer,
					JWKSURL: issuer + "/.well-known/jwks.json",
					CABundleRef: &mcpv1beta1.CABundleSource{
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: publicCAConfigMapName},
						},
					},
					JWKSAllowPrivateIP: true,
				},
			},
		})).To(gomega.Succeed())

		ginkgo.By("creating the authorization server and attested ToolHive client")
		asServer := newSPIFFEMCPServer(
			asName,
			nil,
			&mcpv1beta1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: authConfigName},
			nil,
		)
		asServer.Spec.OIDCConfigRef = &mcpv1beta1.MCPOIDCConfigReference{
			// Scopes becomes the embedded AS's scopesSupported (see
			// AddEmbeddedAuthServerConfigOptions in controllerutil/authserver.go);
			// it defaults to ["openid", "offline_access"] when empty, which does
			// not include the custom scope our SPIFFE association grants.
			Name: oidcConfigName, Audience: resource, ResourceURL: resource,
			Scopes: []string{"openid", spiffeClientScope},
		}
		gomega.Expect(k8sClient.Create(ctx, asServer)).To(gomega.Succeed())
		socketVolume, socketMount, socketEnv, err := SPIREWorkloadAPIVolume("spire-socket", spireInfo.AgentSocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clientTemplate := corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "mcp"},
					{
						Name:  "spiffe-client",
						Image: images.SPIFFEClientImage,
						// kind.local isn't a resolvable registry — the image only exists
						// because `kind load docker-image` injected it directly into
						// containerd. Kubernetes defaults :latest-tagged images to
						// imagePullPolicy: Always, which tries (and fails) a real
						// network pull regardless, so this must be explicit.
						ImagePullPolicy: corev1.PullNever,
						Args:            []string{"hold"},
						Env:             []corev1.EnvVar{socketEnv},
						VolumeMounts: []corev1.VolumeMount{
							socketMount,
							{Name: "as-ca", MountPath: "/var/run/spiffe-ca", ReadOnly: true},
						},
					},
				},
				Volumes: []corev1.Volume{
					socketVolume,
					{
						Name: "as-ca",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: publicCAConfigMapName},
						}},
					},
				},
			},
		}
		rawTemplate, err := json.Marshal(clientTemplate)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(k8sClient.Create(ctx, newSPIFFEMCPServer(
			clientName,
			&clientServiceAccountName,
			nil,
			&runtime.RawExtension{Raw: rawTemplate},
		))).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, newSPIFFEMCPServer(
			noAssociationClientName,
			&noAssociationServiceAccountName,
			nil,
			&runtime.RawExtension{Raw: rawTemplate},
		))).To(gomega.Succeed())
		unmatchedTemplate := clientTemplate.DeepCopy()
		unmatchedTemplate.Spec.Containers[1].VolumeMounts = unmatchedTemplate.Spec.Containers[1].VolumeMounts[:1]
		unmatchedTemplate.Spec.Volumes = unmatchedTemplate.Spec.Volumes[:1]
		unmatchedRawTemplate, err := json.Marshal(unmatchedTemplate)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(k8sClient.Create(ctx, newSPIFFEMCPServer(
			unmatchedClientName,
			&unmatchedClientServiceAccountName,
			nil,
			&runtime.RawExtension{Raw: unmatchedRawTemplate},
		))).To(gomega.Succeed())

		for _, name := range []string{asName, clientName, noAssociationClientName, unmatchedClientName} {
			testutil.WaitForMCPServerRunning(ctx, k8sClient, name, defaultNamespace, timeout, pollInterval)
		}
	})

	ginkgo.AfterAll(func() {
		for _, name := range []string{unmatchedClientName, noAssociationClientName, clientName, asName} {
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
			})
		}
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: defaultNamespace},
		})
		for _, serviceAccountName := range []string{
			unmatchedClientServiceAccountName, noAssociationServiceAccountName, clientServiceAccountName,
		} {
			_ = k8sClient.Delete(ctx, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: defaultNamespace},
			})
		}
		for _, name := range []string{dexSecretName, hmacSecretName, listenerSecretName, signingSecretName} {
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
			})
		}
		_ = k8sClient.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: publicCAConfigMapName, Namespace: defaultNamespace},
		})
		if cleanupDex != nil {
			cleanupDex()
		}
	})

	ginkgo.It("attests the registered ToolHive-managed workload and rejects an unmatched one", func() {
		registeredPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		gomega.Eventually(func() (string, error) {
			identity, err := fetchSPIFFEIdentity(registeredPod.Name, spireInfo.AgentSocketPath, issuer)
			return identity.SPIFFEID, err
		}, timeout, pollInterval).Should(gomega.Equal(clientSPIFFEID))

		unmatchedPod := findSPIFFEClientBackendPod(unmatchedClientServiceAccountName)
		_, err := fetchSPIFFEIdentity(unmatchedPod.Name, spireInfo.AgentSocketPath, issuer)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("no identity issued"))
	})

	ginkgo.It("issues equivalent X.509-SVID and JWT-SVID client credentials tokens without static client credentials", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		asPod := findMCPServerProxyPod(asName)
		assertSPIFFEClientPod(clientPod, publicCAConfigMapName, spireInfo.AgentSocketPath)
		assertSPIFFEASPod(asPod, spireInfo.BundleConfigMapName)

		identity, err := fetchSPIFFEIdentity(clientPod.Name, spireInfo.AgentSocketPath, issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(identity.SPIFFEID).To(gomega.Equal(clientSPIFFEID))

		x509Claims := verifySPIFFEAccessToken(requestSPIFFEToken(clientPod.Name, "x509", spireInfo.AgentSocketPath, issuer, clientID, resource), &signingKey.PublicKey)
		jwtClaims := verifySPIFFEAccessToken(requestSPIFFEToken(clientPod.Name, "jwt", spireInfo.AgentSocketPath, issuer, clientID, resource), &signingKey.PublicKey)
		assertEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims, issuer, clientSPIFFEID, clientID, resource)
	})

	ginkgo.It("rejects a token request from an attested workload that matches no association", func() {
		noAssociationPod := findSPIFFEClientBackendPod(noAssociationServiceAccountName)
		noAssociationSPIFFEID := fmt.Sprintf(
			"spiffe://%s/workload/%s/%s", spireTrustDomain, defaultNamespace, noAssociationServiceAccountName,
		)
		gomega.Eventually(func() (string, error) {
			identity, err := fetchSPIFFEIdentity(noAssociationPod.Name, spireInfo.AgentSocketPath, issuer)
			return identity.SPIFFEID, err
		}, timeout, pollInterval).Should(gomega.Equal(noAssociationSPIFFEID),
			"the workload must hold a valid SVID so the rejection comes from the authorization server")

		for _, method := range []string{"x509", "jwt"} {
			expectSPIFFETokenRejected(noAssociationPod.Name, method, spireInfo.AgentSocketPath, issuer,
				clientID, resource, http.StatusUnauthorized, "invalid_client")
		}
	})

	ginkgo.It("rejects a token request naming a client_id that has no association", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		for _, method := range []string{"x509", "jwt"} {
			expectSPIFFETokenRejected(clientPod.Name, method, spireInfo.AgentSocketPath, issuer,
				"spiffe-unknown-client", resource, http.StatusUnauthorized, "invalid_client")
		}
	})

	ginkgo.It("rejects a token request for a resource outside the association with invalid_target", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		for _, method := range []string{"x509", "jwt"} {
			expectSPIFFETokenRejected(clientPod.Name, method, spireInfo.AgentSocketPath, issuer,
				clientID, "https://resource.example/forbidden", http.StatusBadRequest, "invalid_target")
		}
	})

	// The forged certificate satisfies every leaf-profile rule and names the
	// registered SPIFFE ID, so only chain verification against the trust bundle
	// can reject it: matching the ID text is not enough.
	ginkgo.It("rejects a self-signed X.509 client certificate that carries the registered SPIFFE ID", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		expectForgedSPIFFETokenRejected(clientPod.Name, "x509", issuer, clientSPIFFEID, clientID, resource)
	})

	// The forged assertion names a key ID the bundle contains, has the
	// registered SPIFFE ID as sub and the issuer as its sole audience, but is
	// signed by a throwaway key, so only signature verification against the
	// bundle key can reject it.
	ginkgo.It("rejects a JWT assertion signed with the wrong key for a key ID the trust bundle contains", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		var bundleKeyID string
		gomega.Eventually(func() (string, error) {
			identity, err := fetchSPIFFEIdentity(clientPod.Name, spireInfo.AgentSocketPath, issuer)
			bundleKeyID = identity.JWTKeyID
			return identity.SPIFFEID, err
		}, timeout, pollInterval).Should(gomega.Equal(clientSPIFFEID))
		// A missing kid is also invalid_client, so prove the server accepts a
		// genuine JWT-SVID under this kid before expecting the forged one to fail.
		requestSPIFFEToken(clientPod.Name, "jwt", spireInfo.AgentSocketPath, issuer, clientID, resource)
		expectForgedSPIFFETokenRejected(clientPod.Name, "jwt", issuer, clientSPIFFEID, clientID, resource,
			"--key-id", bundleKeyID)
	})

	// Upstream swap is disabled for this suite: with it enabled the proxy refuses
	// SPIFFE-issued tokens (no upstream session), which is intentionally not
	// covered here. With injection disabled there is no swap step, so the tampered
	// token is refused by the token validator.
	ginkgo.It("accepts X.509-SVID and JWT-SVID issued tokens at the proxy with upstream token injection disabled "+
		"and rejects a tampered one", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		x509Token := requestSPIFFEToken(clientPod.Name, "x509", spireInfo.AgentSocketPath, issuer, clientID, resource)
		jwtToken := requestSPIFFEToken(clientPod.Name, "jwt", spireInfo.AgentSocketPath, issuer, clientID, resource)

		for name, token := range map[string]string{"X.509-issued": x509Token, "JWT-issued": jwtToken} {
			gomega.Eventually(func() error {
				status, body, err := postMCPInitialize(resource, token)
				if err != nil {
					return err
				}
				if status != http.StatusOK || !strings.Contains(body, `"protocolVersion"`) {
					return fmt.Errorf("initialize returned HTTP %d: %s", status, body)
				}
				return nil
			}, timeout, pollInterval).Should(gomega.Succeed(), "the proxy must accept the %s token", name)
		}

		tamperedToken := tamperJWTSignature(x509Token)
		gomega.Eventually(func() error {
			status, body, err := postMCPInitialize(resource, tamperedToken)
			if err != nil {
				return err
			}
			if status != http.StatusUnauthorized {
				return fmt.Errorf("tampered token returned HTTP %d: %s", status, body)
			}
			return nil
		}, timeout, pollInterval).Should(gomega.Succeed())
	})

	ginkgo.It("rotates SPIRE local authorities without restarting the authorization server or client", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		asPod := findMCPServerProxyPod(asName)
		clientSnapshot := podSnapshot(clientPod)
		asSnapshot := podSnapshot(asPod)
		preRotationIdentity, err := fetchSPIFFEIdentity(clientPod.Name, spireInfo.AgentSocketPath, issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(preRotationIdentity.SPIFFEID).To(gomega.Equal(clientSPIFFEID))
		originalBundle := getSPIFFEBundleData(spireInfo)

		serverPodName := ""
		gomega.Eventually(func() error {
			var findErr error
			serverPodName, findErr = findPodName(ctx, k8sClient, defaultNamespace, map[string]string{"app": spireInfo.ServerDeploymentName})
			return findErr
		}, timeout, pollInterval).Should(gomega.Succeed())
		jwtAuthorityID, err := RotateSPIRELocalAuthorities(ctx, defaultNamespace, serverPodName)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(jwtAuthorityID).NotTo(gomega.Equal(preRotationIdentity.JWTKeyID))

		waitForChangedSPIFFEBundle(spireInfo, originalBundle, timeout, pollInterval)
		assertPodSnapshot(findMCPServerProxyPod(asName), asSnapshot)

		gomega.Eventually(func() error {
			identity, identityErr := fetchSPIFFEIdentity(clientPod.Name, spireInfo.AgentSocketPath, issuer)
			if identityErr != nil {
				return identityErr
			}
			if identity.X509LeafSerial == preRotationIdentity.X509LeafSerial {
				return fmt.Errorf("client X.509-SVID serial has not rotated")
			}
			// SPIRE uses a JWT authority's ID as the kid of the JWT-SVIDs it signs.
			// The agent may keep serving a cached JWT-SVID signed by the previous
			// authority until it refreshes it, hence the long Eventually.
			if identity.JWTKeyID == preRotationIdentity.JWTKeyID {
				return fmt.Errorf("client JWT-SVID key ID has not rotated")
			}
			if identity.JWTKeyID != jwtAuthorityID {
				return fmt.Errorf("client JWT-SVID key ID %q is not the activated JWT authority %q", identity.JWTKeyID, jwtAuthorityID)
			}
			x509Token, tokenErr := requestSPIFFETokenE(clientPod.Name, "x509", spireInfo.AgentSocketPath, issuer, clientID, resource)
			if tokenErr != nil {
				return tokenErr
			}
			x509Claims, claimsErr := verifySPIFFEAccessTokenE(x509Token, &signingKey.PublicKey)
			if claimsErr != nil {
				return claimsErr
			}
			jwtToken, tokenErr := requestSPIFFETokenE(clientPod.Name, "jwt", spireInfo.AgentSocketPath, issuer, clientID, resource)
			if tokenErr != nil {
				return tokenErr
			}
			jwtClaims, claimsErr := verifySPIFFEAccessTokenE(jwtToken, &signingKey.PublicKey)
			if claimsErr != nil {
				return claimsErr
			}
			return validateEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims, issuer, clientSPIFFEID, clientID, resource)
		}, 4*time.Minute, pollInterval).Should(gomega.Succeed())
		assertPodSnapshot(findMCPServerProxyPod(asName), asSnapshot)
		assertPodSnapshot(findSPIFFEClientBackendPod(clientServiceAccountName), clientSnapshot)
	})

	ginkgo.It("exposes the TLS listener on a dedicated https-auth port and serves the MCP endpoint over plain HTTP", func() {
		svc := &corev1.Service{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: asServiceName, Namespace: defaultNamespace}, svc)).To(gomega.Succeed())

		var httpPort, httpsAuthPort *corev1.ServicePort
		for i := range svc.Spec.Ports {
			switch svc.Spec.Ports[i].Name {
			case "http":
				httpPort = &svc.Spec.Ports[i]
			case ctrlutil.AuthServerTLSListenerPortName:
				httpsAuthPort = &svc.Spec.Ports[i]
			}
		}
		gomega.Expect(httpPort).NotTo(gomega.BeNil(), "proxy Service must still expose a plain HTTP port")
		gomega.Expect(httpsAuthPort).NotTo(gomega.BeNil(), "proxy Service must expose the dedicated TLS listener port")
		gomega.Expect(httpsAuthPort.Port).To(gomega.Equal(int32(authServerTLSPort)))
		gomega.Expect(httpsAuthPort.AppProtocol).NotTo(gomega.BeNil())
		gomega.Expect(*httpsAuthPort.AppProtocol).To(gomega.Equal("https"))

		deployment := &appsv1.Deployment{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: asName, Namespace: defaultNamespace}, deployment)).To(gomega.Succeed())
		probeCount := 0
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, probe := range []*corev1.Probe{container.LivenessProbe, container.ReadinessProbe, container.StartupProbe} {
				if probe == nil || probe.HTTPGet == nil {
					continue
				}
				probeCount++
				// An empty scheme defaults to HTTP.
				gomega.Expect(probe.HTTPGet.Scheme).To(gomega.BeElementOf(corev1.URISchemeHTTP, corev1.URIScheme("")),
					"auth server probes must use plain HTTP")
				probePort := resolveProbePort(container, probe.HTTPGet.Port)
				gomega.Expect(probePort).NotTo(gomega.BeZero(), "probe port %s must resolve to a container port", probe.HTTPGet.Port.String())
				gomega.Expect(probePort).NotTo(gomega.Equal(int32(authServerTLSPort)),
					"auth server probes must not target the TLS listener port")
			}
		}
		gomega.Expect(probeCount).To(gomega.BeNumerically(">", 0), "the proxy must define at least one HTTP probe")

		var headers string
		gomega.Eventually(func() (err error) {
			headers, err = GetHTTPResponseHeaders(ctx, k8sClient, defaultNamespace,
				fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/mcp", asServiceName, defaultNamespace, proxyPort))
			return err
		}, timeout, pollInterval).Should(gomega.Succeed())
		gomega.Expect(headers).To(gomega.ContainSubstring(" 401 "))
		gomega.Expect(strings.ToLower(headers)).To(gomega.ContainSubstring("www-authenticate"))
	})

	ginkgo.It("rotates the listener TLS certificate without restarting the authorization server", func() {
		asSnapshot := podSnapshot(findMCPServerProxyPod(asName))

		host := fmt.Sprintf("%s.%s.svc.cluster.local", asServiceName, defaultNamespace)
		var originalSerial string
		gomega.Eventually(func() (err error) {
			originalSerial, err = GetTLSCertificateSerial(ctx, k8sClient, defaultNamespace, host, authServerTLSPort, publicCAConfigMapName)
			return err
		}, timeout, pollInterval).Should(gomega.Succeed())

		newCertPEM, newKeyPEM := createSPIFFEListenerLeafCertificate(listenerCA, listenerCAKey, asServiceName, defaultNamespace, 3)
		secret := &corev1.Secret{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: listenerSecretName, Namespace: defaultNamespace}, secret)).To(gomega.Succeed())
		secret.Data["tls.crt"] = newCertPEM
		secret.Data["tls.key"] = newKeyPEM
		gomega.Expect(k8sClient.Update(ctx, secret)).To(gomega.Succeed())

		gomega.Eventually(func() (string, error) {
			return GetTLSCertificateSerial(ctx, k8sClient, defaultNamespace, host, authServerTLSPort, publicCAConfigMapName)
		}, 4*time.Minute, 10*time.Second).ShouldNot(gomega.Equal(originalSerial))

		assertPodSnapshot(findMCPServerProxyPod(asName), asSnapshot)

		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		token := requestSPIFFEToken(clientPod.Name, "x509", spireInfo.AgentSocketPath, issuer, clientID, resource)
		verifySPIFFEAccessToken(token, &signingKey.PublicKey)
	})
})

func newSPIFFEMCPServer(
	name string,
	serviceAccount *string,
	authServerRef *mcpv1beta1.AuthServerRef,
	podTemplateSpec *runtime.RawExtension,
) *mcpv1beta1.MCPServer {
	return &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:           images.YardstickServerImage,
			Transport:       "streamable-http",
			ProxyPort:       8080,
			MCPPort:         8080,
			ServiceAccount:  serviceAccount,
			AuthServerRef:   authServerRef,
			PodTemplateSpec: podTemplateSpec,
			// Debug logs make SPIFFE client-auth decisions visible in failure dumps.
			ResourceOverrides: &mcpv1beta1.ResourceOverrides{
				ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
					Env: []mcpv1beta1.EnvVar{{Name: "TOOLHIVE_DEBUG", Value: "true"}},
				},
			},
		},
	}
}

type spiffeIdentity struct {
	SPIFFEID       string    `json:"spiffe_id"`
	X509LeafSerial string    `json:"x509_leaf_serial"`
	JWTExpiry      time.Time `json:"jwt_expiry"`
	JWTKeyID       string    `json:"jwt_key_id"`
}

type podState struct {
	uid          types.UID
	restartCount map[string]int32
}

func waitForSPIFFEBundle(spireInfo *SPIREInfo, timeout, interval time.Duration) {
	path := filepath.Join(ginkgo.GinkgoT().TempDir(), "bundle.json")
	gomega.Eventually(func() error {
		bundleData, err := getSPIFFEBundleDataE(spireInfo)
		if err != nil {
			return err
		}
		return validateSPIFFEBundle(bundleData, spireInfo.TrustDomain, path)
	}, timeout, interval).Should(gomega.Succeed())
}

func waitForChangedSPIFFEBundle(spireInfo *SPIREInfo, originalBundle string, timeout, interval time.Duration) {
	path := filepath.Join(ginkgo.GinkgoT().TempDir(), "bundle.json")
	gomega.Eventually(func() error {
		bundleData, err := getSPIFFEBundleDataE(spireInfo)
		if err != nil {
			return err
		}
		if bundleData == originalBundle {
			return fmt.Errorf("SPIRE bundle ConfigMap has not changed")
		}
		return validateSPIFFEBundle(bundleData, spireInfo.TrustDomain, path)
	}, timeout, interval).Should(gomega.Succeed())
}

func getSPIFFEBundleData(spireInfo *SPIREInfo) string {
	bundleData, err := getSPIFFEBundleDataE(spireInfo)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return bundleData
}

func getSPIFFEBundleDataE(spireInfo *SPIREInfo) (string, error) {
	configMap := &corev1.ConfigMap{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name: spireInfo.BundleConfigMapName, Namespace: defaultNamespace,
	}, configMap); err != nil {
		return "", err
	}
	bundleData := configMap.Data[spireInfo.BundleConfigMapKey]
	if bundleData == "" {
		return "", fmt.Errorf("SPIRE bundle ConfigMap has no bundle data")
	}
	return bundleData, nil
}

func validateSPIFFEBundle(bundleData, trustDomain, path string) error {
	if err := os.WriteFile(path, []byte(bundleData), 0o600); err != nil {
		return err
	}
	parsedTrustDomain, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil {
		return err
	}
	bundle, err := spiffebundle.Load(parsedTrustDomain, path)
	if err != nil {
		return err
	}
	if len(bundle.X509Authorities()) == 0 || len(bundle.JWTAuthorities()) == 0 {
		return fmt.Errorf("SPIRE bundle does not contain both X.509 and JWT authorities")
	}
	return nil
}

func fetchSPIFFEIdentity(podName, socketPath, issuer string) (spiffeIdentity, error) {
	output, err := testutil.ExecutePodCommand(ctx, defaultNamespace, podName, "spiffe-client", []string{
		"/ko-app/spiffe-client", "identity", "--socket", "unix://" + socketPath, "--jwt-audience", issuer,
	})
	if err != nil {
		return spiffeIdentity{}, fmt.Errorf("fetch SPIFFE client identity: %w", err)
	}
	identity := spiffeIdentity{}
	if err := json.Unmarshal([]byte(output), &identity); err != nil {
		return spiffeIdentity{}, fmt.Errorf("parse SPIFFE client identity: %w", err)
	}
	if identity.SPIFFEID == "" || identity.X509LeafSerial == "" || identity.JWTExpiry.IsZero() || identity.JWTKeyID == "" {
		return spiffeIdentity{}, fmt.Errorf("SPIFFE client identity response is incomplete")
	}
	return identity, nil
}

func podSnapshot(pod *corev1.Pod) podState {
	restartCount := make(map[string]int32, len(pod.Status.ContainerStatuses))
	for _, status := range pod.Status.ContainerStatuses {
		restartCount[status.Name] = status.RestartCount
	}
	return podState{uid: pod.UID, restartCount: restartCount}
}

func assertPodSnapshot(pod *corev1.Pod, expected podState) {
	gomega.Expect(pod.UID).To(gomega.Equal(expected.uid))
	gomega.Expect(podSnapshot(pod).restartCount).To(gomega.Equal(expected.restartCount))
}

func findSPIFFEClientBackendPod(serviceAccountName string) *corev1.Pod {
	pod := &corev1.Pod{}
	gomega.Eventually(func() error {
		pods := &corev1.PodList{}
		if err := k8sClient.List(ctx, pods, client.InNamespace(defaultNamespace)); err != nil {
			return err
		}
		for i := range pods.Items {
			candidate := &pods.Items[i]
			if candidate.DeletionTimestamp == nil && candidate.Status.Phase == corev1.PodRunning &&
				candidate.Spec.ServiceAccountName == serviceAccountName && hasContainer(candidate, "spiffe-client") && isPodReady(candidate) {
				*pod = *candidate
				return nil
			}
		}
		return fmt.Errorf("no ready backend pod uses ServiceAccount %q and the SPIFFE sidecar", serviceAccountName)
	}, e2eTimeout, e2ePollInterval).Should(gomega.Succeed())
	return pod
}

func findMCPServerProxyPod(serverName string) *corev1.Pod {
	pod := &corev1.Pod{}
	gomega.Eventually(func() error {
		deployment := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name: serverName, Namespace: defaultNamespace,
		}, deployment); err != nil {
			return err
		}
		pods := &corev1.PodList{}
		if err := k8sClient.List(ctx, pods,
			client.InNamespace(defaultNamespace),
			client.MatchingLabels(deployment.Spec.Selector.MatchLabels),
		); err != nil {
			return err
		}
		for i := range pods.Items {
			candidate := &pods.Items[i]
			if candidate.DeletionTimestamp == nil && hasContainer(candidate, "toolhive") {
				*pod = *candidate
				return nil
			}
		}
		return fmt.Errorf("no proxy pod found for MCPServer %q", serverName)
	}, e2eTimeout, e2ePollInterval).Should(gomega.Succeed())
	return pod
}

func hasContainer(pod *corev1.Pod, name string) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return true
		}
	}
	return false
}

func requestSPIFFEToken(podName, method, socketPath, issuer, clientID, resource string) string {
	token, err := requestSPIFFETokenE(podName, method, socketPath, issuer, clientID, resource)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return token
}

func requestSPIFFETokenE(podName, method, socketPath, issuer, clientID, resource string) (string, error) {
	token, err := testutil.ExecutePodCommand(ctx, defaultNamespace, podName, "spiffe-client", []string{
		"/ko-app/spiffe-client",
		"token",
		"--method", method,
		"--socket", "unix://" + socketPath,
		"--issuer", issuer,
		"--client-id", clientID,
		"--resource", resource,
		"--scope", spiffeClientScope,
		"--server-ca-file", "/var/run/spiffe-ca/ca.crt",
	})
	if err != nil {
		return "", fmt.Errorf("request %s SPIFFE client credentials token: %w", method, err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("request %s SPIFFE client credentials token returned no token", method)
	}
	return token, nil
}

// expectSPIFFETokenRejected asserts that the spiffe-client token request fails
// at the token endpoint with the given HTTP status and OAuth error code.
func expectSPIFFETokenRejected(
	podName, method, socketPath, issuer, clientID, resource string, status int, oauthError string,
) {
	_, err := requestSPIFFETokenE(podName, method, socketPath, issuer, clientID, resource)
	expectTokenEndpointRejection(err, method, status, oauthError)
}

// expectForgedSPIFFETokenRejected asserts that a token request presenting a
// credential the trust bundle did not issue fails with invalid_client.
func expectForgedSPIFFETokenRejected(podName, method, issuer, spiffeID, clientID, resource string, extraArgs ...string) {
	_, err := testutil.ExecutePodCommand(ctx, defaultNamespace, podName, "spiffe-client", append([]string{
		"/ko-app/spiffe-client",
		"forged-token",
		"--method", method,
		"--spiffe-id", spiffeID,
		"--issuer", issuer,
		"--client-id", clientID,
		"--resource", resource,
		"--scope", spiffeClientScope,
		"--server-ca-file", "/var/run/spiffe-ca/ca.crt",
	}, extraArgs...))
	expectTokenEndpointRejection(err, "forged "+method, http.StatusUnauthorized, "invalid_client")
}

func expectTokenEndpointRejection(err error, label string, status int, oauthError string) {
	gomega.ExpectWithOffset(2, err).To(gomega.HaveOccurred(), "%s token request must be rejected", label)
	gomega.ExpectWithOffset(2, err.Error()).To(gomega.And(
		gomega.ContainSubstring(fmt.Sprintf("token endpoint returned HTTP %d", status)),
		gomega.ContainSubstring(fmt.Sprintf(`"error":%q`, oauthError)),
	), "%s token request", label)
}

// postMCPInitialize sends an MCP initialize request with a bearer token to
// mcpURL from inside the cluster.
func postMCPInitialize(mcpURL, token string) (int, string, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spiffe-e2e", "version": "1.0"},
		},
	})
	if err != nil {
		return 0, "", err
	}
	return PostHTTPInCluster(ctx, k8sClient, defaultNamespace, mcpURL, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
		"Accept":        "application/json, text/event-stream",
	}, string(body))
}

// tamperJWTSignature flips the bits of the first signature byte of a compact JWS.
func tamperJWTSignature(token string) string {
	parts := strings.Split(token, ".")
	gomega.ExpectWithOffset(1, parts).To(gomega.HaveLen(3), "access token must be a compact JWS")
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, signature).NotTo(gomega.BeEmpty())
	signature[0] ^= 0xff
	parts[2] = base64.RawURLEncoding.EncodeToString(signature)
	return strings.Join(parts, ".")
}

// resolveProbePort returns the container port number a probe port refers to,
// resolving a named port against the container's ports. It returns 0 for a
// name the container does not define.
func resolveProbePort(container corev1.Container, port intstr.IntOrString) int32 {
	if port.Type == intstr.Int {
		return port.IntVal
	}
	for _, containerPort := range container.Ports {
		if containerPort.Name == port.StrVal {
			return containerPort.ContainerPort
		}
	}
	return 0
}

func verifySPIFFEAccessToken(token string, publicKey *rsa.PublicKey) map[string]any {
	claims, err := verifySPIFFEAccessTokenE(token, publicKey)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return claims
}

func verifySPIFFEAccessTokenE(token string, publicKey *rsa.PublicKey) (map[string]any, error) {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return nil, fmt.Errorf("parse SPIFFE access token: %w", err)
	}
	claims := map[string]any{}
	if err := parsed.Claims(publicKey, &claims); err != nil {
		return nil, fmt.Errorf("verify SPIFFE access token: %w", err)
	}
	return claims, nil
}

type spiffeAccessTokenClaims struct {
	issuer   string
	subject  string
	clientID string
	audience string
	scope    string
}

func assertEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims map[string]any, issuer, clientSPIFFEID, clientID, resource string) {
	gomega.Expect(validateEquivalentSPIFFEAccessTokenClaims(
		x509Claims, jwtClaims, issuer, clientSPIFFEID, clientID, resource,
	)).To(gomega.Succeed())
}

func validateEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims map[string]any, issuer, clientSPIFFEID, clientID, resource string) error {
	x509Values, err := validateSPIFFEAccessTokenClaims(x509Claims, issuer, clientSPIFFEID, clientID, resource)
	if err != nil {
		return fmt.Errorf("X.509 SPIFFE access token claims are invalid: %w", err)
	}
	jwtValues, err := validateSPIFFEAccessTokenClaims(jwtClaims, issuer, clientSPIFFEID, clientID, resource)
	if err != nil {
		return fmt.Errorf("JWT SPIFFE access token claims are invalid: %w", err)
	}
	if x509Values != jwtValues {
		return fmt.Errorf("SPIFFE access token claims are not equivalent")
	}
	return nil
}

func validateSPIFFEAccessTokenClaims(claims map[string]any, issuer, clientSPIFFEID, clientID, resource string) (spiffeAccessTokenClaims, error) {
	values, err := spiffeAccessTokenClaimValues(claims)
	if err != nil {
		return spiffeAccessTokenClaims{}, err
	}
	if values.issuer != issuer || values.subject != clientSPIFFEID || values.clientID != clientID || values.audience != resource || values.scope != spiffeClientScope {
		return spiffeAccessTokenClaims{}, fmt.Errorf("SPIFFE access token claims do not match the configured client")
	}
	return values, nil
}

func spiffeAccessTokenClaimValues(claims map[string]any) (spiffeAccessTokenClaims, error) {
	issuer, err := stringSPIFFEAccessTokenClaim(claims, "iss")
	if err != nil {
		return spiffeAccessTokenClaims{}, err
	}
	subject, err := stringSPIFFEAccessTokenClaim(claims, "sub")
	if err != nil {
		return spiffeAccessTokenClaims{}, err
	}
	clientID, err := stringSPIFFEAccessTokenClaim(claims, "client_id")
	if err != nil {
		return spiffeAccessTokenClaims{}, err
	}
	audience, err := singleStringSPIFFEAccessTokenClaim(claims, "aud")
	if err != nil {
		return spiffeAccessTokenClaims{}, err
	}
	scope, err := singleStringSPIFFEAccessTokenClaim(claims, "scp")
	if err != nil {
		return spiffeAccessTokenClaims{}, err
	}
	return spiffeAccessTokenClaims{issuer: issuer, subject: subject, clientID: clientID, audience: audience, scope: scope}, nil
}

func stringSPIFFEAccessTokenClaim(claims map[string]any, name string) (string, error) {
	value, ok := claims[name].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("SPIFFE access token %s claim is invalid", name)
	}
	return value, nil
}

func singleStringSPIFFEAccessTokenClaim(claims map[string]any, name string) (string, error) {
	values, ok := claims[name].([]any)
	if !ok || len(values) != 1 {
		return "", fmt.Errorf("SPIFFE access token %s claim is not a single-element array", name)
	}
	value, ok := values[0].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("SPIFFE access token %s claim contains an invalid value", name)
	}
	return value, nil
}

// createSPIFFEListenerCertificate generates a CA and a leaf certificate
// signed by it for use as e2e listener TLS material. It also returns the
// parsed CA certificate and key so tests can mint additional leaf
// certificates (e.g. to simulate rotation) that remain valid against the
// same public CA bundle.
func createSPIFFEListenerCertificate(serviceName, namespace string) (caPEM, certPEM, keyPEM []byte, ca *x509.Certificate, caKey *rsa.PrivateKey) {
	caKey = generateSPIFFERSAKey()
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "SPIFFE E2E CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ca, err = x509.ParseCertificate(caDER)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	certPEM, keyPEM = createSPIFFEListenerLeafCertificate(ca, caKey, serviceName, namespace, 2)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), certPEM, keyPEM, ca, caKey
}

// createSPIFFEListenerLeafCertificate mints a leaf certificate for
// serviceName/namespace signed by ca/caKey with the given serial number.
func createSPIFFEListenerLeafCertificate(ca *x509.Certificate, caKey *rsa.PrivateKey, serviceName, namespace string, serial int64) (certPEM, keyPEM []byte) {
	serverKey := generateSPIFFERSAKey()
	now := time.Now()
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: serviceName},
		DNSNames:     []string{serviceName + "." + namespace + ".svc.cluster.local"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, ca, &serverKey.PublicKey, caKey)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
}

func generateSPIFFERSAKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return key
}

func assertSPIFFEClientPod(pod *corev1.Pod, publicCAConfigMapName, socketPath string) {
	socketVolumeFound := false
	publicCAVolumeFound := false
	for _, volume := range pod.Spec.Volumes {
		gomega.Expect(volume.Secret).To(gomega.BeNil(), "client workload must not mount static secret material")
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				gomega.Expect(source.Secret).To(gomega.BeNil(), "client workload must not project static secret material")
			}
		}
		switch volume.Name {
		case "spire-socket":
			socketVolumeFound = true
			gomega.Expect(volume.HostPath).NotTo(gomega.BeNil())
		case "as-ca":
			publicCAVolumeFound = true
			gomega.Expect(volume.ConfigMap).NotTo(gomega.BeNil())
			gomega.Expect(volume.ConfigMap.Name).To(gomega.Equal(publicCAConfigMapName))
		}
	}
	gomega.Expect(socketVolumeFound).To(gomega.BeTrue())
	gomega.Expect(publicCAVolumeFound).To(gomega.BeTrue())

	for _, container := range pod.Spec.Containers {
		hasSocketMount := false
		hasPublicCAMount := false
		for _, mount := range container.VolumeMounts {
			switch mount.Name {
			case "spire-socket":
				hasSocketMount = true
				gomega.Expect(container.Name).To(gomega.Equal("spiffe-client"))
				gomega.Expect(mount.ReadOnly).To(gomega.BeTrue())
			case "as-ca":
				hasPublicCAMount = true
				gomega.Expect(container.Name).To(gomega.Equal("spiffe-client"))
				gomega.Expect(mount.ReadOnly).To(gomega.BeTrue())
			}
		}
		if container.Name == "spiffe-client" {
			gomega.Expect(hasSocketMount).To(gomega.BeTrue())
			gomega.Expect(hasPublicCAMount).To(gomega.BeTrue())
			gomega.Expect(container.Command).To(gomega.BeEmpty())
			gomega.Expect(container.Args).To(gomega.Equal([]string{"hold"}))
		} else {
			gomega.Expect(hasSocketMount).To(gomega.BeFalse())
			gomega.Expect(hasPublicCAMount).To(gomega.BeFalse())
		}
		if container.Name == "spiffe-client" {
			gomega.Expect(container.EnvFrom).To(gomega.BeEmpty(), "SPIFFE client must not import static credential environment variables")
		}
		for _, env := range container.Env {
			gomega.Expect(env.ValueFrom == nil || (env.ValueFrom.SecretKeyRef == nil && env.ValueFrom.ConfigMapKeyRef == nil)).To(gomega.BeTrue(), "client workload must not reference an OAuth secret or static private key")
			gomega.Expect(strings.Contains(env.Value, "-----BEGIN")).To(gomega.BeFalse(), "client workload must not carry static PEM material")
			if container.Name == "spiffe-client" {
				gomega.Expect(env.Name).To(gomega.Equal("SPIFFE_ENDPOINT_SOCKET"))
				gomega.Expect(env.Value).To(gomega.Equal("unix://" + socketPath))
			}
			gomega.Expect(strings.Contains(strings.ToLower(env.Name), "secret")).To(gomega.BeFalse(), "client workload must not carry a static OAuth secret")
			gomega.Expect(strings.Contains(strings.ToLower(env.Name), "private_key")).To(gomega.BeFalse(), "client workload must not carry a static private key")
		}
	}
}

func assertSPIFFEASPod(pod *corev1.Pod, bundleConfigMapName string) {
	bundleVolumeName := ""
	for _, volume := range pod.Spec.Volumes {
		gomega.Expect(volume.HostPath).To(gomega.BeNil())
		if volume.ConfigMap != nil && volume.ConfigMap.Name == bundleConfigMapName {
			bundleVolumeName = volume.Name
		}
	}
	gomega.Expect(bundleVolumeName).NotTo(gomega.BeEmpty())

	bundleMountFound := false
	for _, container := range pod.Spec.Containers {
		for _, mount := range container.VolumeMounts {
			if mount.Name == bundleVolumeName {
				gomega.Expect(mount.ReadOnly).To(gomega.BeTrue())
				gomega.Expect(mount.MountPath).To(gomega.ContainSubstring("/etc/toolhive/authserver/spiffe-bundles/"))
				bundleMountFound = true
			}
			gomega.Expect(mount.Name).NotTo(gomega.Equal("spire-socket"))
		}
	}
	gomega.Expect(bundleMountFound).To(gomega.BeTrue())
}
