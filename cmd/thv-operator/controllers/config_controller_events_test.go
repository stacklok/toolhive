// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
)

// drainEvents returns every event currently buffered on the fake recorder
// without blocking once the buffer is empty.
func drainEvents(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// countContaining returns how many of evts contain substr.
func countContaining(evts []string, substr string) int {
	n := 0
	for _, e := range evts {
		if strings.Contains(e, substr) {
			n++
		}
	}
	return n
}

// ---- MCPOIDCConfig ----

func TestMCPOIDCConfigReconciler_EmitsEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	// Invalid: type=inline but no inline config.
	cfg := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "oidc-events",
			Namespace:  "default",
			Finalizers: []string{OIDCConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()
	rec := events.NewFakeRecorder(10)
	r := &MCPOIDCConfigReconciler{Client: fakeClient, Scheme: scheme, Recorder: rec}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}}

	// First reconcile: validation fails -> Warning ConfigInvalid.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	evts := drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonConfigInvalid), "expected one ConfigInvalid event: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeWarning), "ConfigInvalid must be a Warning: %v", evts)

	// Second reconcile (still invalid): no repeat event (no transition).
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, drainEvents(rec), "no event expected while staying invalid")

	// Fix the spec and bump generation.
	var live mcpv1beta1.MCPOIDCConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &live))
	live.Spec.Inline = &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://accounts.google.com", ClientID: "c"}
	live.Generation = 2
	require.NoError(t, fakeClient.Update(ctx, &live))

	// Recovery reconcile: Normal ConfigValid.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	evts = drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonConfigValid), "expected one ConfigValid recovery event: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeNormal), "recovery must be Normal: %v", evts)
}

func TestMCPOIDCConfigReconciler_EmitsDeletionBlockedEvent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	cfg := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "oidc-del",
			Namespace:         "default",
			Finalizers:        []string{OIDCConfigFinalizerName},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://accounts.google.com", ClientID: "c"},
		},
	}
	server := v1beta1test.NewMCPServer("ref-server", "default",
		v1beta1test.WithImage("example/mcp:latest"),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.OIDCConfigRef = &mcpv1beta1.MCPOIDCConfigReference{Name: "oidc-del"}
		}),
	)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg, server).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()
	rec := events.NewFakeRecorder(10)
	r := &MCPOIDCConfigReconciler{Client: fakeClient, Scheme: scheme, Recorder: rec}

	res, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0))
	evts := drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonDeletionBlocked), "expected DeletionBlocked event: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeWarning), "DeletionBlocked must be a Warning: %v", evts)
}

func TestMCPOIDCConfigReconciler_NilRecorderNoPanic(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	cfg := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "oidc-nilrec",
			Namespace:  "default",
			Finalizers: []string{OIDCConfigFinalizerName},
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{Type: mcpv1beta1.MCPOIDCConfigTypeInline},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()
	r := &MCPOIDCConfigReconciler{Client: fakeClient, Scheme: scheme} // nil Recorder
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
}

// ---- MCPExternalAuthConfig ----

func validExternalAuthSpec() mcpv1beta1.MCPExternalAuthConfigSpec {
	return mcpv1beta1.MCPExternalAuthConfigSpec{
		Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
		TokenExchange: &mcpv1beta1.TokenExchangeConfig{
			TokenURL:        "https://oauth.example.com/token",
			ClientID:        "test-client",
			ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "test-secret", Key: "client-secret"},
			Audience:        "backend-service",
		},
	}
}

func TestMCPExternalAuthConfigReconciler_EmitsEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	// Invalid: type=tokenExchange but no tokenExchange config block.
	cfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "extauth-events",
			Namespace:  "default",
			Finalizers: []string{ExternalAuthConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{Type: mcpv1beta1.ExternalAuthTypeTokenExchange},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()
	rec := events.NewFakeRecorder(10)
	r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme, Recorder: rec}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	evts := drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonConfigInvalid), "expected ConfigInvalid: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeWarning), "ConfigInvalid must be a Warning: %v", evts)

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, drainEvents(rec), "no event expected while staying invalid")

	var live mcpv1beta1.MCPExternalAuthConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &live))
	live.Spec = validExternalAuthSpec()
	live.Generation = 2
	require.NoError(t, fakeClient.Update(ctx, &live))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	evts = drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonConfigValid), "expected ConfigValid recovery: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeNormal), "recovery must be Normal: %v", evts)
}

func TestMCPExternalAuthConfigReconciler_EmitsDeletionBlockedEvent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	cfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "extauth-del",
			Namespace:         "default",
			Finalizers:        []string{ExternalAuthConfigFinalizerName},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: validExternalAuthSpec(),
	}
	server := v1beta1test.NewMCPServer("ref-server", "default",
		v1beta1test.WithImage("example/mcp:latest"),
		v1beta1test.WithExternalAuthConfigRef("extauth-del"),
	)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg, server).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()
	rec := events.NewFakeRecorder(10)
	r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme, Recorder: rec}

	res, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0))
	evts := drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonDeletionBlocked), "expected DeletionBlocked: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeWarning), "DeletionBlocked must be a Warning: %v", evts)

	// AC: event messages must not leak credential-/topology-sensitive spec
	// content (token endpoint URL, secret ref). They carry only logical
	// workload identifiers.
	assert.Zero(t, countContaining(evts, "oauth.example.com"), "event must not leak the token URL: %v", evts)
	assert.Zero(t, countContaining(evts, "test-secret"), "event must not leak the secret ref: %v", evts)
	assert.Equal(t, 1, countContaining(evts, "ref-server"), "event should name the blocking workload: %v", evts)
}

// TestMCPExternalAuthConfigReconciler_EmitsRecoveryOnSteadyStatePath covers the
// recovery transition that flows through updateReferencingWorkloads rather than
// handleConfigHashChange: a config whose stored hash already matches the current
// spec (no hash change) but whose Valid condition is still False from an earlier
// failure. Reconcile must emit a single Normal ConfigValid event.
func TestMCPExternalAuthConfigReconciler_EmitsRecoveryOnSteadyStatePath(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	spec := validExternalAuthSpec()
	hash := (&MCPExternalAuthConfigReconciler{}).calculateConfigHash(spec)
	cfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "extauth-steady-recovery",
			Namespace:  "default",
			Finalizers: []string{ExternalAuthConfigFinalizerName},
			Generation: 1,
		},
		Spec: spec,
		Status: mcpv1beta1.MCPExternalAuthConfigStatus{
			ConfigHash: hash,
			Conditions: []metav1.Condition{{
				Type:    mcpv1beta1.ConditionTypeValid,
				Status:  metav1.ConditionFalse,
				Reason:  "ValidationFailed",
				Message: "stale prior failure",
			}},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()
	rec := events.NewFakeRecorder(10)
	r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme, Recorder: rec}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	evts := drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonConfigValid), "expected one ConfigValid recovery event: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeNormal), "recovery must be Normal: %v", evts)
}

// ---- MCPAuthzConfig ----

func TestMCPAuthzConfigReconciler_EmitsEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	// Invalid: cedarv1 type but empty policy set (fails backend validation).
	cfg := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "authz-events",
			Namespace:  "default",
			Finalizers: []string{AuthzConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: runtime.RawExtension{Raw: []byte(`{"policies":[],"entities_json":"[]"}`)},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPAuthzConfig{}).
		Build()
	rec := events.NewFakeRecorder(10)
	r := &MCPAuthzConfigReconciler{Client: fakeClient, Scheme: scheme, Recorder: rec}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	evts := drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonConfigInvalid), "expected ConfigInvalid: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeWarning), "ConfigInvalid must be a Warning: %v", evts)

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, drainEvents(rec), "no event expected while staying invalid")

	var live mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &live))
	live.Spec.Config = validCedarConfig()
	live.Generation = 2
	require.NoError(t, fakeClient.Update(ctx, &live))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	evts = drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonConfigValid), "expected ConfigValid recovery: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeNormal), "recovery must be Normal: %v", evts)
}

func TestMCPAuthzConfigReconciler_EmitsDeletionBlockedEvent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	cfg := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "authz-del",
			Namespace:         "default",
			Finalizers:        []string{AuthzConfigFinalizerName},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
	}
	server := v1beta1test.NewMCPServer("ref-server", "default",
		v1beta1test.WithImage("example/mcp:latest"),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: "authz-del"}
		}),
	)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg, server).
		WithStatusSubresource(&mcpv1beta1.MCPAuthzConfig{}).
		Build()
	rec := events.NewFakeRecorder(10)
	r := &MCPAuthzConfigReconciler{Client: fakeClient, Scheme: scheme, Recorder: rec}

	res, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0))
	evts := drainEvents(rec)
	assert.Equal(t, 1, countContaining(evts, eventReasonDeletionBlocked), "expected DeletionBlocked: %v", evts)
	assert.Equal(t, 1, countContaining(evts, corev1.EventTypeWarning), "DeletionBlocked must be a Warning: %v", evts)
}
