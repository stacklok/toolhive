// Regression coverage for the six config-handler failure blocks in
// validateAndHandleConfigs. Each of them used to write only
// Status.Phase = Failed, which left the message and the Ready condition from
// the last healthy reconcile in place -- so a proxy that lost a referenced
// config reported Failed and Ready=True at the same time, and
// `kubectl wait --for=condition=Ready` still saw a healthy proxy.
package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
)

// staleHealthyStatus is the status a proxy carries after a reconcile that
// succeeded: Phase Running, a message, and Ready=True. A later config-handler
// failure has to replace all of it, not just the phase.
const staleObservedGeneration = int64(99)

func staleHealthyStatus() mcpv1beta1.MCPRemoteProxyStatus {
	return mcpv1beta1.MCPRemoteProxyStatus{
		Phase:              mcpv1beta1.MCPRemoteProxyPhaseReady,
		Message:            "Remote proxy is running",
		ObservedGeneration: staleObservedGeneration,
		Conditions: []metav1.Condition{{
			Type:               mcpv1beta1.ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "DeploymentReady",
			Message:            "Deployment is ready and running",
			ObservedGeneration: staleObservedGeneration,
			LastTransitionTime: metav1.Now(),
		}},
	}
}

func TestMCPRemoteProxyConfigHandlerFailuresClearTheStaleReadyCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		option       v1beta1test.MCPRemoteProxyOption
		handlerInMsg string
		extraObjects []client.Object
	}{
		{
			name:         "MCPToolConfig",
			option:       v1beta1test.WithRemoteProxyToolConfigRef("tool"),
			handlerInMsg: "MCPToolConfig",
		},
		{
			name:         "MCPTelemetryConfig",
			option:       v1beta1test.WithRemoteProxyTelemetryConfigRef("telemetry"),
			handlerInMsg: "MCPTelemetryConfig",
		},
		{
			// A *missing* ExternalAuthConfig never reaches the handler: spec
			// validation rejects it first. An existing one carrying Valid=False
			// is what makes handleExternalAuthConfig fail.
			name:         "MCPExternalAuthConfig",
			option:       v1beta1test.WithRemoteProxyExternalAuthConfigRef("auth"),
			handlerInMsg: "MCPExternalAuthConfig",
			extraObjects: []client.Object{&mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
				Status: mcpv1beta1.MCPExternalAuthConfigStatus{Conditions: []metav1.Condition{{
					Type:    mcpv1beta1.ConditionTypeValid,
					Status:  metav1.ConditionFalse,
					Reason:  "EnterpriseRequired",
					Message: "this type requires an enterprise license",
				}}},
			}},
		},
		{
			name:         "authServerRef",
			option:       v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", "authserver"),
			handlerInMsg: "authServerRef",
		},
		{
			name:         "MCPOIDCConfig",
			option:       v1beta1test.WithRemoteProxyOIDCConfigRef("oidc", "audience"),
			handlerInMsg: "MCPOIDCConfig",
		},
		{
			name:         "MCPAuthzConfig",
			option:       v1beta1test.WithRemoteProxyAuthzConfigRef("authz"),
			handlerInMsg: "MCPAuthzConfig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The referenced config is never created, so the handler fails on
			// the not-found branch.
			proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default",
				v1beta1test.WithRemoteProxyURL("https://remote.example.com"),
				v1beta1test.WithRemoteProxyStatus(staleHealthyStatus()),
				tt.option,
			)
			objs := append([]client.Object{proxy}, tt.extraObjects...)

			reconciler, fakeClient := newTestMCPRemoteProxyReconciler(t, objs...)
			_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: client.ObjectKeyFromObject(proxy),
			})
			// The handler's own error is still returned; the status write is
			// best effort, so a failure there is logged, not propagated.
			require.Error(t, err)

			actual := &mcpv1beta1.MCPRemoteProxy{}
			require.NoError(t, fakeClient.Get(t.Context(), client.ObjectKeyFromObject(proxy), actual))

			assert.Equal(t, mcpv1beta1.MCPRemoteProxyPhaseFailed, actual.Status.Phase)
			assert.Contains(t, actual.Status.Message, tt.handlerInMsg,
				"the message has to name the handler that failed")
			assert.Equal(t, actual.Generation, actual.Status.ObservedGeneration,
				"a stale observed generation leaves the status describing an older spec")
			assert.NotEqual(t, staleObservedGeneration, actual.Status.ObservedGeneration)

			ready := meta.FindStatusCondition(actual.Status.Conditions, mcpv1beta1.ConditionTypeReady)
			require.NotNil(t, ready, "the Ready condition must be updated, not left stale")
			assert.Equal(t, metav1.ConditionFalse, ready.Status)
			assert.Equal(t, mcpv1beta1.ConditionReasonNotReady, ready.Reason)
			assert.Equal(t, actual.Status.Message, ready.Message)
		})
	}
}
