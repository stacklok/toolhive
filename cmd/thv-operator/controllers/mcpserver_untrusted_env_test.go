// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
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

// withUntrustedAnnotation marks the MCPServer fixture as untrusted via the
// interim Wave 0 opt-in annotation.
func withUntrustedAnnotation() v1beta1test.MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		if m.Annotations == nil {
			m.Annotations = map[string]string{}
		}
		m.Annotations[untrustedAnnotationKey] = "true"
	}
}

func withSecrets(secrets ...mcpv1beta1.SecretRef) v1beta1test.MCPServerOption {
	return func(m *mcpv1beta1.MCPServer) {
		m.Spec.Secrets = secrets
	}
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

// reconcileOnce runs a single Reconcile for the given MCPServer fixture.
func reconcileOnce(t *testing.T, r *MCPServerReconciler, mcpServer *mcpv1beta1.MCPServer) ctrl.Result {
	t.Helper()

	ctx := log.IntoContext(t.Context(), log.Log)
	req := ctrl.Request{NamespacedName: types.NamespacedName{
		Name:      mcpServer.Name,
		Namespace: mcpServer.Namespace,
	}}
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	return result
}

func TestMCPServerReconciler_UntrustedSecretEnvRejected(t *testing.T) {
	t.Parallel()

	mcpServer := v1beta1test.NewMCPServer("untrusted-secrets", "default",
		withUntrustedAnnotation(),
		withSecrets(mcpv1beta1.SecretRef{Name: "backend-creds", Key: "token", TargetEnvName: "API_TOKEN"}),
	)

	r, recorder := setupUntrustedReconciler(t, mcpServer)

	// Reconcile #11: annotation + spec.secrets → terminal rejection.
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

	// #12: identical spec.secrets but no annotation — no behavior change; the
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

func TestMCPServerReconciler_UntrustedRawTemplateSecretEnvRejected(t *testing.T) {
	t.Parallel()

	// #13: annotation + raw podTemplateSpec smuggling secretKeyRef onto the mcp container.
	mcpServer := v1beta1test.NewMCPServer("untrusted-raw-template", "default",
		withUntrustedAnnotation(),
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

	// #15: annotation present but workload compliant (literal env only) —
	// the gate passes and the reconcile proceeds normally.
	mcpServer := v1beta1test.NewMCPServer("untrusted-compliant", "default",
		withUntrustedAnnotation(),
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
		withUntrustedAnnotation(),
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
		withUntrustedAnnotation(),
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
