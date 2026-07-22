// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	stderrors "errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/egressbroker"
)

// Untrusted-mode wiring constants (ADR-0001 Wave 3). Resource naming is NOT
// re-declared here: it comes from egressbroker.ResourceNamesFor /
// BaseCASecretName / TrimGeneration, the shared naming contract both sides of
// the module boundary import (the vMCP side resolves the same names in
// pkg/vmcp/session/untrusted/egress.go). Data keys come from
// egressbroker.CAKeyCert / CAKeyKey.
const (
	// untrustedMCPServerUIDLabel is the label the operator stamps on the
	// untrusted backend workload (via RunConfig ContainerLabels → the
	// StatefulSet) so the vMCP pod lifecycle can resolve the clone template by
	// rename-safe UID selector. It mirrors
	// pkg/vmcp/session/untrusted.LabelMCPServerUID across the module boundary.
	untrustedMCPServerUIDLabel = "toolhive.stacklok.dev/mcpserver-uid"
	// untrustedNetworkPolicySuffix names the egress-lockdown NetworkPolicy.
	untrustedNetworkPolicySuffix = "-egress"

	// untrustedCAGenerationAnnotation is stamped on the backend StatefulSet's
	// pod template with the current bump-CA generation. The vMCP pod lifecycle
	// reads it at clone time so a pod cloned mid-rotation mounts one
	// consistent cert/key pair. It mirrors
	// pkg/vmcp/session/untrusted.AnnotationCAGeneration across the module
	// boundary; the contract is pinned by tests.
	untrustedCAGenerationAnnotation = "toolhive.stacklok.dev/bump-ca-generation"
)

// untrustedDNSLookup is the resolver for egress-policy destination hosts.
// Production leaves the stub nil (net.LookupIP). Tests install a stub via
// stubUntrustedDNSLookup, which SERIALIZES the whole calling test (parent or
// subtest): the installer holds untrustedDNSTestMu until the t.Cleanup
// restore runs. Tests that exercise an untrusted ensure must stub (via
// stubEgressDNS or reconcileOnce) — real round-robin DNS answers vary
// between calls and would churn the rendered allowlist if a parallel
// sibling's ensure hit them mid-test. All access is mutex-guarded: an
// unsynced package-level swap races between parallel tests.
var (
	untrustedDNSTestMu     sync.Mutex // held for the lifetime of one stubbing test
	untrustedDNSLookupMu   sync.RWMutex
	untrustedDNSLookupStub func(string) ([]net.IP, error)
)

// untrustedDNSTestLock serializes a test (or subtest) that stubs the egress
// DNS lookup against every other such test and installs the stub for the
// caller's whole lifetime (released via the registered t.Cleanup). A
// same-chain re-entry (parent already holds the lock) installs the stub
// without re-locking, so serial subtests compose with a stubbing parent
// without deadlock.
func untrustedDNSTestLock(t interface {
	Helper()
	Cleanup(func())
}, swap func(string) ([]net.IP, error)) {
	t.Helper()
	untrustedDNSLookupMu.RLock()
	held := untrustedDNSLookupStub != nil
	untrustedDNSLookupMu.RUnlock()
	if !held {
		untrustedDNSTestMu.Lock()
	}
	untrustedDNSLookupMu.Lock()
	untrustedDNSLookupStub = swap
	untrustedDNSLookupMu.Unlock()
	if !held {
		t.Cleanup(func() {
			untrustedDNSLookupMu.Lock()
			untrustedDNSLookupStub = nil
			untrustedDNSLookupMu.Unlock()
			untrustedDNSTestMu.Unlock()
		})
	}
}

// resolveEgressHost resolves one policy destination host through the
// installed stub (tests) or net.LookupIP (production).
func resolveEgressHost(host string) ([]net.IP, error) {
	untrustedDNSLookupMu.RLock()
	stub := untrustedDNSLookupStub
	untrustedDNSLookupMu.RUnlock()
	if stub != nil {
		return stub(host)
	}
	return net.LookupIP(host)
}

// ensureUntrustedResources reconciles the untrusted-mode data-plane
// resources for the MCPServer: the per-tenant bump CA (generation-named
// Secret + bundle ConfigMap), the egress-policy ConfigMap, and the
// egress-lockdown NetworkPolicy. All are ownerRef'd to the MCPServer (GC on
// deletion); the untrusted→trusted flip deletes them explicitly.
//
// Terminal vs transient: policy-shape problems (invalid hosts, a host that
// resolves to nothing, an empty resolved allowlist) are terminal
// *SpecValidationError*s — retrying cannot fix the spec. DNS lookup failures
// wrap egressbroker.ErrDNSResolution and are returned as ordinary errors so
// controller-runtime retries with backoff (a NXDOMAIN blip must not poison
// the workload).
func (r *MCPServerReconciler) ensureUntrustedResources(ctx context.Context, m *mcpv1beta1.MCPServer) error {
	if !isUntrusted(m) {
		return r.deleteUntrustedResources(ctx, m)
	}
	if m.Spec.EgressPolicy == nil {
		return &SpecValidationError{
			Message: "spec.egressPolicy is required when spec.untrusted is true",
		}
	}

	// Compile the CRD policy once, resolve destinations, then render the
	// ConfigMap document with the dial allowlist included so the sidecar's
	// D7 guard and the NetworkPolicy share one source.
	policyDoc, err := renderEgressPolicyYAML(m.Spec.EgressPolicy, nil)
	if err != nil {
		return &SpecValidationError{Message: err.Error()}
	}
	policy, err := egressbroker.ParsePolicy(policyDoc)
	if err != nil {
		return &SpecValidationError{Message: err.Error()}
	}
	dialAllowlist, err := egressbroker.ResolveDialAllowlist(policy, resolveEgressHost)
	if err != nil {
		if stderrors.Is(err, egressbroker.ErrDNSResolution) {
			// Transient: DNS is an external dependency; retry with backoff.
			return fmt.Errorf("failed to resolve untrusted egress destinations: %w", err)
		}
		return &SpecValidationError{Message: err.Error()}
	}
	policyDoc, err = renderEgressPolicyYAML(m.Spec.EgressPolicy, dialAllowlist)
	if err != nil {
		return &SpecValidationError{Message: err.Error()}
	}

	generation, err := r.ensureUntrustedCA(ctx, m)
	if err != nil {
		return fmt.Errorf("failed to ensure bump CA: %w", err)
	}
	if err := r.ensureEgressPolicyConfigMap(ctx, m, policyDoc); err != nil {
		return fmt.Errorf("failed to ensure egress policy ConfigMap: %w", err)
	}
	if err := r.ensureUntrustedNetworkPolicy(ctx, m, dialAllowlist); err != nil {
		return fmt.Errorf("failed to ensure egress NetworkPolicy: %w", err)
	}
	if err := r.stampUntrustedCAGeneration(ctx, m, generation); err != nil {
		return fmt.Errorf("failed to publish bump CA generation: %w", err)
	}
	return r.gcUntrustedCAGenerations(ctx, m, generation)
}

// deleteUntrustedResources removes all untrusted-mode resources (used on the
// untrusted→trusted flip; MCPServer deletion itself is handled by GC through
// the owner references). CA generations are listed by label so every
// retained generation goes, not just the current one.
func (r *MCPServerReconciler) deleteUntrustedResources(ctx context.Context, m *mcpv1beta1.MCPServer) error {
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, client.InNamespace(m.Namespace),
		client.MatchingLabels(untrustedResourceLabels(m))); err != nil {
		return fmt.Errorf("failed to list bump CA Secrets: %w", err)
	}
	for i := range secrets.Items {
		name := secrets.Items[i].Name
		if _, ok := egressbroker.TrimGeneration(name, egressbroker.BaseCASecretName(m.Name)); !ok {
			continue
		}
		if err := r.deleteIfExists(ctx, &corev1.Secret{}, name, m.Namespace, "Secret"); err != nil {
			return err
		}
	}

	bundles := &corev1.ConfigMapList{}
	if err := r.List(ctx, bundles, client.InNamespace(m.Namespace),
		client.MatchingLabels(untrustedResourceLabels(m))); err != nil {
		return fmt.Errorf("failed to list bump CA bundles: %w", err)
	}
	for i := range bundles.Items {
		name := bundles.Items[i].Name
		if _, ok := egressbroker.TrimGeneration(name, egressbroker.BaseCABundleName(m.Name)); !ok {
			continue
		}
		if err := r.deleteIfExists(ctx, &corev1.ConfigMap{}, name, m.Namespace, "ConfigMap"); err != nil {
			return err
		}
	}

	if err := r.deleteIfExists(ctx, &corev1.ConfigMap{},
		egressbroker.ResourceNamesFor(m.Name, "").EgressPolicy, m.Namespace, "ConfigMap"); err != nil {
		return err
	}
	return r.deleteIfExists(ctx, &networkingv1.NetworkPolicy{},
		m.Name+untrustedNetworkPolicySuffix, m.Namespace, "NetworkPolicy")
}

// renderEgressPolicyYAML renders the CRD EgressPolicy into the ConfigMap
// document the sidecar compiles. The render is symmetric with
// egressbroker.ParsePolicy: exactly the CRD fields plus the operator-resolved
// dialAllowlist (the same CIDRs the NetworkPolicy ipBlocks carry, so D7 in
// the sidecar and the pod-level policy enforce the same destination set).
func renderEgressPolicyYAML(policy *mcpv1beta1.EgressPolicy, dialAllowlist []string) ([]byte, error) {
	doc := struct {
		Providers     []egressbroker.ProviderPolicy `yaml:"providers"`
		DialAllowlist []string                      `yaml:"dialAllowlist"`
	}{
		Providers:     make([]egressbroker.ProviderPolicy, 0, len(policy.Providers)),
		DialAllowlist: dialAllowlist,
	}
	for _, p := range policy.Providers {
		doc.Providers = append(doc.Providers, egressbroker.ProviderPolicy{
			Provider:            p.Provider,
			AllowedHosts:        p.AllowedHosts,
			AllowedMethods:      p.AllowedMethods,
			AllowedPathPrefixes: p.AllowedPathPrefixes,
		})
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("failed to render egress policy: %w", err)
	}
	return out, nil
}

// ensureUntrustedCA ensures the per-tenant bump-CA Secret and public bundle
// ConfigMap for the CURRENT CA generation, rotating into a NEW generation
// when the CA enters the rotation window (50% of validity, D9). Returns the
// current generation (cert hash).
//
// Rotation is generation-named, never in-place: cert+key of one CA generation
// live in ONE Secret named <name>-bump-ca-<sha>, and the bundle ConfigMap of
// the same generation carries only the public cert. The backend StatefulSet's
// pod template annotation (see stampUntrustedCAGeneration) names the current
// generation, so a session pod cloned mid-rotation mounts exactly one
// consistent cert/key pair — the old Update-in-place design could hand a pod
// new-key+old-cert during the Get/Update window. The private key exists only
// in the Secret, mounted by the trusted sidecar container. Key material is
// never logged.
func (r *MCPServerReconciler) ensureUntrustedCA(ctx context.Context, m *mcpv1beta1.MCPServer) (string, error) {
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, client.InNamespace(m.Namespace),
		client.MatchingLabels(untrustedResourceLabels(m))); err != nil {
		return "", fmt.Errorf("failed to list bump CA Secrets: %w", err)
	}

	certPEM, keyPEM, err := currentCAMaterial(m.Name, secrets.Items)
	if err != nil {
		return "", err
	}
	generation := egressbroker.CAGeneration(certPEM)
	if keyPEM != nil {
		names := egressbroker.ResourceNamesFor(m.Name, generation)
		if err := r.createCASecretIfAbsent(ctx, m, names.CASecret, certPEM, keyPEM); err != nil {
			return "", err
		}
	}

	// Bundle ConfigMaps carry the public cert of their own generation (never
	// the key). Converge a bundle for EVERY parseable generation Secret —
	// pods cloned before a rotation still mount the previous generation's
	// pair while it is retained, so its bundle must exist and match its
	// Secret's cert. The current Secret may have been created this pass and
	// not be in the pre-pass list, so its cert is seeded explicitly.
	bundleCerts := map[string][]byte{generation: certPEM}
	for i := range secrets.Items {
		s := &secrets.Items[i]
		gen, ok := egressbroker.TrimGeneration(s.Name, egressbroker.BaseCASecretName(m.Name))
		if !ok || gen == generation {
			continue
		}
		if _, err := egressbroker.ParseBumpCA(s.Data[egressbroker.CAKeyCert], s.Data[egressbroker.CAKeyKey]); err != nil {
			continue
		}
		bundleCerts[gen] = s.Data[egressbroker.CAKeyCert]
	}
	for gen, cert := range bundleCerts {
		bundle := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      egressbroker.ResourceNamesFor(m.Name, gen).CABundle,
				Namespace: m.Namespace,
				Labels:    untrustedResourceLabels(m),
			},
			Data: map[string]string{egressbroker.CAKeyCert: string(cert)},
		}
		if err := controllerutil.SetControllerReference(m, bundle, r.Scheme); err != nil {
			return "", fmt.Errorf("failed to set owner reference on bump CA bundle: %w", err)
		}
		if err := upsertConfigMapData(ctx, r.Client, bundle); err != nil {
			return "", fmt.Errorf("failed to upsert bump CA bundle ConfigMap: %w", err)
		}
	}
	return generation, nil
}

// currentCAMaterial selects the current CA generation from the listed
// generation Secrets and returns its cert PEM (plus key PEM only when a fresh
// CA was minted this pass — no Secret, or rotation due). The current
// generation is the parseable Secret with the latest notAfter; unparseable
// Secrets are ignored here (the GC pass deletes them — they match no
// published generation).
func currentCAMaterial(mcpserverName string, secrets []corev1.Secret) (certPEM, keyPEM []byte, err error) {
	var (
		current     *corev1.Secret
		currentCert *time.Time
	)
	for i := range secrets {
		s := &secrets[i]
		if _, ok := egressbroker.TrimGeneration(s.Name, egressbroker.BaseCASecretName(mcpserverName)); !ok {
			continue
		}
		loaded, err := egressbroker.ParseBumpCA(s.Data[egressbroker.CAKeyCert], s.Data[egressbroker.CAKeyKey])
		if err != nil {
			continue
		}
		notAfter := loaded.NotAfter()
		if currentCert == nil || notAfter.After(*currentCert) {
			current, currentCert = s, &notAfter
		}
	}

	if current == nil {
		return mintBumpCA(mcpserverName)
	}
	loaded, err := egressbroker.ParseBumpCA(
		current.Data[egressbroker.CAKeyCert], current.Data[egressbroker.CAKeyKey])
	if err != nil {
		return nil, nil, fmt.Errorf("failed to re-parse bump CA Secret %q: %w", current.Name, err)
	}
	if !loaded.NeedsRotation(time.Now()) {
		return current.Data[egressbroker.CAKeyCert], nil, nil
	}
	// Rotation due: mint a NEW generation alongside the old one. Existing
	// pods keep trusting the CA their emptyDir was seeded with; new session
	// pods clone the new template (no cross-pod TLS).
	return mintBumpCA(mcpserverName)
}

// createCASecretIfAbsent creates the generation-named bump-CA Secret when it
// does not exist yet. OwnerRef'd to the MCPServer for GC. Generation Secrets
// are immutable by construction: a name collides only when the same cert was
// re-minted (impossible — the mint includes a fresh key and serial), so an
// AlreadyExists on create means a concurrent reconcile won and its content
// is identical by definition of the generation hash; the call is then a
// no-op.
func (r *MCPServerReconciler) createCASecretIfAbsent(
	ctx context.Context, m *mcpv1beta1.MCPServer, name string, certPEM, keyPEM []byte,
) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.Namespace,
			Labels:    untrustedResourceLabels(m),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			egressbroker.CAKeyCert: certPEM,
			egressbroker.CAKeyKey:  keyPEM,
		},
	}
	if err := controllerutil.SetControllerReference(m, secret, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on bump CA Secret: %w", err)
	}
	if err := r.Create(ctx, secret); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create bump CA Secret: %w", err)
	}
	return nil
}

// gcUntrustedCAGenerations keeps the current CA generation and the previous
// one (N and N-1: pods cloned just before the rotation still mount N-1, and
// their idle TTL outlives the next rotation window), deleting every older
// generation's Secret and bundle. Deletions are best-effort-collected: a
// failure is returned so the reconcile retries (orphan generations would
// otherwise accumulate forever).
func (r *MCPServerReconciler) gcUntrustedCAGenerations(ctx context.Context, m *mcpv1beta1.MCPServer, currentGen string) error {
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, client.InNamespace(m.Namespace),
		client.MatchingLabels(untrustedResourceLabels(m))); err != nil {
		return fmt.Errorf("failed to list bump CA Secrets for GC: %w", err)
	}
	previousGen := previousCAGenerationFromSecrets(secrets.Items, m.Name, currentGen)
	keep := map[string]bool{currentGen: true}
	if previousGen != "" {
		keep[previousGen] = true
	}
	for i := range secrets.Items {
		s := &secrets.Items[i]
		gen, ok := egressbroker.TrimGeneration(s.Name, egressbroker.BaseCASecretName(m.Name))
		if !ok || keep[gen] {
			continue
		}
		if err := r.deleteIfExists(ctx, &corev1.Secret{}, s.Name, m.Namespace, "Secret"); err != nil {
			return err
		}
	}

	bundles := &corev1.ConfigMapList{}
	if err := r.List(ctx, bundles, client.InNamespace(m.Namespace),
		client.MatchingLabels(untrustedResourceLabels(m))); err != nil {
		return fmt.Errorf("failed to list bump CA bundles for GC: %w", err)
	}
	for i := range bundles.Items {
		b := &bundles.Items[i]
		gen, ok := egressbroker.TrimGeneration(b.Name, egressbroker.BaseCABundleName(m.Name))
		if !ok || keep[gen] {
			continue
		}
		if err := r.deleteIfExists(ctx, &corev1.ConfigMap{}, b.Name, m.Namespace, "ConfigMap"); err != nil {
			return err
		}
	}
	return nil
}

// previousCAGenerationFromSecrets returns the parseable CA generation with
// the latest notAfter strictly older than currentGen's, or "" when there is
// none. notAfter (not creation order) orders generations: two rotations in
// quick succession still keep the immediately-previous CA. secrets is the
// caller's already-fetched list (the operator lists these Secrets once per
// reconcile pass).
func previousCAGenerationFromSecrets(
	secrets []corev1.Secret, mcpserverName, currentGen string,
) string {
	type candidate struct {
		gen      string
		notAfter time.Time
	}
	var current *candidate
	candidates := map[string]candidate{}
	for i := range secrets {
		s := &secrets[i]
		gen, ok := egressbroker.TrimGeneration(s.Name, egressbroker.BaseCASecretName(mcpserverName))
		if !ok {
			continue
		}
		loaded, err := egressbroker.ParseBumpCA(s.Data[egressbroker.CAKeyCert], s.Data[egressbroker.CAKeyKey])
		if err != nil {
			continue
		}
		c := candidate{gen: gen, notAfter: loaded.NotAfter()}
		candidates[gen] = c
		if gen == currentGen {
			cc := c
			current = &cc
		}
	}
	if current == nil {
		return ""
	}
	previous := ""
	for _, c := range candidates {
		if c.gen == currentGen || !c.notAfter.Before(current.notAfter) {
			continue
		}
		if previous == "" || c.notAfter.After(candidates[previous].notAfter) {
			previous = c.gen
		}
	}
	return previous
}

// stampUntrustedCAGeneration publishes the current bump-CA generation on the
// backend StatefulSet's pod template annotation so the vMCP pod lifecycle
// clones pods mounting exactly that generation's Secret/bundle (mid-rotation
// consistency). A no-op when the annotation is already current (idempotent
// reconcile: no spurious STS write).
func (r *MCPServerReconciler) stampUntrustedCAGeneration(
	ctx context.Context, m *mcpv1beta1.MCPServer, generation string,
) error {
	sts, err := r.findUntrustedBackendSTS(ctx, m)
	if err != nil || sts == nil {
		// The STS may not exist yet (first reconcile precedes the workload
		// deploy); the next reconcile stamps it. Session pods cannot be cloned
		// before the STS exists either, so no pod ever runs unstamped.
		return err
	}
	if sts.Spec.Template.Annotations[untrustedCAGenerationAnnotation] == generation {
		return nil
	}
	patch := client.MergeFrom(sts.DeepCopy())
	annotations := map[string]string{}
	for k, v := range sts.Spec.Template.Annotations {
		annotations[k] = v
	}
	annotations[untrustedCAGenerationAnnotation] = generation
	sts.Spec.Template.Annotations = annotations
	if err := r.Patch(ctx, sts, patch); err != nil {
		return fmt.Errorf("failed to stamp CA generation on backend StatefulSet: %w", err)
	}
	return nil
}

// findUntrustedBackendSTS resolves the MCPServer's backend StatefulSet by the
// same selector the vMCP pod lifecycle lists with (mcpserver-uid label +
// toolhive=true). Returns (nil, nil) when no StatefulSet carries the label
// yet. More than one match is an error — the lifecycle would fail closed on
// the same ambiguity.
func (r *MCPServerReconciler) findUntrustedBackendSTS(
	ctx context.Context, m *mcpv1beta1.MCPServer,
) (*appsv1.StatefulSet, error) {
	list := &appsv1.StatefulSetList{}
	if err := r.List(ctx, list,
		client.InNamespace(m.Namespace),
		client.MatchingLabels{untrustedMCPServerUIDLabel: string(m.UID), "toolhive": "true"},
	); err != nil {
		return nil, fmt.Errorf("failed to list backend StatefulSets: %w", err)
	}
	switch len(list.Items) {
	case 0:
		return nil, nil
	case 1:
		return &list.Items[0], nil
	default:
		return nil, fmt.Errorf("expected at most one backend StatefulSet for untrusted MCPServer %q, found %d",
			m.Name, len(list.Items))
	}
}

// ensureEgressPolicyConfigMap converges the sidecar-consumed policy document
// on the CRD's EgressPolicy.
func (r *MCPServerReconciler) ensureEgressPolicyConfigMap(
	ctx context.Context, m *mcpv1beta1.MCPServer, policyDoc []byte,
) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      egressbroker.ResourceNamesFor(m.Name, "").EgressPolicy,
			Namespace: m.Namespace,
			Labels:    untrustedResourceLabels(m),
		},
		Data: map[string]string{"policy.yaml": string(policyDoc)},
	}
	if err := controllerutil.SetControllerReference(m, cm, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on egress policy ConfigMap: %w", err)
	}
	return upsertConfigMapData(ctx, r.Client, cm)
}

// ensureUntrustedNetworkPolicy converges the pod-level egress lockdown for
// untrusted session pods (D7): the only permitted egress is loopback (the
// sidecar), cluster DNS (kube-system/kube-dns pods only), and the
// policy-resolved destination CIDRs.
//
// NetworkPolicy is pod-level, not container-level — the backend container's
// confinement is enforced by routing (HTTP(S)_PROXY env) and by the fact
// that the credential never resides in the backend container at all; see
// spec-wave3 §4.1 for the documented container-separation limitation. The
// load-bearing controls are the sidecar's destination binding (D5) and
// per-dial IP validation (D7).
func (r *MCPServerReconciler) ensureUntrustedNetworkPolicy(
	ctx context.Context, m *mcpv1beta1.MCPServer, dialAllowlist []string,
) error {
	desired := desiredUntrustedNetworkPolicy(m, dialAllowlist)
	if err := controllerutil.SetControllerReference(m, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on egress NetworkPolicy: %w", err)
	}

	existing := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create egress NetworkPolicy: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get egress NetworkPolicy: %w", err)
	}
	if networkPolicyNeedsUpdate(existing, desired) {
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update egress NetworkPolicy: %w", err)
		}
	}
	return nil
}

const (
	// dnsNamespaceSelectorName is the namespace of the cluster DNS
	// deployment on kubeadm/kind/managed clusters. Documented limitation: on
	// clusters whose DNS lives elsewhere (e.g. a custom namespace), DNS
	// egress is denied until the deployment is adjusted — an explicit,
	// visible failure (backend DNS lookups fail loudly) rather than a silent
	// port-53-to-anywhere hole.
	dnsNamespaceSelectorName = "kube-system"
	// dnsPodSelectorName matches CoreDNS/kube-dns pods by their standard
	// k8s-app label (kubeadm, kind, GKE, EKS default addons all use it).
	dnsPodSelectorName = "kube-dns"
)

// desiredUntrustedNetworkPolicy renders the egress lockdown.
func desiredUntrustedNetworkPolicy(
	m *mcpv1beta1.MCPServer, dialAllowlist []string,
) *networkingv1.NetworkPolicy {
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	dnsPort := intstr.FromInt32(53)

	// Destination allowlist (operator-resolved AllowedHosts; best-effort
	// defense-in-depth — the sidecar re-validates per dial, D7). The list is
	// sorted by the resolver, so rule order is deterministic (no churn).
	destinationRule := networkingv1.NetworkPolicyEgressRule{}
	for _, cidr := range dialAllowlist {
		destinationRule.To = append(destinationRule.To,
			networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}})
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + untrustedNetworkPolicySuffix,
			Namespace: m.Namespace,
			Labels:    untrustedResourceLabels(m),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
				"toolhive.stacklok.dev/untrusted":     "true",
				"toolhive.stacklok.dev/mcpserver-uid": string(m.UID),
			}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				// Loopback: the in-pod sidecar (Envoy + broker).
				{To: []networkingv1.NetworkPolicyPeer{
					{IPBlock: &networkingv1.IPBlock{CIDR: "127.0.0.1/32"}},
					{IPBlock: &networkingv1.IPBlock{CIDR: "::1/128"}},
				}},
				// DNS: restricted to the cluster DNS pods (kube-system +
				// k8s-app=kube-dns) — never 0.0.0.0/0:53, which would let the
				// backend run or reach an arbitrary DNS server to smuggle data
				// or resolve attacker-controlled names off-policy.
				{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": dnsNamespaceSelectorName,
						}},
						PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
							"k8s-app": dnsPodSelectorName,
						}},
					}},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udp, Port: &dnsPort},
						{Protocol: &tcp, Port: &dnsPort},
					},
				},
				destinationRule,
			},
		},
	}
}

// upsertConfigMapData creates the ConfigMap or updates only its Data (and
// labels) when they drift. A no-op reconcile performs no write (idempotency:
// unchanged ResourceVersion). The operator is the sole writer of these
// ConfigMaps, so a selective Update after Get is safe.
func upsertConfigMapData(ctx context.Context, c client.Client, desired *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	err := c.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		if err := c.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create ConfigMap %s: %w", desired.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get ConfigMap %s: %w", desired.Name, err)
	}
	if stringMapEqual(existing.Data, desired.Data) && stringMapEqual(existing.Labels, desired.Labels) {
		return nil
	}
	existing.Data = desired.Data
	existing.Labels = desired.Labels
	if err := c.Update(ctx, existing); err != nil {
		return fmt.Errorf("failed to update ConfigMap %s: %w", desired.Name, err)
	}
	return nil
}

// mintBumpCA generates a fresh per-tenant bump CA and returns the PEM cert +
// key. The key bytes are handled only long enough to write the Secret; they
// are never logged.
func mintBumpCA(mcpserverName string) (certPEM, keyPEM []byte, err error) {
	ca, err := egressbroker.GenerateBumpCA(mcpserverName+"-bump-ca", time.Now())
	if err != nil {
		return nil, nil, err
	}
	key, err := ca.KeyPEM()
	if err != nil {
		return nil, nil, err
	}
	return ca.CertPEM(), key, nil
}

// untrustedResourceLabels identifies operator-managed untrusted resources.
func untrustedResourceLabels(m *mcpv1beta1.MCPServer) map[string]string {
	labels := labelsForMCPServer(m.Name)
	labels["toolhive.stacklok.dev/untrusted-resource"] = "true"
	return labels
}

// stringMapEqual compares two string maps for exact equality.
func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// networkPolicyNeedsUpdate reports whether the live NetworkPolicy differs
// from desired in the fields the operator owns.
func networkPolicyNeedsUpdate(existing, desired *networkingv1.NetworkPolicy) bool {
	if !stringMapEqual(existing.Labels, desired.Labels) {
		return true
	}
	return renderedEgressRules(existing.Spec.Egress) != renderedEgressRules(desired.Spec.Egress)
}

// renderedEgressRules canonicalizes egress rules for comparison: peers and
// ports are sorted per rule and rules are sorted, so semantically equal
// policies compare equal regardless of rule order.
func renderedEgressRules(rules []networkingv1.NetworkPolicyEgressRule) string {
	rendered := make([]string, 0, len(rules))
	for _, rule := range rules {
		var peers []string
		for _, to := range rule.To {
			if to.IPBlock != nil {
				peers = append(peers, "ip:"+to.IPBlock.CIDR)
			}
			if to.PodSelector != nil {
				peers = append(peers, fmt.Sprintf("pod:%v", to.PodSelector.MatchLabels))
			}
			if to.NamespaceSelector != nil {
				peers = append(peers, fmt.Sprintf("ns:%v", to.NamespaceSelector.MatchLabels))
			}
		}
		sort.Strings(peers)
		var ports []string
		for _, p := range rule.Ports {
			proto := ""
			if p.Protocol != nil {
				proto = string(*p.Protocol)
			}
			port := ""
			if p.Port != nil {
				port = p.Port.String()
			}
			ports = append(ports, proto+"/"+port)
		}
		sort.Strings(ports)
		rendered = append(rendered, strings.Join(peers, ",")+"|"+strings.Join(ports, ","))
	}
	sort.Strings(rendered)
	return strings.Join(rendered, ";")
}
