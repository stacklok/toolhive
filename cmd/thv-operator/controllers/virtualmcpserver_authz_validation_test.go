// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	statusmocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus/mocks"
)

// validAuthzPayload is a minimal Cedar v1 payload used in passing fixtures.
const validAuthzPayload = `{
  "version": "1.0",
  "type":    "cedarv1",
  "cedar": {
    "policies": ["permit(principal, action, resource);"],
    "entities_json": "[]"
  }
}`

// vmcpWithAuthzConfigMap returns a minimal VirtualMCPServer that references the named
// authz ConfigMap. Other fields are omitted; tests only exercise the authz CM validation
// path so most spec fields are not relevant.
func vmcpWithAuthzConfigMap(cmName string) *mcpv1beta1.VirtualMCPServer {
	return v1beta1test.NewVirtualMCPServer("vmcp", "default",
		v1beta1test.WithVMCPGroupRef("g"),
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type: "anonymous",
			AuthzConfig: &mcpv1beta1.AuthzConfigRef{
				Type:      mcpv1beta1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{Name: cmName, Key: "authz.json"},
			},
		}),
	)
}

// reconcilerWithObjects wires a VirtualMCPServerReconciler with a fake client containing
// the supplied objects. The reconciler runs the namespaced-scope branch in tests.
func reconcilerWithObjects(t *testing.T, objects ...client.Object) *VirtualMCPServerReconciler {
	t.Helper()
	scheme := testutil.NewScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &VirtualMCPServerReconciler{Client: c, Scheme: scheme}
}

func TestValidateAuthzConfigMapRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		vmcp             *mcpv1beta1.VirtualMCPServer
		seedConfigMap    *corev1.ConfigMap
		expectNoError    bool
		expectNotFound   bool
		expectErrMessage string
	}{
		{
			name:          "nil authzConfig is a no-op",
			vmcp:          v1beta1test.NewVirtualMCPServer("v", "default"),
			expectNoError: true,
		},
		{
			name: "inline authzConfig is a no-op",
			vmcp: v1beta1test.NewVirtualMCPServer("v", "default",
				v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
					AuthzConfig: &mcpv1beta1.AuthzConfigRef{
						Type:   mcpv1beta1.AuthzConfigTypeInline,
						Inline: &mcpv1beta1.InlineAuthzConfig{Policies: []string{"permit(principal, action, resource);"}},
					},
				}),
			),
			expectNoError: true,
		},
		{
			name: "configMap reference resolves",
			vmcp: vmcpWithAuthzConfigMap("authz-cm"),
			seedConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "authz-cm", Namespace: "default"},
				Data:       map[string]string{"authz.json": validAuthzPayload},
			},
			expectNoError: true,
		},
		{
			name:           "missing configMap surfaces IsNotFound",
			vmcp:           vmcpWithAuthzConfigMap("authz-cm"),
			expectNotFound: true,
		},
		{
			name: "malformed payload surfaces a non-NotFound error",
			vmcp: vmcpWithAuthzConfigMap("authz-cm"),
			seedConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "authz-cm", Namespace: "default"},
				Data:       map[string]string{"authz.json": "{ not valid"},
			},
			expectErrMessage: "failed to parse authz config",
		},
		{
			// Pre-validator and converter must agree on "valid". A payload
			// that parses as authz.Config and passes the registered-authorizer
			// validation but isn't Cedar-flavoured (e.g. the HTTP authorizer
			// registered alongside Cedar) must still fail here on the vMCP
			// path, or it would pass pre-validation and then fail opaquely at
			// convert time with a different error.
			name: "valid authz.Config but non-Cedar type is rejected",
			vmcp: vmcpWithAuthzConfigMap("authz-cm"),
			seedConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "authz-cm", Namespace: "default"},
				Data: map[string]string{
					"authz.json": `{"version":"1.0","type":"httpv1","pdp":{"http":{"url":"http://localhost:9000"},"claim_mapping":"standard"}}`,
				},
			},
			expectErrMessage: "is not a Cedar config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var objs []client.Object
			if tt.seedConfigMap != nil {
				objs = append(objs, tt.seedConfigMap)
			}
			r := reconcilerWithObjects(t, objs...)
			err := r.validateAuthzConfigMapRef(t.Context(), tt.vmcp)
			switch {
			case tt.expectNoError:
				require.NoError(t, err)
			case tt.expectNotFound:
				require.Error(t, err)
				assert.True(t, errors.IsNotFound(err),
					"expected IsNotFound, got %T: %v", err, err)
			case tt.expectErrMessage != "":
				require.Error(t, err)
				assert.False(t, errors.IsNotFound(err), "expected non-NotFound error")
				assert.Contains(t, err.Error(), tt.expectErrMessage)
			}
		})
	}
}

// TestEnsureAuthSecretsValid_AuthzConfigMapNotFound verifies that a missing authz
// ConfigMap produces the ConditionReasonAuthzConfigMapNotFound reason on the
// AuthConfigured condition, surfacing the diagnostic to the user as a status
// condition rather than only as a converter error later in the reconcile.
func TestEnsureAuthSecretsValid_AuthzConfigMapNotFound(t *testing.T) {
	t.Parallel()

	r := reconcilerWithObjects(t)
	vmcp := vmcpWithAuthzConfigMap("missing-cm")

	ctrl := gomock.NewController(t)
	mockMgr := statusmocks.NewMockStatusManager(ctrl)
	mockMgr.EXPECT().
		SetAuthConfiguredCondition(
			mcpv1beta1.ConditionReasonAuthzConfigMapNotFound,
			gomock.Any(),
			metav1.ConditionFalse,
		).Times(1)
	mockMgr.EXPECT().SetObservedGeneration(gomock.Any()).Times(1)

	err := r.ensureAuthSecretsValid(t.Context(), vmcp, mockMgr)
	require.Error(t, err)
	assert.True(t, errors.IsNotFound(err))
}

// TestEnsureAuthSecretsValid_AuthzConfigMapInvalid verifies that a malformed (but
// existing) authz ConfigMap surfaces ConditionReasonAuthzConfigMapInvalid on the
// AuthConfigured condition.
func TestEnsureAuthSecretsValid_AuthzConfigMapInvalid(t *testing.T) {
	t.Parallel()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "authz-cm", Namespace: "default"},
		Data:       map[string]string{"authz.json": "{ not valid"},
	}
	r := reconcilerWithObjects(t, cm)
	vmcp := vmcpWithAuthzConfigMap("authz-cm")

	ctrl := gomock.NewController(t)
	mockMgr := statusmocks.NewMockStatusManager(ctrl)
	mockMgr.EXPECT().
		SetAuthConfiguredCondition(
			mcpv1beta1.ConditionReasonAuthzConfigMapInvalid,
			gomock.Any(),
			metav1.ConditionFalse,
		).Times(1)
	mockMgr.EXPECT().SetObservedGeneration(gomock.Any()).Times(1)

	err := r.ensureAuthSecretsValid(t.Context(), vmcp, mockMgr)
	require.Error(t, err)
	assert.False(t, errors.IsNotFound(err))
}

// TestEnsureAuthSecretsValid_AuthzConfigMapResolves verifies the happy path emits
// the AuthValid condition when the authz ConfigMap is resolvable.
func TestEnsureAuthSecretsValid_AuthzConfigMapResolves(t *testing.T) {
	t.Parallel()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "authz-cm", Namespace: "default"},
		Data:       map[string]string{"authz.json": validAuthzPayload},
	}
	r := reconcilerWithObjects(t, cm)
	vmcp := vmcpWithAuthzConfigMap("authz-cm")

	ctrl := gomock.NewController(t)
	mockMgr := statusmocks.NewMockStatusManager(ctrl)
	mockMgr.EXPECT().
		SetAuthConfiguredCondition(
			mcpv1beta1.ConditionReasonAuthValid,
			"Authentication configuration is valid",
			metav1.ConditionTrue,
		).Times(1)
	mockMgr.EXPECT().SetObservedGeneration(gomock.Any()).Times(1)

	require.NoError(t, r.ensureAuthSecretsValid(t.Context(), vmcp, mockMgr))
}
