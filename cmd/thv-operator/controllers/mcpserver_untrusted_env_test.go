// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"net"
	"strings"
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
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
)

// withUntrustedSpec marks the MCPServer fixture as untrusted via the
// Wave-1 spec.untrusted field and enables untrusted mode for the test
// process (TOOLHIVE_ENABLE_UNTRUSTED_MODE) — the mode is opt-in and
// isUntrusted requires both, so untrusted fixtures must also flip the flag.
// t.Setenv auto-restores, but it serializes the calling test (no t.Parallel).
func withUntrustedSpec(t *testing.T) v1beta1test.MCPServerOption {
	t.Helper()
	t.Setenv(untrusted.EnvEnableUntrustedMode, "true")
	return func(m *mcpv1beta1.MCPServer) {
		m.Spec.Untrusted = true
	}
}

// withUntrustedCompliantPolicy adds a minimal valid EgressPolicy (hostname
// destination; reconcile-path tests stub untrustedDNSLookup so no real DNS
// resolution happens) and enables untrusted mode for the test. Wave 3:
// untrusted servers without an EgressPolicy terminate at the egress-resource
// gate, so fixtures that must proceed down the normal reconcile path need
// this.
func withUntrustedCompliantPolicy(t *testing.T) v1beta1test.MCPServerOption {
	t.Helper()
	t.Setenv(untrusted.EnvEnableUntrustedMode, "true")
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

// TestIsUntrusted pins the isUntrusted gate: spec.untrusted AND the
// TOOLHIVE_ENABLE_UNTRUSTED_MODE env flag must both be on — the mode is
// opt-in, so the spec field alone is inert while the operator runs with the
// mode disabled. The interim Wave-0 annotation is inert either way.
// t.Setenv serializes the subtests (no t.Parallel).
//
//nolint:paralleltest // t.Setenv modifies the process environment.
func TestIsUntrusted(t *testing.T) {
	t.Run("mode enabled", func(t *testing.T) {
		t.Setenv(untrusted.EnvEnableUntrustedMode, "true")

		assert.False(t, isUntrusted(v1beta1test.NewMCPServer("plain", "default")))
		assert.True(t, isUntrusted(v1beta1test.NewMCPServer("flagged", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Spec.Untrusted = true }))))

		annotated := v1beta1test.NewMCPServer("annotated", "default", v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Annotations = map[string]string{"toolhive.stacklok.dev/untrusted": "true"}
		}))
		assert.False(t, isUntrusted(annotated), "the interim Wave-0 annotation must have no effect")
	})

	t.Run("mode disabled treats spec.untrusted=true as trusted", func(t *testing.T) {
		t.Setenv(untrusted.EnvEnableUntrustedMode, "false")
		assert.False(t, isUntrusted(v1beta1test.NewMCPServer("flagged", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Spec.Untrusted = true }))))
	})

	t.Run("mode unset defaults to disabled", func(t *testing.T) {
		// No t.Setenv: the absent-env default is OFF.
		assert.False(t, untrusted.ModeEnabled(), "fixture precondition: the env var must be unset")
		assert.False(t, isUntrusted(v1beta1test.NewMCPServer("flagged", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Spec.Untrusted = true }))))
	})
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
		// The fake client reads its own writes, so it serves as the APIReader
		// (read-your-write) the untrusted CA path needs.
		APIReader: fakeClient,
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
	// installEgressDNSStubOnce guarantees one lock+install per test no matter
	// how many reconcileOnce calls follow; a parallel sibling stubber blocks
	// on the mutex until t.Cleanup releases it.
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

// stubUntrustedDNSOnce installs the fixture DNS stub (every host resolves to
// the shared fixture IP) exactly once per test via the shared once-per-test
// install. Reconciles after the first reuse the already-installed stub; a
// parallel sibling blocks on the mutex until t.Cleanup releases it.
func stubUntrustedDNSOnce(t *testing.T) {
	t.Helper()
	installEgressDNSStubOnce(t, func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("140.82.114.26")}, nil
	})
}

//nolint:paralleltest // withUntrustedSpec calls t.Setenv (serializes the test).
func TestMCPServerReconciler_UntrustedSecretEnvRejected(t *testing.T) {
	mcpServer := v1beta1test.NewMCPServer("untrusted-secrets", "default",
		withUntrustedSpec(t),
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

//nolint:paralleltest // withUntrustedSpec calls t.Setenv (serializes the test).
func TestMCPServerReconciler_UntrustedRawTemplateSecretEnvRejected(t *testing.T) {
	// #13: spec.untrusted + raw podTemplateSpec smuggling secretKeyRef onto the mcp container.
	mcpServer := v1beta1test.NewMCPServer("untrusted-raw-template", "default",
		withUntrustedSpec(t),
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

//nolint:paralleltest // withUntrustedCompliantPolicy calls t.Setenv (serializes the test).
func TestMCPServerReconciler_UntrustedCompliantDeploys(t *testing.T) {
	// #15: untrusted but compliant (literal env only) —
	// the gate passes and the reconcile proceeds normally.
	mcpServer := v1beta1test.NewMCPServer("untrusted-compliant", "default",
		withUntrustedCompliantPolicy(t),
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
//
//nolint:paralleltest // withUntrustedCompliantPolicy calls t.Setenv (serializes the test).
func TestMCPServerReconciler_UntrustedLatchClearsAndWarningReArms(t *testing.T) {
	ctx := log.IntoContext(t.Context(), log.Log)

	// Start REJECTED: untrusted + spec.secrets.
	mcpServer := v1beta1test.NewMCPServer("untrusted-latch", "default",
		withUntrustedCompliantPolicy(t),
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

//nolint:paralleltest // withUntrustedSpec calls t.Setenv (serializes the test).
func TestDeploymentForMCPServer_UntrustedSecretEnvRejectedAtBuildTime(t *testing.T) {
	// Defense-in-depth: deploymentForMCPServer itself rejects the built patch
	// for untrusted workloads (spec.secrets seam)...
	mcpServer := v1beta1test.NewMCPServer("untrusted-build-gate", "default",
		withUntrustedSpec(t),
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
//
//nolint:paralleltest // withUntrustedSpec calls t.Setenv (serializes the test).
func TestDeploymentForMCPServer_UntrustedSentinelInjection(t *testing.T) {
	mcpServer := v1beta1test.NewMCPServer("untrusted-sentinel", "default",
		withUntrustedSpec(t),
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
//
//nolint:paralleltest // withUntrustedSpec calls t.Setenv (serializes the test).
func TestDeploymentForMCPServer_UntrustedSentinelCollision(t *testing.T) {
	mcpServer := v1beta1test.NewMCPServer("untrusted-collision", "default",
		withUntrustedSpec(t),
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
//
//nolint:paralleltest // withUntrustedSpec calls t.Setenv (serializes the test).
func TestDeploymentForMCPServer_UntrustedSentinelForgery(t *testing.T) {
	mcpServer := v1beta1test.NewMCPServer("untrusted-forgery", "default",
		withUntrustedSpec(t),
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
//
//nolint:paralleltest // t.Setenv at the parent serializes the subtests' env view.
func TestMCPServerReconciler_UntrustedGroupRefNotVMCPFronted(t *testing.T) {
	// Untrusted mode must be ON for the untrusted groupRef check to arm; set it
	// once for the whole test (parent-level t.Setenv covers all subtests).
	t.Setenv(untrusted.EnvEnableUntrustedMode, "true")

	readyGroup := func(name string) *mcpv1beta1.MCPGroup {
		return &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Status:     mcpv1beta1.MCPGroupStatus{Phase: mcpv1beta1.MCPGroupPhaseReady},
		}
	}
	untrustedInGroup := func(name, group string) *mcpv1beta1.MCPServer {
		return v1beta1test.NewMCPServer(name, "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
				m.Spec.Untrusted = true
				m.Spec.EgressPolicy = &mcpv1beta1.EgressPolicy{
					Providers: []mcpv1beta1.ProviderEgress{{
						Provider:     "github",
						AllowedHosts: []string{"api.github.com"},
					}},
				}
				m.Spec.GroupRef = &mcpv1beta1.MCPGroupRef{Name: group}
			}),
		)
	}

	t.Run("untrusted workload in an un-fronted group gets the dedicated condition", func(t *testing.T) {

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

// TestMCPServerReconciler_UntrustedModeDisabledReconcilesAsTrusted pins the
// feature-flag behavior: with TOOLHIVE_ENABLE_UNTRUSTED_MODE off, a
// spec.untrusted=true workload reconciles as a normal trusted workload — the
// secretKeyRef env gate does NOT fire, no untrusted data-plane resources are
// created — and the degradation is surfaced on the UntrustedMode condition
// (False/UntrustedModeDisabled) plus a one-shot Warning event.
//
//nolint:paralleltest // t.Setenv modifies the process environment.
func TestMCPServerReconciler_UntrustedModeDisabledReconcilesAsTrusted(t *testing.T) {
	t.Setenv(untrusted.EnvEnableUntrustedMode, "false")

	mcpServer := v1beta1test.NewMCPServer("untrusted-flag-off", "default",
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.Untrusted = true
			m.Spec.EgressPolicy = &mcpv1beta1.EgressPolicy{
				Providers: []mcpv1beta1.ProviderEgress{{
					Provider:     "github",
					AllowedHosts: []string{"api.github.com"},
				}},
			}
		}),
		withSecrets(mcpv1beta1.SecretRef{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}),
	)

	r, recorder := setupUntrustedReconciler(t, mcpServer)

	// The reconcile must continue down the normal trusted path (requeue waiting
	// on the runconfig ConfigMap) — it must NOT fail the workload.
	result := reconcileOnce(t, r, mcpServer)
	assert.False(t, result.IsZero(), "flag-off untrusted workload must reconcile as trusted, not terminate")

	updated := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), updated))

	// The secretKeyRef gate must NOT have fired: the flag-off workload is
	// trusted and trusted workloads may source backend env from Secrets.
	assert.Nil(t, meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid),
		"the untrusted env gate must not arm while the mode is disabled")
	assert.NotEqual(t, mcpv1beta1.MCPServerPhaseFailed, updated.Status.Phase)

	// The degradation is surfaced on the UntrustedMode condition.
	cond := meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeUntrustedMode)
	require.NotNil(t, cond, "UntrustedMode condition must be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonUntrustedModeDisabled, cond.Reason)
	assert.Equal(t, updated.Generation, cond.ObservedGeneration)
	assert.Contains(t, cond.Message, untrusted.EnvEnableUntrustedMode)

	// ...and with a one-shot Warning event on the transition.
	require.Eventually(t, func() bool {
		return countContaining(drainEvents(recorder), mcpv1beta1.ConditionReasonUntrustedModeDisabled) == 1
	}, 2*time.Second, 10*time.Millisecond, "expected one Warning event on the disabled transition")

	// No untrusted data-plane resources may exist (the ensure call deletes them).
	assert.Empty(t, listCASecrets(t, r, mcpServer), "no bump CA Secrets may be created while the mode is disabled")

	// Idempotency: a second reconcile must not churn the condition or re-fire
	// the event.
	result = reconcileOnce(t, r, mcpServer)
	afterSecond := &mcpv1beta1.MCPServer{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(mcpServer), afterSecond))
	condAfter := meta.FindStatusCondition(afterSecond.Status.Conditions, mcpv1beta1.ConditionTypeUntrustedMode)
	require.NotNil(t, condAfter)
	assert.Equal(t, *cond, *condAfter, "UntrustedMode condition must not churn on re-observe")
	assert.False(t, result.IsZero())
	time.Sleep(50 * time.Millisecond) // let any async event emission settle
	assert.Empty(t, drainEvents(recorder), "no event expected while the mode stays disabled")
}

// TestMCPServerReconciler_UntrustedModeDisabledSentinelsNotInjected pins that
// flag-off untrusted workloads build a Deployment without sentinel env: the
// sentinel seam keys on isUntrusted, which is false while the mode is off.
//
//nolint:paralleltest // t.Setenv modifies the process environment.
func TestMCPServerReconciler_UntrustedModeDisabledSentinelsNotInjected(t *testing.T) {
	t.Setenv(untrusted.EnvEnableUntrustedMode, "false")

	mcpServer := v1beta1test.NewMCPServer("untrusted-flag-off-build", "default",
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.Untrusted = true
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
				"flag-off untrusted workloads must never receive sentinel env")
		}
	}
}

// TestMCPServerReconciler_SurfaceUntrustedModeConditionLifecycle pins the
// condition lifecycle independent of the reconcile path: enabled → True,
// disabled → False + Warning, spec.untrusted=false → cleared.
//
//nolint:paralleltest // t.Setenv modifies the process environment.
func TestMCPServerReconciler_SurfaceUntrustedModeConditionLifecycle(t *testing.T) {
	ctx := log.IntoContext(t.Context(), log.Log)

	find := func(r *MCPServerReconciler, key client.ObjectKey) *metav1.Condition {
		m := &mcpv1beta1.MCPServer{}
		require.NoError(t, r.Get(ctx, key, m))
		return meta.FindStatusCondition(m.Status.Conditions, mcpv1beta1.ConditionTypeUntrustedMode)
	}

	t.Run("enabled reports True", func(t *testing.T) {
		t.Setenv(untrusted.EnvEnableUntrustedMode, "true")
		mcpServer := v1beta1test.NewMCPServer("mode-on", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Spec.Untrusted = true }))
		r, recorder := setupUntrustedReconciler(t, mcpServer)

		r.surfaceUntrustedModeCondition(ctx, mcpServer)

		cond := find(r, client.ObjectKeyFromObject(mcpServer))
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, mcpv1beta1.ConditionReasonUntrustedModeEnabled, cond.Reason)
		time.Sleep(50 * time.Millisecond)
		assert.Empty(t, drainEvents(recorder), "no Warning when the mode is enabled")
	})

	t.Run("spec.untrusted=false clears the condition", func(t *testing.T) {
		t.Setenv(untrusted.EnvEnableUntrustedMode, "false")
		mcpServer := v1beta1test.NewMCPServer("mode-cleared", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Spec.Untrusted = true }))
		r, _ := setupUntrustedReconciler(t, mcpServer)

		r.surfaceUntrustedModeCondition(ctx, mcpServer)
		require.NotNil(t, find(r, client.ObjectKeyFromObject(mcpServer)), "precondition: condition latched")

		// Flip to trusted; the condition must be removed.
		current := &mcpv1beta1.MCPServer{}
		require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(mcpServer), current))
		current.Spec.Untrusted = false
		require.NoError(t, r.Update(ctx, current))
		r.surfaceUntrustedModeCondition(ctx, current)

		assert.Nil(t, find(r, client.ObjectKeyFromObject(mcpServer)),
			"UntrustedMode condition must clear when spec.untrusted flips to false")
	})

	t.Run("warning re-arms after the condition clears", func(t *testing.T) {
		t.Setenv(untrusted.EnvEnableUntrustedMode, "false")
		mcpServer := v1beta1test.NewMCPServer("mode-rearm", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Spec.Untrusted = true }))
		r, recorder := setupUntrustedReconciler(t, mcpServer)

		r.surfaceUntrustedModeCondition(ctx, mcpServer)
		require.Eventually(t, func() bool {
			return countContaining(drainEvents(recorder), mcpv1beta1.ConditionReasonUntrustedModeDisabled) == 1
		}, 2*time.Second, 10*time.Millisecond)

		// untrusted→trusted→untrusted: the Warning must fire again on the
		// second disabled transition.
		current := &mcpv1beta1.MCPServer{}
		require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(mcpServer), current))
		current.Spec.Untrusted = false
		require.NoError(t, r.Update(ctx, current))
		r.surfaceUntrustedModeCondition(ctx, current)

		require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(mcpServer), current))
		current.Spec.Untrusted = true
		require.NoError(t, r.Update(ctx, current))
		r.surfaceUntrustedModeCondition(ctx, current)

		require.Eventually(t, func() bool {
			return countContaining(drainEvents(recorder), mcpv1beta1.ConditionReasonUntrustedModeDisabled) == 1
		}, 2*time.Second, 10*time.Millisecond, "the Warning must fire on the second disabled transition")
	})
}
