// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
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
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

const spiffeClientE2EImage = "kind.local/toolhive-spiffe-e2e-client/spiffe-client:latest"

var _ = ginkgo.Describe("MCPServer SPIFFE client credentials", ginkgo.Ordered, func() {
	const (
		timeout      = 5 * time.Minute
		pollInterval = 2 * time.Second
		clientScope  = "mcp.read"
		proxyPort    = 8080
	)

	var (
		asName                            string
		asServiceName                     string
		authConfigName                    string
		clientName                        string
		unmatchedClientName               string
		clientServiceAccountName          string
		unmatchedClientServiceAccountName string
		clientID                          string
		clientSPIFFEID                    string
		dexName                           string
		dexSecretName                     string
		hmacSecretName                    string
		listenerSecretName                string
		publicCAConfigMapName             string
		signingSecretName                 string
		spireName                         string
		issuer                            string
		resource                          string
		signingKey                        *rsa.PrivateKey
		spireInfo                         *SPIREInfo
		cleanupDex                        func()
		cleanupSPIRE                      func()
	)

	ginkgo.BeforeAll(func() {
		suffix := fmt.Sprintf("%d-%d", ginkgo.GinkgoParallelProcess(), time.Now().UnixNano())
		asName = "spiffe-as-" + suffix
		asServiceName = "mcp-" + asName + "-proxy"
		authConfigName = "spiffe-auth-" + suffix
		clientName = "spiffe-client-" + suffix
		unmatchedClientName = "spiffe-unmatched-client-" + suffix
		clientServiceAccountName = "spiffe-client-sa-" + suffix
		unmatchedClientServiceAccountName = "spiffe-unmatched-client-sa-" + suffix
		clientID = "spiffe-client-" + suffix
		dexName = "spiffe-dex-" + suffix
		dexSecretName = "spiffe-dex-secret-" + suffix
		hmacSecretName = "spiffe-hmac-" + suffix
		listenerSecretName = "spiffe-listener-" + suffix
		publicCAConfigMapName = "spiffe-ca-" + suffix
		signingSecretName = "spiffe-signing-" + suffix
		spireName = "spiffe-" + suffix
		issuer = fmt.Sprintf("https://%s.%s.svc.cluster.local:%d", asServiceName, defaultNamespace, proxyPort)
		resource = issuer + "/mcp"
		clientSPIFFEID = fmt.Sprintf(
			"spiffe://%s/workload/%s/%s/%s",
			spireTrustDomain,
			defaultNamespace,
			clientServiceAccountName,
			clientName,
		)

		ginkgo.By("creating the attested and unmatched client ServiceAccounts")
		for _, serviceAccountName := range []string{clientServiceAccountName, unmatchedClientServiceAccountName} {
			gomega.Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: defaultNamespace},
			})).To(gomega.Succeed())
		}

		ginkgo.By("deploying SPIRE and verifying its published trust bundle")
		spireInfo, cleanupSPIRE = deploySPIRE(ctx, k8sClient, spireName, defaultNamespace, timeout, pollInterval)
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
		gomega.Expect(CreateSPIREWorkloadEntries(
			ctx,
			k8sClient,
			defaultNamespace,
			serverPodName,
			spireName+"-agent",
			defaultNamespace,
			clientServiceAccountName,
			clientName,
		)).To(gomega.Succeed())

		ginkgo.By("creating listener TLS material and a client-only public CA bundle")
		caPEM, certPEM, keyPEM := createSPIFFEListenerCertificate(asServiceName, defaultNamespace)
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
		_, cleanupDex = deployDex(ctx, k8sClient, dexName, issuer+"/oauth/callback")

		ginkgo.By("creating the file-backed embedded authorization server configuration")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
					Issuer: issuer,
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
					InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientAuthConfig{{
						TrustDomainRef: "spire",
						Principal:      clientSPIFFEID,
						ClientID:       clientID,
						Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{
							mcpv1beta1.SPIFFEAuthenticationMethodX509,
							mcpv1beta1.SPIFFEAuthenticationMethodJWT,
						},
						Resources:  []string{resource},
						Audiences:  []string{resource},
						Scopes:     []string{clientScope},
						GrantTypes: []string{"client_credentials"},
					}}},
					ListenerTLS: &mcpv1beta1.ListenerTLSConfig{
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

		ginkgo.By("creating the authorization server and attested ToolHive client")
		gomega.Expect(k8sClient.Create(ctx, newSPIFFEMCPServer(
			asName,
			nil,
			&mcpv1beta1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: authConfigName},
			nil,
		))).To(gomega.Succeed())
		socketVolume, socketMount, socketEnv, err := SPIREWorkloadAPIVolume("spire-socket")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clientTemplate := corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "mcp"},
					{
						Name:  "spiffe-client",
						Image: spiffeClientE2EImage,
						Args:  []string{"hold"},
						Env:   []corev1.EnvVar{socketEnv},
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

		waitForSPIFFEMCPServerReady(asName, timeout, pollInterval)
		waitForSPIFFEMCPServerReady(clientName, timeout, pollInterval)
		waitForSPIFFEMCPServerReady(unmatchedClientName, timeout, pollInterval)
	})

	ginkgo.AfterAll(func() {
		for _, name := range []string{unmatchedClientName, clientName, asName} {
			_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace},
			})
		}
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: authConfigName, Namespace: defaultNamespace},
		})
		for _, serviceAccountName := range []string{unmatchedClientServiceAccountName, clientServiceAccountName} {
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
		if cleanupSPIRE != nil {
			cleanupSPIRE()
		}
	})

	ginkgo.It("attests the registered ToolHive-managed workload and rejects an unmatched one", func() {
		registeredPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		identity, err := fetchSPIFFEIdentity(registeredPod.Name, spireInfo.AgentSocketPath, issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(identity.SPIFFEID).To(gomega.Equal(clientSPIFFEID))

		unmatchedPod := findSPIFFEClientBackendPod(unmatchedClientServiceAccountName)
		_, err = fetchSPIFFEIdentity(unmatchedPod.Name, spireInfo.AgentSocketPath, issuer)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("issues equivalent X.509-SVID and JWT-SVID client credentials tokens without static client credentials", func() {
		clientPod := findSPIFFEClientBackendPod(clientServiceAccountName)
		asPod := findMCPServerProxyPod(asName)
		assertSPIFFEClientPod(clientPod, publicCAConfigMapName)
		assertSPIFFEASPod(asPod, spireInfo.BundleConfigMapName)

		identity, err := fetchSPIFFEIdentity(clientPod.Name, spireInfo.AgentSocketPath, issuer)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(identity.SPIFFEID).To(gomega.Equal(clientSPIFFEID))

		x509Claims := verifySPIFFEAccessToken(requestSPIFFEToken(clientPod.Name, "x509", spireInfo.AgentSocketPath, issuer, clientID, resource, clientScope), &signingKey.PublicKey)
		jwtClaims := verifySPIFFEAccessToken(requestSPIFFEToken(clientPod.Name, "jwt", spireInfo.AgentSocketPath, issuer, clientID, resource, clientScope), &signingKey.PublicKey)
		assertEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims, issuer, clientSPIFFEID, clientID, resource, clientScope)
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
		gomega.Expect(RotateSPIRELocalAuthorities(ctx, defaultNamespace, serverPodName)).To(gomega.Succeed())

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
			if identity.JWTExpiry.Compare(preRotationIdentity.JWTExpiry) <= 0 {
				return fmt.Errorf("client JWT-SVID expiry has not advanced")
			}
			x509Token, tokenErr := requestSPIFFETokenE(clientPod.Name, "x509", spireInfo.AgentSocketPath, issuer, clientID, resource, clientScope)
			if tokenErr != nil {
				return tokenErr
			}
			x509Claims, claimsErr := verifySPIFFEAccessTokenE(x509Token, &signingKey.PublicKey)
			if claimsErr != nil {
				return claimsErr
			}
			jwtToken, tokenErr := requestSPIFFETokenE(clientPod.Name, "jwt", spireInfo.AgentSocketPath, issuer, clientID, resource, clientScope)
			if tokenErr != nil {
				return tokenErr
			}
			jwtClaims, claimsErr := verifySPIFFEAccessTokenE(jwtToken, &signingKey.PublicKey)
			if claimsErr != nil {
				return claimsErr
			}
			return validateEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims, issuer, clientSPIFFEID, clientID, resource, clientScope)
		}, 2*time.Minute, pollInterval).Should(gomega.Succeed())
		assertPodSnapshot(findMCPServerProxyPod(asName), asSnapshot)
		assertPodSnapshot(findSPIFFEClientBackendPod(clientServiceAccountName), clientSnapshot)
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
		},
	}
}

type spiffeIdentity struct {
	SPIFFEID       string    `json:"spiffe_id"`
	X509LeafSerial string    `json:"x509_leaf_serial"`
	JWTExpiry      time.Time `json:"jwt_expiry"`
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
	if identity.SPIFFEID == "" || identity.X509LeafSerial == "" || identity.JWTExpiry.IsZero() {
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

func waitForSPIFFEMCPServerReady(name string, timeout, interval time.Duration) {
	gomega.Eventually(func() mcpv1beta1.MCPServerPhase {
		server := &mcpv1beta1.MCPServer{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: name, Namespace: defaultNamespace,
		}, server)).To(gomega.Succeed())
		return server.Status.Phase
	}, timeout, interval).Should(gomega.Equal(mcpv1beta1.MCPServerPhaseReady))
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

func requestSPIFFEToken(podName, method, socketPath, issuer, clientID, resource, scope string) string {
	token, err := testutil.ExecutePodCommand(ctx, defaultNamespace, podName, "spiffe-client", []string{
		"/ko-app/spiffe-client",
		"token",
		"--method", method,
		"--socket", "unix://" + socketPath,
		"--issuer", issuer,
		"--client-id", clientID,
		"--resource", resource,
		"--scope", scope,
		"--server-ca-file", "/var/run/spiffe-ca/ca.crt",
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	token = strings.TrimSpace(token)
	gomega.Expect(token).NotTo(gomega.BeEmpty())
	return token
}

func requestSPIFFETokenE(podName, method, socketPath, issuer, clientID, resource, scope string) (string, error) {
	token, err := testutil.ExecutePodCommand(ctx, defaultNamespace, podName, "spiffe-client", []string{
		"/ko-app/spiffe-client",
		"token",
		"--method", method,
		"--socket", "unix://" + socketPath,
		"--issuer", issuer,
		"--client-id", clientID,
		"--resource", resource,
		"--scope", scope,
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

func verifySPIFFEAccessToken(token string, publicKey *rsa.PublicKey) map[string]any {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	claims := map[string]any{}
	gomega.Expect(parsed.Claims(publicKey, &claims)).To(gomega.Succeed())
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

func assertEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims map[string]any, issuer, clientSPIFFEID, clientID, resource, clientScope string) {
	x509Values, err := spiffeAccessTokenClaimValues(x509Claims)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	jwtValues, err := spiffeAccessTokenClaimValues(jwtClaims)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for _, values := range []spiffeAccessTokenClaims{x509Values, jwtValues} {
		gomega.Expect(values.issuer).To(gomega.Equal(issuer))
		gomega.Expect(values.subject).To(gomega.Equal(clientSPIFFEID))
		gomega.Expect(values.clientID).To(gomega.Equal(clientID))
		gomega.Expect(values.audience).To(gomega.Equal(resource))
		gomega.Expect(values.scope).To(gomega.Equal(clientScope))
	}
	gomega.Expect(x509Values).To(gomega.Equal(jwtValues))
}

func validateEquivalentSPIFFEAccessTokenClaims(x509Claims, jwtClaims map[string]any, issuer, clientSPIFFEID, clientID, resource, clientScope string) error {
	x509Values, err := validateSPIFFEAccessTokenClaims(x509Claims, issuer, clientSPIFFEID, clientID, resource, clientScope)
	if err != nil {
		return fmt.Errorf("X.509 SPIFFE access token claims are invalid: %w", err)
	}
	jwtValues, err := validateSPIFFEAccessTokenClaims(jwtClaims, issuer, clientSPIFFEID, clientID, resource, clientScope)
	if err != nil {
		return fmt.Errorf("JWT SPIFFE access token claims are invalid: %w", err)
	}
	if x509Values != jwtValues {
		return fmt.Errorf("SPIFFE access token claims are not equivalent")
	}
	return nil
}

func validateSPIFFEAccessTokenClaims(claims map[string]any, issuer, clientSPIFFEID, clientID, resource, clientScope string) (spiffeAccessTokenClaims, error) {
	values, err := spiffeAccessTokenClaimValues(claims)
	if err != nil {
		return spiffeAccessTokenClaims{}, err
	}
	if values.issuer != issuer || values.subject != clientSPIFFEID || values.clientID != clientID || values.audience != resource || values.scope != clientScope {
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

func createSPIFFEListenerCertificate(serviceName, namespace string) ([]byte, []byte, []byte) {
	caKey := generateSPIFFERSAKey()
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

	serverKey := generateSPIFFERSAKey()
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: serviceName},
		DNSNames:     []string{serviceName + "." + namespace + ".svc.cluster.local"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
}

func generateSPIFFERSAKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return key
}

func assertSPIFFEClientPod(pod *corev1.Pod, publicCAConfigMapName string) {
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
				gomega.Expect(env.Value).To(gomega.Equal("unix://" + spireAgentSocketPath))
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
