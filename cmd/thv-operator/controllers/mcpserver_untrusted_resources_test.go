// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func withEgressPolicy() v1beta1test.MCPServerOption {
	return v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
		m.Spec.Untrusted = true
		m.Spec.EgressPolicy = &mcpv1beta1.EgressPolicy{
			Providers: []mcpv1beta1.ProviderEgress{{
				Provider:            "github",
				AllowedHosts:        []string{"api.github.com"},
				AllowedMethods:      []string{"GET"},
				AllowedPathPrefixes: []string{"/repos/"},
			}},
		}
	})
}

// Unit fixtures use hostname allowedHosts (the CRD grammar forbids IP
// literals) plus a stubbed DNS lookup — no real resolution in unit tests. The
// stub spans the test's whole lifetime (t.Cleanup restores it): a mid-test
// restore would let a parallel sibling's Reconcile hit real DNS or a stale
// stub. Tests calling this must NOT run in parallel (paralleltest nolint).
func stubEgressDNS(t *testing.T, ips map[string][]net.IP) {
	t.Helper()
	untrustedDNSTestLock(t, stubDNSFor(ips, false))
}

// stubEgressDNSStrict stubs DNS with exact map semantics (unknown hosts
// fail) for tests asserting resolution failure.
func stubEgressDNSStrict(t *testing.T, ips map[string][]net.IP) {
	t.Helper()
	untrustedDNSTestLock(t, stubDNSFor(ips, true))
}

// stubDNSFor builds a DNS stub over ips. Non-strict stubs resolve unknown
// hosts to the shared fixture IP so any test's stub is a valid stand-in for
// any other's (a parallel sibling may run under this stub after this test's
// cleanup already ran). Strict stubs fail unknown hosts (resolution-failure
// assertions).
func stubDNSFor(ips map[string][]net.IP, strict bool) func(string) ([]net.IP, error) {
	return func(host string) ([]net.IP, error) {
		if resolved, ok := ips[host]; ok {
			return resolved, nil
		}
		if strict {
			return nil, fmt.Errorf("no such host: %s", host)
		}
		return []net.IP{net.ParseIP("140.82.114.26")}, nil
	}
}

var githubDNS = map[string][]net.IP{"api.github.com": {net.ParseIP("140.82.114.26")}}

func caSecretName(t *testing.T, r *MCPServerReconciler, m *mcpv1beta1.MCPServer) string {
	t.Helper()
	secrets := listCASecrets(t, r, m)
	require.Len(t, secrets, 1, "expected exactly one current CA generation Secret")
	return secrets[0].Name
}

func listCASecrets(t *testing.T, r *MCPServerReconciler, m *mcpv1beta1.MCPServer) []corev1.Secret {
	t.Helper()
	secrets := &corev1.SecretList{}
	require.NoError(t, r.List(t.Context(), secrets,
		client.InNamespace(m.Namespace), client.MatchingLabels(untrustedResourceLabels(m))))
	return secrets.Items
}

//nolint:tparallel // Subtests swap the package-level DNS lookup stub; they must run serially.
func TestEnsureUntrustedResources(t *testing.T) {
	t.Parallel()

	//nolint:paralleltest // Swaps the package-level DNS lookup stub (restored after each ensure).
	t.Run("untrusted MCPServer gets generation-named CA Secret, bundle, policy ConfigMap, NetworkPolicy", func(t *testing.T) {
		m := v1beta1test.NewMCPServer("github-mcp", "default", withEgressPolicy())
		stubEgressDNS(t, githubDNS)
		r, _ := setupUntrustedReconciler(t, m)
		ctx := t.Context()

		require.NoError(t, r.ensureUntrustedResources(ctx, m))

		// Generation-named CA Secret with both keys, ownerRef'd.
		secrets := listCASecrets(t, r, m)
		require.Len(t, secrets, 1)
		secret := secrets[0]
		generation, ok := egressbroker.TrimGeneration(secret.Name, egressbroker.BaseCASecretName(m.Name))
		require.True(t, ok, "CA Secret must be generation-named, got %q", secret.Name)
		assert.Equal(t, egressbroker.ResourceNamesFor(m.Name, generation).CASecret, secret.Name)
		assert.NotEmpty(t, secret.Data["ca.crt"])
		assert.NotEmpty(t, secret.Data["ca.key"])
		assertOwnerRef(t, secret.OwnerReferences, m)

		// The generation is the hash of the cert; the CA parses and is fresh.
		assert.Equal(t, egressbroker.CAGeneration(secret.Data["ca.crt"]), generation)
		ca, err := egressbroker.ParseBumpCA(secret.Data["ca.crt"], secret.Data["ca.key"])
		require.NoError(t, err)
		assert.False(t, ca.NeedsRotation(metav1.Now().Time))

		// Bundle ConfigMap of the SAME generation carries the same public cert, no key.
		bundle := &corev1.ConfigMap{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{
			Name: egressbroker.ResourceNamesFor(m.Name, generation).CABundle, Namespace: "default"}, bundle))
		assert.Equal(t, string(secret.Data["ca.crt"]), bundle.Data["ca.crt"])
		assert.NotContains(t, bundle.Data, "ca.key")
		assertOwnerRef(t, bundle.OwnerReferences, m)

		// Policy ConfigMap renders the CRD EgressPolicy and re-parses.
		policyCM := &corev1.ConfigMap{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{
			Name: "github-mcp-egress-policy", Namespace: "default"}, policyCM))
		assertOwnerRef(t, policyCM.OwnerReferences, m)
		compiled, err := egressbroker.ParsePolicy([]byte(policyCM.Data["policy.yaml"]))
		require.NoError(t, err, "rendered policy must round-trip through the sidecar's parser")
		provider, ok := compiled.ProviderFor("api.github.com")
		assert.True(t, ok)
		assert.Equal(t, "github", provider)
		assert.Equal(t, []string{"140.82.114.26/32"}, compiled.DialAllowlist())

		// NetworkPolicy selects the untrusted session pods and locks egress.
		np := &networkingv1.NetworkPolicy{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{
			Name: "github-mcp-egress", Namespace: "default"}, np))
		assertOwnerRef(t, np.OwnerReferences, m)
		assert.Equal(t, map[string]string{
			"toolhive.stacklok.dev/untrusted":     "true",
			"toolhive.stacklok.dev/mcpserver-uid": string(m.UID),
		}, np.Spec.PodSelector.MatchLabels)
		assert.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)

		var cidrs []string
		for _, rule := range np.Spec.Egress {
			for _, peer := range rule.To {
				if peer.IPBlock != nil {
					cidrs = append(cidrs, peer.IPBlock.CIDR)
				}
			}
		}
		assert.Contains(t, cidrs, "127.0.0.1/32", "loopback (sidecar) must be permitted")
		assert.Contains(t, cidrs, "140.82.114.26/32", "policy destinations must be permitted")
		assertDNSRuleRestricted(t, np)
	})

	//nolint:paralleltest // Swaps the package-level DNS lookup stub (restored after each ensure).
	t.Run("reconcile is idempotent: second ensure performs no spurious write", func(t *testing.T) {
		m := v1beta1test.NewMCPServer("github-mcp", "default", withEgressPolicy())
		stubEgressDNS(t, githubDNS)
		r, _ := setupUntrustedReconciler(t, m)
		ctx := t.Context()

		require.NoError(t, r.ensureUntrustedResources(ctx, m))
		secretName := caSecretName(t, r, m)
		secretBefore := &corev1.Secret{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secretBefore))
		npBefore := &networkingv1.NetworkPolicy{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{
			Name: "github-mcp-egress", Namespace: "default"}, npBefore))

		require.NoError(t, r.ensureUntrustedResources(ctx, m))

		secrets := listCASecrets(t, r, m)
		require.Len(t, secrets, 1, "idempotent reconcile must not mint a new generation")
		secretAfter := &corev1.Secret{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secretAfter))
		assert.Equal(t, secretBefore.ResourceVersion, secretAfter.ResourceVersion,
			"unchanged CA Secret must not be rewritten (a rewrite would churn pods)")
		npAfter := &networkingv1.NetworkPolicy{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{
			Name: "github-mcp-egress", Namespace: "default"}, npAfter))
		if npBefore.ResourceVersion != npAfter.ResourceVersion {
			t.Fatalf("NetworkPolicy churned: before=%s after=%s\nbefore rules: %s\nafter rules:  %s\nbefore labels: %v\nafter labels:  %v",
				npBefore.ResourceVersion, npAfter.ResourceVersion,
				renderedEgressRules(npBefore.Spec.Egress), renderedEgressRules(npAfter.Spec.Egress),
				npBefore.Labels, npAfter.Labels)
		}
	})

	//nolint:paralleltest // Swaps the package-level DNS lookup stub (restored after each ensure).
	t.Run("untrusted→trusted flip deletes all untrusted resources", func(t *testing.T) {
		m := v1beta1test.NewMCPServer("github-mcp", "default", withEgressPolicy())
		stubEgressDNS(t, githubDNS)
		r, _ := setupUntrustedReconciler(t, m)
		ctx := t.Context()

		require.NoError(t, r.ensureUntrustedResources(ctx, m))
		secretName := caSecretName(t, r, m)
		gen, _ := egressbroker.TrimGeneration(secretName, egressbroker.BaseCASecretName(m.Name))

		// Flip to trusted.
		m.Spec.Untrusted = false
		require.NoError(t, r.ensureUntrustedResources(ctx, m))

		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secret)
		assert.True(t, apierrors.IsNotFound(err), "CA Secret must be deleted on flip")
		bundle := &corev1.ConfigMap{}
		err = r.Get(ctx, types.NamespacedName{
			Name: egressbroker.ResourceNamesFor(m.Name, gen).CABundle, Namespace: "default"}, bundle)
		assert.True(t, apierrors.IsNotFound(err), "CA bundle must be deleted on flip")
		policyCM := &corev1.ConfigMap{}
		err = r.Get(ctx, types.NamespacedName{Name: "github-mcp-egress-policy", Namespace: "default"}, policyCM)
		assert.True(t, apierrors.IsNotFound(err), "policy ConfigMap must be deleted on flip")
		np := &networkingv1.NetworkPolicy{}
		err = r.Get(ctx, types.NamespacedName{Name: "github-mcp-egress", Namespace: "default"}, np)
		assert.True(t, apierrors.IsNotFound(err), "NetworkPolicy must be deleted on flip")
	})

	//nolint:paralleltest // Serial with sibling subtests that swap the DNS lookup stub.
	t.Run("missing EgressPolicy on untrusted server is a terminal spec error", func(t *testing.T) {
		m := v1beta1test.NewMCPServer("github-mcp", "default", withUntrustedSpec())
		r, _ := setupUntrustedReconciler(t, m)

		err := r.ensureUntrustedResources(t.Context(), m)
		require.Error(t, err)
		var specErr *SpecValidationError
		require.ErrorAs(t, err, &specErr)
	})

	//nolint:paralleltest // Serial with sibling subtests that swap the DNS lookup stub.
	t.Run("DNS failure is transient, never a terminal spec error", func(t *testing.T) {
		m := v1beta1test.NewMCPServer("github-mcp", "default", withEgressPolicy())
		stubEgressDNSStrict(t, map[string][]net.IP{}) // every lookup fails
		r, _ := setupUntrustedReconciler(t, m)

		err := r.ensureUntrustedResources(t.Context(), m)
		require.Error(t, err)
		assert.ErrorIs(t, err, egressbroker.ErrDNSResolution)
		var specErr *SpecValidationError
		assert.False(t, errorAsSpecValidation(err, &specErr),
			"a DNS blip must retry with backoff, not poison the workload: %v", err)
	})

	//nolint:paralleltest // Serial with sibling subtests that swap the DNS lookup stub.
	t.Run("host resolving to no addresses is a terminal spec error", func(t *testing.T) {
		m := v1beta1test.NewMCPServer("github-mcp", "default", withEgressPolicy())
		stubEgressDNSStrict(t, map[string][]net.IP{"api.github.com": {}}) // resolves to nothing
		r, _ := setupUntrustedReconciler(t, m)

		err := r.ensureUntrustedResources(t.Context(), m)
		require.Error(t, err)
		var specErr *SpecValidationError
		require.ErrorAs(t, err, &specErr)
	})

	//nolint:paralleltest // Swaps the package-level DNS lookup stub (restored after each ensure).
	t.Run("unparsable CA Secret is garbage-collected and a fresh generation minted", func(t *testing.T) {
		m := v1beta1test.NewMCPServer("github-mcp", "default", withEgressPolicy())
		stubEgressDNS(t, githubDNS)
		r, _ := setupUntrustedReconciler(t, m)
		ctx := t.Context()

		require.NoError(t, r.ensureUntrustedResources(ctx, m))
		// Corrupt the current generation Secret.
		secretName := caSecretName(t, r, m)
		secret := &corev1.Secret{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, secret))
		secret.Data = map[string][]byte{"ca.crt": []byte("garbage"), "ca.key": []byte("garbage")}
		require.NoError(t, r.Update(ctx, secret))

		require.NoError(t, r.ensureUntrustedResources(ctx, m))
		secrets := listCASecrets(t, r, m)
		require.Len(t, secrets, 1, "the corrupt generation must be GC'd, replaced by one fresh generation")
		assert.NotEqual(t, secretName, secrets[0].Name)
		_, err := egressbroker.ParseBumpCA(secrets[0].Data["ca.crt"], secrets[0].Data["ca.key"])
		require.NoError(t, err, "corrupt CA must be regenerated")
	})

	//nolint:paralleltest // Swaps the package-level DNS lookup stub (restored after each ensure).
	t.Run("rotation mints a new generation, keeps N-1, GCs older, and re-stamps the STS template", func(t *testing.T) {
		m := v1beta1test.NewMCPServer("github-mcp", "default", withEgressPolicy())
		stubEgressDNS(t, githubDNS)
		r, _ := setupUntrustedReconciler(t, m)
		ctx := t.Context()

		require.NoError(t, r.ensureUntrustedResources(ctx, m))
		firstName := caSecretName(t, r, m)
		first := &corev1.Secret{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{Name: firstName, Namespace: "default"}, first))

		// Force rotation: replace the fresh generation with an almost-expired
		// CA (test-only manipulation of the stored objects) so the next ensure
		// rotates it. The fixture is backdated well past the second aged
		// fixture below so generation ordering by notAfter is unambiguous.
		oldCert, oldKey := mintAgedBumpCA(t, m.Name, -egressbroker.CAValidity/2-24*time.Hour)
		staleGen := egressbroker.CAGeneration(oldCert)
		staleName := egressbroker.ResourceNamesFor(m.Name, staleGen).CASecret
		require.NoError(t, r.Delete(ctx, first))
		require.NoError(t, r.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      staleName,
				Namespace: m.Namespace,
				Labels:    untrustedResourceLabels(m),
			},
			Data: map[string][]byte{"ca.crt": oldCert, "ca.key": oldKey},
		}))

		require.NoError(t, r.ensureUntrustedResources(ctx, m))

		secrets := listCASecrets(t, r, m)
		require.Len(t, secrets, 2, "rotation must keep N (new) and N-1 (aged) generations")

		var newSecret *corev1.Secret
		for i := range secrets {
			if secrets[i].Name != staleName {
				newSecret = &secrets[i]
			}
		}
		require.NotNil(t, newSecret, "a new generation Secret must exist")
		newGen, _ := egressbroker.TrimGeneration(newSecret.Name, egressbroker.BaseCASecretName(m.Name))
		assert.NotEqual(t, staleGen, newGen)

		// The new bundle matches the new Secret's cert (same generation pair).
		bundle := &corev1.ConfigMap{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{
			Name: egressbroker.ResourceNamesFor(m.Name, newGen).CABundle, Namespace: "default"}, bundle))
		assert.Equal(t, string(newSecret.Data["ca.crt"]), bundle.Data["ca.crt"],
			"bundle must always come from the same generation as the Secret")
		// The stale generation's bundle is recreated from its own Secret's cert
		// (N-1 pods keep a consistent pair), never from the new cert.
		oldBundle := &corev1.ConfigMap{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{
			Name: egressbroker.ResourceNamesFor(m.Name, staleGen).CABundle, Namespace: "default"}, oldBundle))
		assert.Equal(t, string(oldCert), oldBundle.Data["ca.crt"])

		// The STS template annotation publishes the NEW generation.
		sts := backendSTSFixture(m)
		require.NoError(t, r.Create(ctx, sts))
		require.NoError(t, r.ensureUntrustedResources(ctx, m))
		stamped := &appsv1.StatefulSet{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{Name: sts.Name, Namespace: m.Namespace}, stamped))
		assert.Equal(t, newGen, stamped.Spec.Template.Annotations[untrustedCAGenerationAnnotation])

		// A third rotation GCs the oldest generation.
		newest := &corev1.Secret{}
		require.NoError(t, r.Get(ctx, types.NamespacedName{Name: newSecret.Name, Namespace: "default"}, newest))
		olderCert, olderKey := mintAgedBumpCA(t, m.Name, -egressbroker.CAValidity/2)
		require.NoError(t, r.Delete(ctx, newest))
		// Replace "current" with another aged CA so the next ensure rotates again.
		secondStaleGen := egressbroker.CAGeneration(olderCert)
		require.NoError(t, r.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      egressbroker.ResourceNamesFor(m.Name, secondStaleGen).CASecret,
				Namespace: m.Namespace,
				Labels:    untrustedResourceLabels(m),
			},
			Data: map[string][]byte{"ca.crt": olderCert, "ca.key": olderKey},
		}))
		require.NoError(t, r.ensureUntrustedResources(ctx, m))

		secrets = listCASecrets(t, r, m)
		require.Len(t, secrets, 2, "only N and N-1 generations survive")
		names := []string{secrets[0].Name, secrets[1].Name}
		assert.NotContains(t, names, staleName, "the oldest generation must be GC'd")
		goneBundle := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{
			Name: egressbroker.ResourceNamesFor(m.Name, staleGen).CABundle, Namespace: "default"}, goneBundle)
		assert.True(t, apierrors.IsNotFound(err), "the oldest generation's bundle must be GC'd")
	})
}

// backendSTSFixture builds the labeled backend StatefulSet the generation
// stamp targets (same labels DeployWorkload derives from the RunConfig
// container labels).
func backendSTSFixture(m *mcpv1beta1.MCPServer) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
			Labels: map[string]string{
				untrustedMCPServerUIDLabel: string(m.UID),
				"toolhive":                 "true",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": m.Name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": m.Name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "mcp", Image: "img"}}},
			},
		},
	}
}

// mintAgedBumpCA mints a CA already inside the rotation window (past 50% of
// validity) so ensure triggers rotation on the next pass. age is the
// (negative) offset from now the CA's validity starts at; distinct ages make
// generation ordering by notAfter unambiguous.
func mintAgedBumpCA(t *testing.T, name string, age time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	ca, err := egressbroker.GenerateBumpCA(name+"-bump-ca", metav1.Now().Add(age))
	require.NoError(t, err)
	require.True(t, ca.NeedsRotation(metav1.Now().Time), "fixture CA must be inside the rotation window")
	key, err := ca.KeyPEM()
	require.NoError(t, err)
	return ca.CertPEM(), key
}

func errorAsSpecValidation(err error, target **SpecValidationError) bool {
	for err != nil {
		if se, ok := err.(*SpecValidationError); ok {
			*target = se
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// TestCreateRunConfig_UntrustedUIDLabel pins that an untrusted MCPServer's
// RunConfig carries the rename-safe mcpserver-uid container label. The label
// flows to the backend StatefulSet (DeployWorkload applies ContainerLabels),
// which is how the vMCP pod lifecycle resolves the clone template by selector
// (LabelMCPServerUID + toolhive=true). A trusted MCPServer must NOT carry it.
func TestCreateRunConfig_UntrustedUIDLabel(t *testing.T) {
	t.Parallel()

	newServer := func(opts ...v1beta1test.MCPServerOption) *mcpv1beta1.MCPServer {
		return v1beta1test.NewMCPServer("github-mcp", "default", opts...)
	}

	t.Run("untrusted server stamps the mcpserver-uid container label", func(t *testing.T) {
		t.Parallel()
		m := newServer(withUntrustedCompliantPolicy())
		r, _ := setupUntrustedReconciler(t, m)
		rc, err := r.createRunConfigFromMCPServer(m)
		require.NoError(t, err)
		assert.Equal(t, string(m.UID), rc.ContainerLabels[untrustedMCPServerUIDLabel],
			"untrusted RunConfig must carry the mcpserver-uid label for STS template resolution")
	})

	t.Run("trusted server does not stamp the label", func(t *testing.T) {
		t.Parallel()
		m := newServer()
		r, _ := setupUntrustedReconciler(t, m)
		rc, err := r.createRunConfigFromMCPServer(m)
		require.NoError(t, err)
		_, present := rc.ContainerLabels[untrustedMCPServerUIDLabel]
		assert.False(t, present, "trusted RunConfig must not carry the untrusted UID label")
	})
}

func assertOwnerRef(t *testing.T, refs []metav1.OwnerReference, m *mcpv1beta1.MCPServer) {
	t.Helper()
	require.Len(t, refs, 1)
	assert.Equal(t, "MCPServer", refs[0].Kind)
	assert.Equal(t, m.Name, refs[0].Name)
	assert.Equal(t, m.UID, refs[0].UID)
}

// assertDNSRuleRestricted pins the tightened DNS egress: DNS may flow only to
// the cluster DNS pods (kube-system + k8s-app=kube-dns) on port 53 — never to
// 0.0.0.0/0:53 (an arbitrary DNS server would smuggle data off-policy).
func assertDNSRuleRestricted(t *testing.T, np *networkingv1.NetworkPolicy) {
	t.Helper()
	for _, rule := range np.Spec.Egress {
		has53 := false
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.IntVal == 53 {
				has53 = true
			}
		}
		if !has53 {
			continue
		}
		require.Len(t, rule.To, 1, "the DNS rule must name exactly one peer (the cluster DNS pods)")
		peer := rule.To[0]
		assert.Nil(t, peer.IPBlock, "DNS must not be an IPBlock rule (that was the 0.0.0.0/0:53 hole)")
		require.NotNil(t, peer.NamespaceSelector)
		assert.Equal(t, map[string]string{"kubernetes.io/metadata.name": "kube-system"},
			peer.NamespaceSelector.MatchLabels)
		require.NotNil(t, peer.PodSelector)
		assert.Equal(t, map[string]string{"k8s-app": "kube-dns"}, peer.PodSelector.MatchLabels)
		return
	}
	t.Fatal("NetworkPolicy must permit DNS (port 53) to the cluster DNS pods")
}

// TestOperatorPolicyContractWithSidecar pins the cross-module contract the
// untrusted pod clone relies on (applyEgressBrokerSidecar in
// pkg/vmcp/session/untrusted/egress.go): the operator-rendered egress policy
// ConfigMap must live at the fixed, generation-free name the sidecar volume
// mounts (egressbroker.ResourceNamesFor(name, "").EgressPolicy), its document
// must round-trip through the sidecar's parser, and it must carry the
// operator-resolved dialAllowlist the broker's D7 guard enforces (the env
// override THV_EGRESSBROKER_DIAL_ALLOWLIST is unset in the clone wiring).
//
//nolint:paralleltest // Swaps the package-level DNS lookup stub (restored after the ensure call).
func TestOperatorPolicyContractWithSidecar(t *testing.T) {
	m := v1beta1test.NewMCPServer("github-mcp", "default", withEgressPolicy())
	stubEgressDNS(t, githubDNS)
	r, _ := setupUntrustedReconciler(t, m)

	require.NoError(t, r.ensureUntrustedResources(t.Context(), m))

	// Name contract: fixed name, no generation suffix (unlike the CA objects,
	// whose generation is read from the STS template annotation at clone time).
	wantName := egressbroker.ResourceNamesFor(m.Name, "").EgressPolicy
	assert.Equal(t, m.Name+"-egress-policy", wantName)
	assert.NotContains(t, wantName, egressbroker.CAGeneration([]byte("x")),
		"the policy ConfigMap name must not be generation-qualified")

	cm := &corev1.ConfigMap{}
	require.NoError(t, r.Get(t.Context(), types.NamespacedName{Name: wantName, Namespace: m.Namespace}, cm))

	// Content contract: the sidecar compiles exactly this document (policy +
	// dialAllowlist); ParsePolicy is the sidecar's own parser.
	policy, err := egressbroker.ParsePolicy([]byte(cm.Data["policy.yaml"]))
	require.NoError(t, err)
	provider, ok := policy.ProviderFor("api.github.com")
	assert.True(t, ok)
	assert.Equal(t, "github", provider)
	assert.Equal(t, []string{"140.82.114.26/32"}, policy.DialAllowlist(),
		"the broker's D7 dial allowlist comes from this document when the env override is unset")

	// The NetworkPolicy ipBlocks and the document's dialAllowlist are the same
	// set (one resolver output feeds both).
	np := &networkingv1.NetworkPolicy{}
	require.NoError(t, r.Get(t.Context(), types.NamespacedName{
		Name: m.Name + untrustedNetworkPolicySuffix, Namespace: m.Namespace}, np))
	var cidrs []string
	for _, rule := range np.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil && peer.IPBlock.CIDR != "127.0.0.1/32" && peer.IPBlock.CIDR != "::1/128" {
				cidrs = append(cidrs, peer.IPBlock.CIDR)
			}
		}
	}
	assert.Equal(t, policy.DialAllowlist(), cidrs)
}

func TestRenderEgressPolicyYAML(t *testing.T) {
	t.Parallel()

	doc, err := renderEgressPolicyYAML(&mcpv1beta1.EgressPolicy{
		Providers: []mcpv1beta1.ProviderEgress{{
			Provider:            "github",
			AllowedHosts:        []string{"api.github.com", "*.githubusercontent.com"},
			AllowedMethods:      []string{"GET", "POST"},
			AllowedPathPrefixes: []string{"/repos/"},
		}},
	}, []string{"140.82.112.0/20"})
	require.NoError(t, err)

	// The render must be symmetric with the sidecar parser: same policy in,
	// same evaluation out — and the dial allowlist travels with the document
	// (same source as the NetworkPolicy ipBlocks, for D7).
	policy, err := egressbroker.ParsePolicy(doc)
	require.NoError(t, err)
	assert.Equal(t, []string{"140.82.112.0/20"}, policy.DialAllowlist())
	provider, ok := policy.ProviderFor("raw.githubusercontent.com")
	assert.True(t, ok)
	assert.Equal(t, "github", provider)
	assert.True(t, policy.Allows("github", "POST", "/repos/x"))
	assert.False(t, policy.Allows("github", "DELETE", "/repos/x"))
	assert.False(t, policy.Allows("github", "GET", "/admin"))
}
