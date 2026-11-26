// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/externalauthsupport"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// Arbitrary source Valid=False reason and message used by the Status Condition
// Parity mirror tests. The mirror copies whatever reason the referenced
// MCPExternalAuthConfig carries, so the tests use a value no controller sets
// itself (unlike InvalidConfig, which ownedByLocalValidation treats specially).
const (
	testSourceInvalidReason  = "SourceRejected"
	testSourceInvalidMessage = "the referenced MCPExternalAuthConfig was rejected"
)

// TestIsLocalValidationFailure pins which ExternalAuthConfigValidated
// conditions the mirror and the same-hash guards treat as locally owned.
func TestIsLocalValidationFailure(t *testing.T) {
	t.Parallel()

	falseCond := func(reason, message string) *metav1.Condition {
		return &metav1.Condition{Status: metav1.ConditionFalse, Reason: reason, Message: message}
	}

	tests := []struct {
		name string
		cond *metav1.Condition
		want bool
	}{
		{name: "nil", cond: nil, want: false},
		{
			name: "local InvalidConfig",
			cond: falseCond(mcpv1beta1.ConditionReasonInvalidConfig, "obo external auth requires incoming authentication"),
			want: true,
		},
		{
			name: "local InvalidCABundle",
			cond: falseCond(mcpv1beta1.ConditionReasonInvalidCABundle, "invalid CA bundle: missing key"),
			want: true,
		},
		{
			name: "mirrored InvalidConfig",
			cond: falseCond(mcpv1beta1.ConditionReasonInvalidConfig,
				mirroredConditionMessage("obo-config", "obo: clientId is required")),
			want: false,
		},
		{
			name: "mirrored InvalidCABundle",
			cond: falseCond(mcpv1beta1.ConditionReasonInvalidCABundle, mirroredConditionMessage("auth", "")),
			want: false,
		},
		{
			name: "local MultiUpstreamNotSupported",
			cond: falseCond(mcpv1beta1.ConditionReasonExternalAuthConfigMultiUpstream,
				"MCPServer supports only one upstream provider (found 2); use VirtualMCPServer for multi-upstream"),
			want: true,
		},
		{
			name: "mirrored MultiUpstreamNotSupported",
			cond: falseCond(mcpv1beta1.ConditionReasonExternalAuthConfigMultiUpstream, mirroredConditionMessage("auth", "")),
			want: false,
		},
		{name: "other reason", cond: falseCond(testSourceInvalidReason, testSourceInvalidMessage), want: false},
		{
			name: "True condition",
			cond: &metav1.Condition{Status: metav1.ConditionTrue, Reason: mcpv1beta1.ConditionReasonInvalidConfig},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isLocalValidationFailure(tt.cond))
		})
	}
}

// TestExternalAuthMirror_OBOSourceHealsToSameSpec drives a real obo
// MCPExternalAuthConfig through the MCPExternalAuthConfig reconciler from valid
// to malformed (Valid=False / InvalidConfig, the same reason the consumers use
// for their own validation failures) and back to the exact same valid spec, so
// the source's ConfigHash and the consumer's stored hash and generation never
// change. The consumer must end on ExternalAuthConfigValidated=True: the
// mirrored InvalidConfig must not be mistaken for a locally-owned failure and
// held by the same-hash guard.
func TestExternalAuthMirror_OBOSourceHealsToSameSpec(t *testing.T) {
	t.Parallel()

	const (
		namespace = "default"
		authName  = "obo-config"
	)

	tests := []struct {
		name string
		// newConsumer returns the consumer object referencing authName.
		newConsumer func() client.Object
		// handle runs the consumer's handleExternalAuthConfig against obj.
		handle func(ctx context.Context, c client.Client, obj client.Object) error
		// condition returns the consumer's ExternalAuthConfigValidated condition.
		condition  func(obj client.Object) *metav1.Condition
		wantReason string
	}{
		{
			name: "MCPServer",
			newConsumer: func() client.Object {
				return v1beta1test.NewMCPServer("server", namespace,
					v1beta1test.WithImage("test-image"),
					v1beta1test.WithExternalAuthConfigRef(authName),
					v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
						m.Generation = 4
						m.Spec.OIDCConfigRef = &mcpv1beta1.MCPOIDCConfigReference{Name: "oidc"}
					}),
				)
			},
			handle: func(ctx context.Context, c client.Client, obj client.Object) error {
				r := newTestMCPServerReconciler(c, c.Scheme(), kubernetes.PlatformKubernetes)
				return r.handleExternalAuthConfig(ctx, obj.(*mcpv1beta1.MCPServer))
			},
			condition: func(obj client.Object) *metav1.Condition {
				return meta.FindStatusCondition(obj.(*mcpv1beta1.MCPServer).Status.Conditions,
					mcpv1beta1.ConditionTypeExternalAuthConfigValidated)
			},
			wantReason: mcpv1beta1.ConditionReasonExternalAuthConfigValid,
		},
		{
			name: "MCPRemoteProxy",
			newConsumer: func() client.Object {
				return v1beta1test.NewMCPRemoteProxy("proxy", namespace,
					v1beta1test.WithRemoteProxyExternalAuthConfigRef(authName),
					v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
						p.Generation = 4
						p.Spec.OIDCConfigRef = &mcpv1beta1.MCPOIDCConfigReference{Name: "oidc"}
					}),
				)
			},
			handle: func(ctx context.Context, c client.Client, obj client.Object) error {
				r := &MCPRemoteProxyReconciler{Client: c, Scheme: c.Scheme()}
				return r.handleExternalAuthConfig(ctx, obj.(*mcpv1beta1.MCPRemoteProxy))
			},
			condition: func(obj client.Object) *metav1.Condition {
				return meta.FindStatusCondition(obj.(*mcpv1beta1.MCPRemoteProxy).Status.Conditions,
					mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated)
			},
			wantReason: mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			source := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       authName,
					Namespace:  namespace,
					Finalizers: []string{ExternalAuthConfigFinalizerName},
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeOBO,
					OBO:  validOBOSpec(),
				},
			}
			consumer := tt.newConsumer()

			scheme := testutil.NewScheme(t)
			c := withExternalAuthConfigRefIndexes(fake.NewClientBuilder().WithScheme(scheme)).
				WithObjects(source, consumer).
				WithStatusSubresource(
					&mcpv1beta1.MCPExternalAuthConfig{}, &mcpv1beta1.MCPServer{}, &mcpv1beta1.MCPRemoteProxy{}).
				Build()
			sourceReconciler := &MCPExternalAuthConfigReconciler{Client: c, Scheme: scheme}
			sourceKey := client.ObjectKeyFromObject(source)

			// setSourceSpec writes the obo spec and reconciles the source.
			setSourceSpec := func(spec *mcpv1beta1.OBOConfig) {
				t.Helper()
				live := &mcpv1beta1.MCPExternalAuthConfig{}
				require.NoError(t, c.Get(ctx, sourceKey, live))
				live.Spec.OBO = spec
				require.NoError(t, c.Update(ctx, live))
				_, err := sourceReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: sourceKey})
				require.NoError(t, err)
			}
			sourceHash := func() string {
				t.Helper()
				live := &mcpv1beta1.MCPExternalAuthConfig{}
				require.NoError(t, c.Get(ctx, sourceKey, live))
				return live.Status.ConfigHash
			}

			// 1. Valid source: the consumer records True and the source hash.
			setSourceSpec(validOBOSpec())
			validHash := sourceHash()
			require.NotEmpty(t, validHash)
			require.NoError(t, tt.handle(ctx, c, consumer))
			require.Equal(t, metav1.ConditionTrue, tt.condition(consumer).Status)

			// 2. Malformed source: Valid=False / InvalidConfig, hash unchanged.
			malformed := validOBOSpec()
			malformed.ClientID = ""
			setSourceSpec(malformed)
			require.Equal(t, validHash, sourceHash(), "a rejected spec must not advance the source hash")
			require.Error(t, tt.handle(ctx, c, consumer))
			mirrored := tt.condition(consumer)
			require.NotNil(t, mirrored)
			require.Equal(t, metav1.ConditionFalse, mirrored.Status)
			require.Equal(t, mcpv1beta1.ConditionReasonInvalidConfig, mirrored.Reason)

			// 3. Revert to the exact last-valid spec: same hash, same consumer
			// generation. The stale mirror must clear and True must be written.
			setSourceSpec(validOBOSpec())
			require.Equal(t, validHash, sourceHash())
			require.NoError(t, tt.handle(ctx, c, consumer))
			healed := tt.condition(consumer)
			require.NotNil(t, healed)
			assert.Equal(t, metav1.ConditionTrue, healed.Status,
				"a healed source must not leave a mirrored InvalidConfig on the consumer")
			assert.Equal(t, tt.wantReason, healed.Reason)
		})
	}
}

// TestExternalAuthMirror_OBOIncomingAuthRejectionClearsOnRevert covers the
// consumer's own obo incoming-auth rejection rather than a mirrored one. A
// consumer without oidcConfigRef references a valid non-obo config and stores
// its hash. The source switches to obo, so the consumer records InvalidConfig
// while its stored hash stays at the old value. The source then reverts to the
// original spec and hash: the consumer must end on True, because the rejection
// no longer applies, even though its generation and stored hash are unchanged.
func TestExternalAuthMirror_OBOIncomingAuthRejectionClearsOnRevert(t *testing.T) {
	t.Parallel()

	const (
		namespace = "default"
		authName  = "auth-config"
	)

	tests := []struct {
		name        string
		newConsumer func() client.Object
		handle      func(ctx context.Context, c client.Client, obj client.Object) error
		condition   func(obj client.Object) *metav1.Condition
		wantReason  string
	}{
		{
			name: "MCPServer",
			newConsumer: func() client.Object {
				return v1beta1test.NewMCPServer("server", namespace,
					v1beta1test.WithImage("test-image"),
					v1beta1test.WithExternalAuthConfigRef(authName),
					v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Generation = 4 }),
				)
			},
			handle: func(ctx context.Context, c client.Client, obj client.Object) error {
				r := newTestMCPServerReconciler(c, c.Scheme(), kubernetes.PlatformKubernetes)
				return r.handleExternalAuthConfig(ctx, obj.(*mcpv1beta1.MCPServer))
			},
			condition: func(obj client.Object) *metav1.Condition {
				return meta.FindStatusCondition(obj.(*mcpv1beta1.MCPServer).Status.Conditions,
					mcpv1beta1.ConditionTypeExternalAuthConfigValidated)
			},
			wantReason: mcpv1beta1.ConditionReasonExternalAuthConfigValid,
		},
		{
			name: "MCPRemoteProxy",
			newConsumer: func() client.Object {
				return v1beta1test.NewMCPRemoteProxy("proxy", namespace,
					v1beta1test.WithRemoteProxyExternalAuthConfigRef(authName),
					v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
						p.Generation = 4
						p.Spec.OIDCConfigRef = nil
					}),
				)
			},
			handle: func(ctx context.Context, c client.Client, obj client.Object) error {
				r := &MCPRemoteProxyReconciler{Client: c, Scheme: c.Scheme()}
				return r.handleExternalAuthConfig(ctx, obj.(*mcpv1beta1.MCPRemoteProxy))
			},
			condition: func(obj client.Object) *metav1.Condition {
				return meta.FindStatusCondition(obj.(*mcpv1beta1.MCPRemoteProxy).Status.Conditions,
					mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated)
			},
			wantReason: mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigValid,
		},
	}

	unauthenticated := mcpv1beta1.MCPExternalAuthConfigSpec{Type: mcpv1beta1.ExternalAuthTypeUnauthenticated}
	oboSpec := mcpv1beta1.MCPExternalAuthConfigSpec{Type: mcpv1beta1.ExternalAuthTypeOBO, OBO: validOBOSpec()}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			source := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       authName,
					Namespace:  namespace,
					Finalizers: []string{ExternalAuthConfigFinalizerName},
				},
				Spec: unauthenticated,
			}
			consumer := tt.newConsumer()

			scheme := testutil.NewScheme(t)
			c := withExternalAuthConfigRefIndexes(fake.NewClientBuilder().WithScheme(scheme)).
				WithObjects(source, consumer).
				WithStatusSubresource(
					&mcpv1beta1.MCPExternalAuthConfig{}, &mcpv1beta1.MCPServer{}, &mcpv1beta1.MCPRemoteProxy{}).
				Build()
			sourceReconciler := &MCPExternalAuthConfigReconciler{Client: c, Scheme: scheme}
			sourceKey := client.ObjectKeyFromObject(source)

			// setSourceSpec writes spec, reconciles the source and returns its hash.
			setSourceSpec := func(spec mcpv1beta1.MCPExternalAuthConfigSpec) string {
				t.Helper()
				live := &mcpv1beta1.MCPExternalAuthConfig{}
				require.NoError(t, c.Get(ctx, sourceKey, live))
				live.Spec = spec
				require.NoError(t, c.Update(ctx, live))
				_, err := sourceReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: sourceKey})
				require.NoError(t, err)
				require.NoError(t, c.Get(ctx, sourceKey, live))
				return live.Status.ConfigHash
			}

			// 1. Non-obo source: True, and the consumer stores its hash.
			originalHash := setSourceSpec(unauthenticated)
			require.NotEmpty(t, originalHash)
			require.NoError(t, tt.handle(ctx, c, consumer))
			require.Equal(t, metav1.ConditionTrue, tt.condition(consumer).Status)

			// 2. obo source: the consumer has no incoming OIDC and rejects it.
			require.NotEqual(t, originalHash, setSourceSpec(oboSpec))
			require.Error(t, tt.handle(ctx, c, consumer))
			rejected := tt.condition(consumer)
			require.NotNil(t, rejected)
			require.Equal(t, metav1.ConditionFalse, rejected.Status)
			require.Equal(t, ctrlutil.MsgOBORequiresIncomingAuth, rejected.Message)

			// 3. Revert to the original spec: same hash as the consumer stored.
			require.Equal(t, originalHash, setSourceSpec(unauthenticated))
			require.NoError(t, tt.handle(ctx, c, consumer))
			healed := tt.condition(consumer)
			require.NotNil(t, healed)
			assert.Equal(t, metav1.ConditionTrue, healed.Status,
				"the obo incoming-auth rejection must clear once the source is no longer obo")
			assert.Equal(t, tt.wantReason, healed.Reason)
		})
	}
}

// TestMirroredMessagePrefix_NotUsedByLocalMessages pins the invariant
// isLocalValidationFailure relies on: no locally written InvalidConfig or
// InvalidCABundle message starts with mirroredMessagePrefix, or the condition
// would be mistaken for a mirrored one and cleared by the mirror on every
// reconcile. The embedded-auth RunConfig build failure carries a dynamic error
// message and is not listed; its errors come from RunConfig validation and do
// not name a referenced MCPExternalAuthConfig.
func TestMirroredMessagePrefix_NotUsedByLocalMessages(t *testing.T) {
	t.Parallel()

	localConditions := map[string]struct{ reason, message string }{
		"obo incoming auth": {mcpv1beta1.ConditionReasonInvalidConfig, ctrlutil.MsgOBORequiresIncomingAuth},
		"CA bundle": {
			mcpv1beta1.ConditionReasonInvalidCABundle,
			ctrlutil.InvalidCABundleMessage(errors.New("missing key")),
		},
	}
	for name, c := range localConditions {
		assert.False(t, strings.HasPrefix(c.message, mirroredMessagePrefix),
			"%s message %q must not start with the mirrored prefix", name, c.message)
		assert.True(t, isLocalValidationFailure(&metav1.Condition{
			Status: metav1.ConditionFalse, Reason: c.reason, Message: c.message,
		}), "%s message must be treated as locally owned", name)
	}
}

// TestExternalAuthValidCondition pins what a backend resource reports on a True
// ExternalAuthConfigValidated condition. A type the backend accepts without
// applying it carries its own reason and a message that names the type, so a
// backend used on its own does not read as protected by it. Every other type
// keeps the consumer's plain "valid" reason and message.
func TestExternalAuthValidCondition(t *testing.T) {
	t.Parallel()

	consumers := map[externalauthsupport.Consumer]string{
		externalauthsupport.ConsumerMCPServer:      mcpv1beta1.ConditionReasonExternalAuthConfigValid,
		externalauthsupport.ConsumerMCPRemoteProxy: mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigValid,
	}
	notApplied := []mcpv1beta1.ExternalAuthType{
		mcpv1beta1.ExternalAuthTypeHeaderInjection,
		mcpv1beta1.ExternalAuthTypeUpstreamInject,
		mcpv1beta1.ExternalAuthTypeXAA,
	}
	applied := []mcpv1beta1.ExternalAuthType{
		mcpv1beta1.ExternalAuthTypeTokenExchange,
		mcpv1beta1.ExternalAuthTypeUnauthenticated,
		mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
		mcpv1beta1.ExternalAuthTypeOBO,
	}

	for consumer, validReason := range consumers {
		for _, authType := range notApplied {
			reason, message := externalAuthValidCondition(consumer, validReason, "hdr", authType)
			assert.Equal(t, mcpv1beta1.ConditionReasonAppliedByVirtualMCPServer, reason, "%s/%s", consumer, authType)
			assert.Equal(t, fmt.Sprintf(
				"MCPExternalAuthConfig 'hdr' is valid, but %s does not apply type %q itself: "+
					"it takes effect only when a VirtualMCPServer discovers this backend",
				consumer, authType), message)
		}
		for _, authType := range applied {
			reason, message := externalAuthValidCondition(consumer, validReason, "hdr", authType)
			assert.Equal(t, validReason, reason, "%s/%s", consumer, authType)
			assert.Equal(t, "MCPExternalAuthConfig 'hdr' is valid", message)
		}
	}
}
