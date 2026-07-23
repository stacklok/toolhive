// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/egressbroker"
)

// getCurrentCASecret loads the single current-generation bump-CA Secret for
// the test server (fails while zero or several generations exist).
func getCurrentCASecret(into *corev1.Secret) error {
	secrets := &corev1.SecretList{}
	if err := untrustedK8sClient.List(untrustedCtx, secrets,
		client.InNamespace("default"),
		client.MatchingLabels{"toolhive.stacklok.dev/untrusted-resource": "true"}); err != nil {
		return err
	}
	if len(secrets.Items) != 1 {
		return fmt.Errorf("expected exactly one current CA Secret, found %d", len(secrets.Items))
	}
	*into = secrets.Items[0]
	return nil
}

// mintAgedCAForRotation mints a CA already inside the rotation window (past
// 50% of validity) so the next reconcile rotates it.
func mintAgedCAForRotation() (certPEM, keyPEM []byte) {
	ca, err := egressbroker.GenerateBumpCA("untrusted-egress-it-bump-ca",
		time.Now().Add(-egressbroker.CAValidity/2))
	Expect(err).NotTo(HaveOccurred())
	Expect(ca.NeedsRotation(time.Now())).To(BeTrue(), "fixture CA must be inside the rotation window")
	key, err := ca.KeyPEM()
	Expect(err).NotTo(HaveOccurred())
	return ca.CertPEM(), key
}

var _ = Describe("MCPServer untrusted egress resources", Label("k8s", "untrusted", "egress"), func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When an untrusted MCPServer is reconciled", Ordered, func() {
		const serverName = "untrusted-egress-it"
		var mcpServer *mcpv1beta1.MCPServer

		BeforeAll(func() {
			mcpServer = &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serverName,
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:latest",
					Transport: "stdio",
					Untrusted: true,
					GroupRef:  &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					EgressPolicy: &mcpv1beta1.EgressPolicy{
						Providers: []mcpv1beta1.ProviderEgress{{
							Provider:            "github",
							AllowedHosts:        []string{"api.github.com"},
							AllowedMethods:      []string{"GET"},
							AllowedPathPrefixes: []string{"/repos/"},
							CredentialEnvName:   "GITHUB_TOKEN",
						}},
					},
				},
			}
			Expect(untrustedK8sClient.Create(untrustedCtx, mcpServer)).To(Succeed())
		})

		AfterAll(func() {
			Expect(untrustedK8sClient.Delete(untrustedCtx, mcpServer)).To(Succeed())
		})

		It("Should create the generation-named bump-CA Secret with an ownerRef", func() {
			secret := &corev1.Secret{}
			Eventually(func() error {
				return getCurrentCASecret(secret)
			}, timeout, interval).Should(Succeed())

			generation, ok := egressbroker.TrimGeneration(secret.Name, egressbroker.BaseCASecretName(serverName))
			Expect(ok).To(BeTrue(), "CA Secret must be generation-named, got %q", secret.Name)
			Expect(egressbroker.CAGeneration(secret.Data["ca.crt"])).To(Equal(generation),
				"the generation must be the hash of the Secret's cert")
			Expect(secret.Data).To(HaveKey("ca.crt"))
			Expect(secret.Data).To(HaveKey("ca.key"))
			Expect(secret.OwnerReferences).To(HaveLen(1))
			Expect(secret.OwnerReferences[0].Kind).To(Equal("MCPServer"))
			Expect(secret.OwnerReferences[0].Name).To(Equal(serverName))
		})

		It("Should create the CA-bundle ConfigMap with the same generation's public cert only", func() {
			secret := &corev1.Secret{}
			Eventually(func() error {
				return getCurrentCASecret(secret)
			}, timeout, interval).Should(Succeed())
			generation, _ := egressbroker.TrimGeneration(secret.Name, egressbroker.BaseCASecretName(serverName))

			bundle := &corev1.ConfigMap{}
			Eventually(func() error {
				return untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
					Name:      egressbroker.ResourceNamesFor(serverName, generation).CABundle,
					Namespace: "default",
				}, bundle)
			}, timeout, interval).Should(Succeed())

			Expect(bundle.Data).To(HaveKey("ca.crt"))
			Expect(bundle.Data).ToNot(HaveKey("ca.key"),
				"the CA private key must never appear outside the CA Secret")

			// The bundle cert must equal the SAME generation Secret's public cert.
			Expect(bundle.Data["ca.crt"]).To(Equal(string(secret.Data["ca.crt"])))
		})

		It("Should rotate into a new generation, keep N-1, and GC older generations", func() {
			// Force rotation atomically: replace the CURRENT Secret's CA
			// material in place with an aged CA (past the 50% rotation window).
			// Updating data on the same object is a single apiserver write, so
			// there is no empty-set window for a reconcile to misread as "no
			// CA" and no third object for the GC to race. The rotation path
			// keys on the cert's notAfter (not the Secret name), so a sole aged
			// CA makes the next ensure rotate: mint N, retain the aged CA as
			// N-1.
			current := &corev1.Secret{}
			Eventually(func() error {
				return getCurrentCASecret(current)
			}, timeout, interval).Should(Succeed())

			agedCert, agedKey := mintAgedCAForRotation()
			freshName := current.Name
			current.Data = map[string][]byte{"ca.crt": agedCert, "ca.key": agedKey}
			Expect(untrustedK8sClient.Update(untrustedCtx, current)).To(Succeed())

			// The reconcile must mint a NEW generation alongside the aged one
			// and settle to exactly N and N-1 (the aged in-place Secret is the
			// retained previous; any older duplicate from the initial mint is
			// GC'd). Poll the cert set, not a transient count.
			listCA := func() []corev1.Secret {
				secrets := &corev1.SecretList{}
				if err := untrustedK8sClient.List(untrustedCtx, secrets,
					client.InNamespace("default"), client.MatchingLabels(current.Labels)); err != nil {
					return nil
				}
				return secrets.Items
			}
			Eventually(func() bool {
				items := listCA()
				if len(items) != 2 {
					return false
				}
				var sawAged, sawFresh bool
				for i := range items {
					if items[i].Name == freshName && string(items[i].Data["ca.crt"]) == string(agedCert) {
						sawAged = true // the in-place aged CA, retained as N-1
					}
					if items[i].Name != freshName {
						if ca, err := egressbroker.ParseBumpCA(items[i].Data["ca.crt"], items[i].Data["ca.key"]); err == nil &&
							!ca.NeedsRotation(time.Now()) {
							sawFresh = true // the freshly minted current generation
						}
					}
				}
				return sawAged && sawFresh
			}, timeout, interval).Should(BeTrue(),
				"rotation settles to the aged N-1 plus a fresh N, older generations GC'd")

			// Every retained generation's bundle converges on its own Secret's
			// cert (mid-rotation consistency: N and N-1 pairs stay coherent).
			Eventually(func() bool {
				for _, s := range listCA() {
					gen, ok := egressbroker.TrimGeneration(s.Name, egressbroker.BaseCASecretName(serverName))
					if !ok {
						continue
					}
					b := &corev1.ConfigMap{}
					if err := untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
						Name:      egressbroker.ResourceNamesFor(serverName, gen).CABundle,
						Namespace: "default",
					}, b); err != nil {
						return false
					}
					if b.Data["ca.crt"] != string(s.Data["ca.crt"]) {
						return false
					}
				}
				return true
			}, timeout, interval).Should(BeTrue(),
				"each retained generation's bundle must carry that generation's cert")
		})

		It("Should render the egress policy ConfigMap from the CRD EgressPolicy", func() {
			cm := &corev1.ConfigMap{}
			Eventually(func() error {
				return untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
					Name:      serverName + "-egress-policy",
					Namespace: "default",
				}, cm)
			}, timeout, interval).Should(Succeed())

			Expect(cm.Data).To(HaveKey("policy.yaml"))
			Expect(cm.Data["policy.yaml"]).To(ContainSubstring("provider: github"))
			Expect(cm.Data["policy.yaml"]).To(ContainSubstring("api.github.com"))
			Expect(cm.Data["policy.yaml"]).To(ContainSubstring("/repos/"))
			Expect(cm.OwnerReferences).To(HaveLen(1))
			Expect(cm.OwnerReferences[0].Kind).To(Equal("MCPServer"))
		})

		It("Should create the egress-lockdown NetworkPolicy selecting the untrusted session pods", func() {
			np := &networkingv1.NetworkPolicy{}
			Eventually(func() error {
				return untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
					Name:      serverName + "-egress",
					Namespace: "default",
				}, np)
			}, timeout, interval).Should(Succeed())

			Expect(np.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue(
				"toolhive.stacklok.dev/untrusted", "true"))
			Expect(np.Spec.PodSelector.MatchLabels).To(HaveKey("toolhive.stacklok.dev/mcpserver-uid"))
			Expect(np.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeEgress))

			var cidrs []string
			for _, rule := range np.Spec.Egress {
				for _, peer := range rule.To {
					if peer.IPBlock != nil {
						cidrs = append(cidrs, peer.IPBlock.CIDR)
					}
				}
			}
			Expect(cidrs).To(ContainElement("127.0.0.1/32"),
				"loopback (the in-pod sidecar) must be the only always-permitted egress besides DNS")
			Expect(np.OwnerReferences).To(HaveLen(1))
			Expect(np.OwnerReferences[0].Kind).To(Equal("MCPServer"))
		})

		It("Should delete all untrusted resources on an untrusted→trusted flip", func() {
			current := &mcpv1beta1.MCPServer{}
			Expect(untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
				Name:      serverName,
				Namespace: "default",
			}, current)).To(Succeed())
			current.Spec.Untrusted = false
			Expect(untrustedK8sClient.Update(untrustedCtx, current)).To(Succeed())

			gone := func(obj client.Object, name string) func() bool {
				return func() bool {
					err := untrustedK8sClient.Get(untrustedCtx,
						types.NamespacedName{Name: name, Namespace: "default"}, obj)
					return apierrors.IsNotFound(err)
				}
			}
			Eventually(func() int {
				secrets := &corev1.SecretList{}
				if err := untrustedK8sClient.List(untrustedCtx, secrets,
					client.InNamespace("default"),
					client.MatchingLabels{"toolhive.stacklok.dev/untrusted-resource": "true"}); err != nil {
					return -1
				}
				return len(secrets.Items)
			}, timeout, interval).Should(Equal(0), "every CA generation Secret must be deleted on flip")
			Eventually(gone(&corev1.ConfigMap{}, serverName+"-egress-policy"), timeout, interval).Should(BeTrue())
			Eventually(gone(&networkingv1.NetworkPolicy{}, serverName+"-egress"), timeout, interval).Should(BeTrue(),
				"NetworkPolicy must be deleted on flip")
		})
	})
})
