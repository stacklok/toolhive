// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

// withUntrustedSpec marks the MCPServer fixture as untrusted via the
// Wave-1 spec.untrusted field.
func withUntrustedSpec() v1beta1test.MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.Spec.Untrusted = true
	}
}

// withUntrustedCompliantPolicy adds a minimal valid EgressPolicy (hostname
// destination; reconcile-path tests stub untrustedDNSLookup so no real DNS
// resolution happens). Wave 3: untrusted servers without an EgressPolicy
// terminate at the egress-resource gate, so fixtures that must proceed down
// the normal reconcile path need this.
func withUntrustedCompliantPolicy() v1beta1test.MCPServerOption {
	return v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
		m.Spec.Untrusted = true
		m.Spec.EgressPolicy = &mcpv1beta1.EgressPolicy{
			Providers: []mcpv1beta1.ProviderEgress{{
				Provider:     "github",
				AllowedHosts: []string{"api.github.com"},
			}},
		}
	})
}

func withSecrets(secrets ...mcpv1beta1.SecretRef) v1beta1test.MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.Spec.Secrets = secrets
	}
}

// TestIsUntrusted pins the Wave-1 body of isUntrusted: it reads spec.untrusted
// and nothing else — in particular the interim Wave-0 annotation is inert.
func TestIsUntrusted(t *testing.T) {
	t.Parallel()

	assert.False(t, isUntrusted(v1beta1test.NewMCPServer("plain", "default")))
	assert.True(t, isUntrusted(v1beta1test.NewMCPServer("flagged", "default", withUntrustedSpec())))

	annotated := v1beta1test.NewMCPServer("annotated", "default", v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
		m.Annotations = map[string]string{"toolhive.stacklok.dev/untrusted": "true"}
	}))
	assert.False(t, isUntrusted(annotated), "the interim Wave-0 annotation must have no effect in Wave 1")
}

// setupUntrustedReconciler builds a fake-client-backed reconciler for the
// untrusted env gate tests and returns it with the event recorder.
func setupUntrustedReconciler(t *testing.T, objs ...client.Object) (*MCPServerReconciler, *events.FakeRecorder) {
	t.Helper()

	s := testutil.NewScheme(t)
	eventRecorder := events.NewFakeRecorder(10)

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()

	r := &MCPServerReconciler{
		Client:           fakeClient,
		Scheme:           s,
		Recorder:         eventRecorder,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}
	return r, eventRecorder
}

// reconcileOnce runs a single Reconcile for the given MCPServer fixture. The
// untrusted egress gate resolves policy destinations through the package-level
// untrustedDNSLookup stub; this helper installs the fixture stub for the whole
// test lifetime (t.Cleanup restores it) so a mid-test restore never leaves a
// parallel sibling's Reconcile hitting real DNS.
func reconcileOnce(t *testing.T, r *MCPServerReconciler, mcpServer *mcpv1beta1.MCPServer) ctrl.Result {
	t.Helper()

	// Install the fixture stub ONCE per test, held for its whole lifetime.
	// untrustedDNSTestLock treats any installed stub (including a parallel
	// sibling's) as "held" and skips locking, so calling it per reconcile
	// lets a multi-reconcile test overwrite another test's stub mid-flight.
	// stubOnce guarantees one lock+install per test no matter how many
	// reconcileOnce calls follow; t.Cleanup releases it.
	stubUntrustedDNSOnce(t)

	ctx := log.IntoContext(t.Context(), log.Log)
	req := ctrl.Request{NamespacedName: types.NamespacedName{
		Name:      mcpServer.Name,
		Namespace: mcpServer.Namespace,
	}}
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	return result
}

// stubUntrustedDNSOnce installs the fixture DNS stub exactly once per test,
// holding untrustedDNSTestMu for the test's whole lifetime. Reconciles after
// the first reuse the already-installed stub; a parallel sibling blocks on
// the mutex until t.Cleanup releases it. This removes the re-entry race in
// untrustedDNSTestLock that let a multi-reconcile test clobber a sibling's
// stub without holding the lock.
var untrustedDNSStubOnce sync.Map // *testing.T -> struct{}{}

func stubUntrustedDNSOnce(t *testing.T) {
	t.Helper()
	if _, done := untrustedDNSStubOnce.LoadOrStore(t, struct{}{}); done {
		return
	}
	untrustedDNSTestMu.Lock()
	untrustedDNSLookupMu.Lock()
	untrustedDNSLookupStub = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("140.82.114.26")}, nil
	}
	untrustedDNSLookupMu.Unlock()
	t.Cleanup(func() {
		untrustedDNSLookupMu.Lock()
		untrustedDNSLookupStub = nil
		untrustedDNSLookupMu.Unlock()
		untrustedDNSTestMu.Unlock()
		untrustedDNSStubOnce.Delete(t)
	})
}

func TestMCPServerReconciler_UntrustedSecretEnvRejected(t *testing.T) {
	t.Parallel()

	mcpServer := v1beta1test.NewMCPServer("untrusted-secrets", "default",
		withUntrustedSpec(),
		withSecrets(mcpv1beta1.SecretRef{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}),
	)

	r, recorder := setupUntrustedReconciler(t, mcpServer)

	// Reconcile #11: spec.untrusted + spec.secrets → terminal rejection.
	result := reconcileOnce(t, r, mcpServer)
	assert.True(t, result.IsZero(), "terminal spec error must not requeue or schedule a retry")

	updated := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))

	condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, condition, "Valid condition should be set")
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonSecretEnvRejected, condition.Reason)
	assert.Equal(t, updated.Generation, condition.ObservedGeneration)
	assert.Contains(t, condition.Message, `env var "API_TOKEN"`)
	assert.Contains(t, condition.Message, `Secret "backend-creds"`)

	ready := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeReady)
	require.NotNil(t, ready, "Ready condition should be set")
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, mcpv1beta1.MCPServerPhaseFailed, updated.Status.Phase)

	// One-shot Warning event on the false-transition.
	require.Eventually(t, func() bool {
		return countContaining(drainEvents(recorder), mcpv1beta1.ConditionReasonSecretEnvRejected) == 1
	}, 2*time.Second, 10*time.Millisecond, "expected one Warning event on the invalid transition")

	// No Deployment may be created for the rejected workload.
	dep := &appsv1.Deployment{}
	err := r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), dep)
	assert.True(t, apierrors.IsNotFound(err), "no Deployment should be created for a rejected workload")

	// Reconcile #14 (idempotency): the terminal branch itself must be a no-op on
	// the second reconcile — the Valid/Ready conditions it owns must be
	// byte-identical afterwards (proving no spurious write or churn from the
	// gate), and no duplicate event must fire. Note: the pre-existing advisory
	// validators (stdio replica cap, session storage, rate limit) perform their
	// own legacy write-per-reconcile, so a whole-object ResourceVersion
	// comparison is not meaningful here; the gate's idempotency is scoped to
	// what it owns.
	result = reconcileOnce(t, r, mcpServer)
	assert.True(t, result.IsZero())

	afterSecond := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), afterSecond))

	conditionAfter := meta.FindStatusCondition(afterSecond.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, conditionAfter)
	assert.Equal(t, *condition, *conditionAfter, "gate-owned Valid condition must not churn on re-observe")
	readyAfter := meta.FindStatusCondition(afterSecond.Status.Conditions, mcpv1beta1.ConditionTypeReady)
	require.NotNil(t, readyAfter)
	assert.Equal(t, *ready, *readyAfter, "gate-owned Ready condition must not churn on re-observe")
	assert.Equal(t, updated.Status.Phase, afterSecond.Status.Phase)
	assert.Equal(t, updated.Status.Message, afterSecond.Status.Message)

	time.Sleep(50 * time.Millisecond) // let any async event emission settle
	assert.Empty(t, drainEvents(recorder), "no event expected while staying invalid")
}

func TestMCPServerReconciler_TrustedSecretsDeployAsToday(t *testing.T) {
	t.Parallel()

	// #12: identical spec.secrets but spec.untrusted=false — no behavior change; the
	// reconcile must proceed past the gate (it will requeue waiting on the
	// runconfig ConfigMap, which is the normal pre-Deployment path).
	mcpServer := v1beta1test.NewMCPServer("trusted-secrets", "default",
		withSecrets(mcpv1beta1.SecretRef{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}),
	)

	r, _ := setupUntrustedReconciler(t, mcpServer)

	result := reconcileOnce(t, r, mcpServer)

	updated := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))

	condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	assert.Nil(t, condition, "no Valid condition should be recorded for trusted workloads")
	assert.NotEqual(t, mcpv1beta1.MCPServerPhaseFailed, updated.Status.Phase)
	assert.False(t, result.IsZero(),
		"trusted reconcile should continue down the normal path (requeue waiting on runconfig)")
}

// TestMCPServerReconciler_InterimAnnotationIsInert pins the Wave-1 removal of the
// interim Wave-0 opt-in annotation: annotation set + spec.untrusted=false + secrets
// must sail past the gate exactly like a trusted workload.
func TestMCPServerReconciler_InterimAnnotationIsInert(t *testing.T) {
	t.Parallel()

	mcpServer := v1beta1test.NewMCPServer("annotated-but-trusted", "default",
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Annotations = map[string]string{"toolhive.stacklok.dev/untrusted": "true"}
		}),
		withSecrets(mcpv1beta1.SecretRef{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}),
	)

	r, _ := setupUntrustedReconciler(t, mcpServer)

	result := reconcileOnce(t, r, mcpServer)

	updated := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))

	condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	assert.Nil(t, condition, "the interim Wave-0 annotation must no longer arm the gate")
	assert.NotEqual(t, mcpv1beta1.MCPServerPhaseFailed, updated.Status.Phase)
	assert.False(t, result.IsZero(),
		"annotated-but-trusted reconcile should continue down the normal path")
}

func TestMCPServerReconciler_UntrustedRawTemplateSecretEnvRejected(t *testing.T) {
	t.Parallel()

	// #13: spec.untrusted + raw podTemplateSpec smuggling secretKeyRef onto the mcp container.
	mcpServer := v1beta1test.NewMCPServer("untrusted-raw-template", "default",
		withUntrustedSpec(),
		v1beta1test.WithPodTemplateSpec(&runtime.RawExtension{
			Raw: []byte(`{"spec":{"containers":[{"name":"mcp","env":[{"name":"API_TOKEN","valueFrom":{"secretKeyRef":{"name":"smuggled-secret","key":"token"}}}]}]}}`),
		}),
	)

	r, recorder := setupUntrustedReconciler(t, mcpServer)

	result := reconcileOnce(t, r, mcpServer)
	assert.True(t, result.IsZero())

	updated := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))

	condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, condition, "Valid condition should be set")
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonSecretEnvRejected, condition.Reason)
	assert.Contains(t, condition.Message, `env var "API_TOKEN"`)
	assert.Contains(t, condition.Message, `Secret "smuggled-secret"`)

	require.Eventually(t, func() bool {
		return countContaining(drainEvents(recorder), mcpv1beta1.ConditionReasonSecretEnvRejected) == 1
	}, 2*time.Second, 10*time.Millisecond, "expected one Warning event on the invalid transition")
}

func TestMCPServerReconciler_UntrustedCompliantDeploys(t *testing.T) {
	t.Parallel()

	// #15: untrusted but compliant (literal env only) —
	// the gate passes and the reconcile proceeds normally.
	mcpServer := v1beta1test.NewMCPServer("untrusted-compliant", "default",
		withUntrustedCompliantPolicy(),
		v1beta1test.WithEnv(mcpv1beta1.EnvVar{Name: "SENTINEL", Value: "literal-value"}),
		v1beta1test.WithPodTemplateSpec(&runtime.RawExtension{
			Raw: []byte(`{"spec":{"containers":[{"name":"mcp","env":[{"name":"LITERAL","value":"ok"}]}]}}`),
		}),
	)

	r, _ := setupUntrustedReconciler(t, mcpServer)

	result := reconcileOnce(t, r, mcpServer)

	updated := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))

	condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	assert.Nil(t, condition, "compliant untrusted workload must not get a Valid=False condition")
	assert.NotEqual(t, mcpv1beta1.MCPServerPhaseFailed, updated.Status.Phase)
	assert.False(t, result.IsZero(),
		"compliant untrusted reconcile should continue down the normal path")
}

// TestMCPServerReconciler_UntrustedLatchClearsAndWarningReArms pins the
// rejected → fixed → rejected lifecycle:
//  1. A rejected spec latches Valid=False (one Warning).
//  2. Fixing the spec clears the Valid condition and the workload proceeds
//     past the gate (requeue waiting on runconfig, like any valid spec).
//  3. Re-breaking the spec latches Valid=False again AND the one-shot Warning
//     fires a second time — proving the pass path genuinely un-poisons the
//     latch instead of leaving the workload permanently silenced.
func TestMCPServerReconciler_UntrustedLatchClearsAndWarningReArms(t *testing.T) {
	t.Parallel()

	ctx := log.IntoContext(t.Context(), log.Log)

	// Start REJECTED: untrusted + spec.secrets.
	mcpServer := v1beta1test.NewMCPServer("untrusted-latch", "default",
		withUntrustedCompliantPolicy(),
		withSecrets(mcpv1beta1.SecretRef{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}),
	)
	r, recorder := setupUntrustedReconciler(t, mcpServer)

	result := reconcileOnce(t, r, mcpServer)
	assert.True(t, result.IsZero(), "rejected spec is terminal: no requeue")
	require.Eventually(t, func() bool {
		return countContaining(drainEvents(recorder), mcpv1beta1.ConditionReasonSecretEnvRejected) == 1
	}, 2*time.Second, 10*time.Millisecond, "expected the first Warning on the invalid transition")

	current := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(mcpServer), current))
	require.NotNil(t, meta.FindStatusCondition(current.Status.Conditions, mcpv1beta1.ConditionTypeValid),
		"Valid=False must be latched on rejection")

	// FIX the spec: drop spec.secrets. The gate must clear the latched Valid
	// condition and let the reconcile proceed down the normal path.
	current.Spec.Secrets = nil
	require.NoError(t, r.Update(ctx, current))

	result = reconcileOnce(t, r, mcpServer)
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(mcpServer), current))
	assert.Nil(t, meta.FindStatusCondition(current.Status.Conditions, mcpv1beta1.ConditionTypeValid),
		"passing the gate must clear the latched Valid condition")
	assert.NotEqual(t, mcpv1beta1.ConditionReasonSecretEnvRejected, current.Status.Message,
		"passing the gate must clear the latched gate-rejection message")
	assert.False(t, result.IsZero(),
		"fixed workload must proceed down the normal path (requeue waiting on runconfig)")

	// RE-BREAK the spec: the latch must re-engage and — critically — the
	// Warning must fire again, which only happens when the pass path above
	// removed the Valid=False latch (wasInvalid would otherwise stay true).
	current.Spec.Secrets = []mcpv1beta1.SecretRef{{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}}
	require.NoError(t, r.Update(ctx, current))

	result = reconcileOnce(t, r, mcpServer)
	assert.True(t, result.IsZero())
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(mcpServer), current))
	condition := meta.FindStatusCondition(current.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, condition, "Valid=False must latch again on re-rejection")
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	require.Eventually(t, func() bool {
		return countContaining(drainEvents(recorder), mcpv1beta1.ConditionReasonSecretEnvRejected) == 1
	}, 2*time.Second, 10*time.Millisecond,
		"the Warning must fire on the second invalid transition (latch-poisoning case)")
}

func TestDeploymentForMCPServer_UntrustedSecretEnvRejectedAtBuildTime(t *testing.T) {
	t.Parallel()

	// Defense-in-depth: deploymentForMCPServer itself rejects the built patch
	// for untrusted workloads (spec.secrets seam)...
	mcpServer := v1beta1test.NewMCPServer("untrusted-build-gate", "default",
		withUntrustedSpec(),
		withSecrets(mcpv1beta1.SecretRef{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}),
	)

	r, _ := setupUntrustedReconciler(t, mcpServer)

	ctx := log.IntoContext(t.Context(), log.Log)
	deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.Error(t, err)
	assert.Nil(t, deployment)
	assert.Contains(t, err.Error(), `env var "API_TOKEN"`)
	assert.Contains(t, err.Error(), `Secret "backend-creds"`)

	// The defense-in-depth rejection is a typed SpecValidationError so the
	// reconcile call sites peel it with errors.As into terminal treatment
	// (condition, no requeue) instead of a transient forever-backoff.
	var specErr *SpecValidationError
	assert.ErrorAs(t, err, &specErr, "gate rejection must be a terminal typed error")

	// ...and for a trusted workload the same secrets pass the build gate.
	trusted := v1beta1test.NewMCPServer("trusted-build-gate", "default",
		withSecrets(mcpv1beta1.SecretRef{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}),
	)
	deployment, err = r.deploymentForMCPServer(ctx, trusted, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment)
}

// TestDeploymentForMCPServer_UntrustedSentinelInjection pins the Wave-1 sentinel
// seam in deploymentForMCPServer: for an untrusted workload with a declared
// credentialEnvName, the --k8s-pod-patch argument carries the literal sentinel
// env var on the mcp container, and the pod patch still passes the Wave-0 gate.
func TestDeploymentForMCPServer_UntrustedSentinelInjection(t *testing.T) {
	t.Parallel()

	mcpServer := v1beta1test.NewMCPServer("untrusted-sentinel", "default",
		withUntrustedSpec(),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.EgressPolicy = &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{{
				Provider:          "github",
				AllowedHosts:      []string{"api.github.com"},
				CredentialEnvName: "GITHUB_TOKEN",
			}}}
		}),
	)

	r, _ := setupUntrustedReconciler(t, mcpServer)
	ctx := log.IntoContext(t.Context(), log.Log)

	deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment)

	patch := podPatchFromDeployment(t, deployment)
	require.Len(t, patch.Spec.Containers, 1)
	env := patch.Spec.Containers[0].Env
	require.Len(t, env, 1, "exactly one sentinel env var expected")
	assert.Equal(t, "GITHUB_TOKEN", env[0].Name)
	assert.Equal(t, "thv-untrusted-sentinel:github", env[0].Value)
	assert.Nil(t, env[0].ValueFrom, "sentinel env must be a literal Value, never ValueFrom")

	// The injected patch composes with the Wave-0 gate.
	require.NoError(t, ctrlutil.ValidateNoSecretEnvForUntrusted(patch, "mcp", true))
}

// TestDeploymentForMCPServer_UntrustedSentinelCollision pins the terminal rejection
// when a declared credentialEnvName collides with user-declared env.
func TestDeploymentForMCPServer_UntrustedSentinelCollision(t *testing.T) {
	t.Parallel()

	mcpServer := v1beta1test.NewMCPServer("untrusted-collision", "default",
		withUntrustedSpec(),
		v1beta1test.WithEnv(mcpv1beta1.EnvVar{Name: "GITHUB_TOKEN", Value: "user-value"}),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"containers":[{"name":"mcp","env":[{"name":"GITHUB_TOKEN","value":"user-value"}]}]}}`),
			}
			m.Spec.EgressPolicy = &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{{
				Provider:          "github",
				AllowedHosts:      []string{"api.github.com"},
				CredentialEnvName: "GITHUB_TOKEN",
			}}}
		}),
	)

	r, _ := setupUntrustedReconciler(t, mcpServer)
	ctx := log.IntoContext(t.Context(), log.Log)

	deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.Error(t, err)
	assert.Nil(t, deployment)
	assert.Contains(t, err.Error(), `env var "GITHUB_TOKEN" on container "mcp" already exists with a different value`)

	var specErr *SpecValidationError
	assert.ErrorAs(t, err, &specErr, "sentinel collision must be a terminal typed error")
}

// TestDeploymentForMCPServer_UntrustedSentinelForgery pins the terminal rejection
// of a user-forged sentinel literal in the raw pod template.
func TestDeploymentForMCPServer_UntrustedSentinelForgery(t *testing.T) {
	t.Parallel()

	mcpServer := v1beta1test.NewMCPServer("untrusted-forgery", "default",
		withUntrustedSpec(),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.PodTemplateSpec = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"containers":[{"name":"mcp","env":[{"name":"FORGED","value":"thv-untrusted-sentinel:attacker"}]}]}}`),
			}
			m.Spec.EgressPolicy = &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{{
				Provider:     "github",
				AllowedHosts: []string{"api.github.com"},
			}}}
		}),
	)

	r, _ := setupUntrustedReconciler(t, mcpServer)
	ctx := log.IntoContext(t.Context(), log.Log)

	deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.Error(t, err)
	assert.Nil(t, deployment)
	assert.Contains(t, err.Error(), `env var "FORGED" on container "mcp" uses the reserved sentinel prefix`)

	var specErr *SpecValidationError
	assert.ErrorAs(t, err, &specErr, "sentinel forgery must be a terminal typed error")
}

// TestDeploymentForMCPServer_TrustedEgressPolicyInjectsNothing pins that sentinel
// injection is gated on spec.untrusted: a trusted workload declaring an
// egressPolicy gets no sentinel env.
func TestDeploymentForMCPServer_TrustedEgressPolicyInjectsNothing(t *testing.T) {
	t.Parallel()

	mcpServer := v1beta1test.NewMCPServer("trusted-egress-policy", "default",
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.EgressPolicy = &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{{
				Provider:          "github",
				AllowedHosts:      []string{"api.github.com"},
				CredentialEnvName: "GITHUB_TOKEN",
			}}}
		}),
	)

	r, _ := setupUntrustedReconciler(t, mcpServer)
	ctx := log.IntoContext(t.Context(), log.Log)

	deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment)

	patch := podPatchFromDeployment(t, deployment)
	for _, c := range patch.Spec.Containers {
		for _, env := range c.Env {
			assert.NotContains(t, env.Value, "thv-untrusted-sentinel:",
				"trusted workloads must never receive sentinel env")
		}
	}
}

// podPatchFromDeployment extracts and unmarshals the --k8s-pod-patch argument
// from the proxy container args of a built Deployment.
func podPatchFromDeployment(t *testing.T, deployment *appsv1.Deployment) *corev1.PodTemplateSpec {
	t.Helper()

	var patchJSON string
	for _, arg := range deployment.Spec.Template.Spec.Containers[0].Args {
		if after, ok := strings.CutPrefix(arg, "--k8s-pod-patch="); ok {
			patchJSON = after
			break
		}
	}
	require.NotEmpty(t, patchJSON, "Deployment should carry a --k8s-pod-patch argument")

	patch := &corev1.PodTemplateSpec{}
	require.NoError(t, json.Unmarshal([]byte(patchJSON), patch))
	return patch
}

// TestMCPServerReconciler_UntrustedGroupRefNotVMCPFronted pins the untrusted
// groupRef status check (ADR-0001 D4): an untrusted workload whose MCPGroup
// has no fronting VirtualMCPServer gets GroupRefValidated=False with the
// dedicated reason; the same workload reports valid once a vMCP fronts the
// group. Trusted workloads in the same un-fronted group are unaffected.
func TestMCPServerReconciler_UntrustedGroupRefNotVMCPFronted(t *testing.T) {
	t.Parallel()

	readyGroup := func(name string) *mcpv1beta1.MCPGroup {
		return &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Status:     mcpv1beta1.MCPGroupStatus{Phase: mcpv1beta1.MCPGroupPhaseReady},
		}
	}
	untrustedInGroup := func(name, group string) *mcpv1beta1.MCPServer {
		return v1beta1test.NewMCPServer(name, "default",
			withUntrustedCompliantPolicy(),
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
				m.Spec.GroupRef = &mcpv1beta1.MCPGroupRef{Name: group}
			}),
		)
	}

	t.Run("untrusted workload in an un-fronted group gets the dedicated condition", func(t *testing.T) {
		t.Parallel()
		group := readyGroup("lonely-group")
		mcpServer := untrustedInGroup("untrusted-no-front", "lonely-group")
		r, _ := setupUntrustedReconciler(t, group, mcpServer)

		r.validateGroupRef(t.Context(), mcpServer)

		updated := &mcpv1beta1.MCPServer{}
		require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))
		condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionGroupRefValidated)
		require.NotNil(t, condition)
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, mcpv1beta1.ConditionReasonGroupRefNotVMCPFronted, condition.Reason)
		assert.Equal(t, updated.Generation, condition.ObservedGeneration)
		assert.Contains(t, condition.Message, "not fronted by a VirtualMCPServer")
	})

	t.Run("untrusted workload in a vMCP-fronted group validates", func(t *testing.T) {
		t.Parallel()
		group := readyGroup("fronted-group")
		vmcp := v1beta1test.NewVirtualMCPServer("front", "default", v1beta1test.WithVMCPGroupRef("fronted-group"))
		mcpServer := untrustedInGroup("untrusted-fronted", "fronted-group")
		r, _ := setupUntrustedReconciler(t, group, vmcp, mcpServer)

		r.validateGroupRef(t.Context(), mcpServer)

		updated := &mcpv1beta1.MCPServer{}
		require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))
		condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionGroupRefValidated)
		require.NotNil(t, condition)
		assert.Equal(t, metav1.ConditionTrue, condition.Status)
		assert.Equal(t, mcpv1beta1.ConditionReasonGroupRefValidated, condition.Reason)
	})

	t.Run("trusted workload in an un-fronted group is unaffected", func(t *testing.T) {
		t.Parallel()
		group := readyGroup("trusted-group")
		mcpServer := v1beta1test.NewMCPServer("trusted-no-front", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
				m.Spec.GroupRef = &mcpv1beta1.MCPGroupRef{Name: "trusted-group"}
			}),
		)
		r, _ := setupUntrustedReconciler(t, group, mcpServer)

		r.validateGroupRef(t.Context(), mcpServer)

		updated := &mcpv1beta1.MCPServer{}
		require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))
		condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionGroupRefValidated)
		require.NotNil(t, condition)
		assert.Equal(t, metav1.ConditionTrue, condition.Status,
			"the vMCP-front requirement applies to untrusted workloads only")
	})
}
