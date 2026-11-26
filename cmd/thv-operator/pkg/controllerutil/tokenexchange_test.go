// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/externalauthsupport"
	"github.com/stacklok/toolhive/pkg/runner"
)

// TestAddExternalAuthConfigOptions_EnforcesSupportMatrix drives the run-config
// dispatch with every ExternalAuthType for both consumers that route through
// it, and requires the outcome to agree with the externalauthsupport matrix:
// an unsupported type must surface as UnsupportedTypeError (never a silent
// no-op — the failure mode behind #5930), and a supported type must get past
// the support check (later errors, e.g. a missing secret, are fine but must
// not be UnsupportedTypeError). A type the backend only carries for a
// VirtualMCPServer must add nothing to the run config and must not fail.
// Because it exercises the real dispatch, this test fails when the matrix and
// the switch drift apart in either direction.
func TestAddExternalAuthConfigOptions_EnforcesSupportMatrix(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)
	const ns = "default"

	allTypes := []mcpv1beta1.ExternalAuthType{
		mcpv1beta1.ExternalAuthTypeTokenExchange,
		mcpv1beta1.ExternalAuthTypeHeaderInjection,
		mcpv1beta1.ExternalAuthTypeBearerToken,
		mcpv1beta1.ExternalAuthTypeUnauthenticated,
		mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
		mcpv1beta1.ExternalAuthTypeAWSSts,
		mcpv1beta1.ExternalAuthTypeUpstreamInject,
		mcpv1beta1.ExternalAuthTypeOBO,
		mcpv1beta1.ExternalAuthTypeXAA,
	}

	consumers := []externalauthsupport.Consumer{
		externalauthsupport.ConsumerMCPServer,
		externalauthsupport.ConsumerMCPRemoteProxy,
	}

	for _, consumer := range consumers {
		for _, authType := range allTypes {
			t.Run(fmt.Sprintf("%s/%s", consumer, authType), func(t *testing.T) {
				t.Parallel()

				cfg := &mcpv1beta1.MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: ns},
					Spec:       mcpv1beta1.MCPExternalAuthConfigSpec{Type: authType},
				}
				fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).Build()

				var options []runner.RunConfigBuilderOption
				err := AddExternalAuthConfigOptions(
					t.Context(),
					fakeClient,
					ns,
					"server-name",
					consumer,
					&mcpv1beta1.ExternalAuthConfigRef{Name: cfg.Name},
					nil,
					&options,
				)

				var unsupported *externalauthsupport.UnsupportedTypeError
				if externalauthsupport.AppliedByVirtualMCPServer(consumer, authType) {
					require.NoError(t, err, "a type applied by VirtualMCPServer must not fail the backend")
					assert.Empty(t, options, "a type applied by VirtualMCPServer adds nothing to the run config")
				} else if externalauthsupport.Supports(consumer, authType) {
					if err != nil {
						assert.False(t, errors.As(err, &unsupported),
							"supported type %q must pass the support check; got %v", authType, err)
					}
				} else {
					require.Error(t, err, "unsupported type %q must not silently no-op", authType)
					require.ErrorAs(t, err, &unsupported)
					assert.Equal(t, consumer, unsupported.Consumer)
					assert.Equal(t, authType, unsupported.AuthType)
					assert.Empty(t, options, "an unsupported type must not contribute run-config options")
				}
			})
		}
	}
}
