// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestDiscoverAndResolveAuth_ValidatesFetchedResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1beta1.MCPExternalAuthConfig
		wantType     string
		wantError    string
	}{
		{
			name: "Valid False condition rejects supported auth type",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
				},
				Status: mcpv1beta1.MCPExternalAuthConfigStatus{
					Conditions: []metav1.Condition{
						{
							Type:    mcpv1beta1.ConditionTypeValid,
							Status:  metav1.ConditionFalse,
							Reason:  "InvalidConfig",
							Message: "source validation failed",
						},
					},
				},
			},
			wantError: "is invalid (InvalidConfig): source validation failed",
		},
		{
			name: "local validation rejects invalid supported config",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeUpstreamInject,
					UpstreamInject: &mcpv1beta1.UpstreamInjectSpec{
						ProviderName: "",
					},
				},
			},
			wantError: "failed validation: upstreamInject requires a non-empty providerName",
		},
		{
			name: "locally valid resource without status remains usable",
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
				},
			},
			wantType: authtypes.StrategyTypeUnauthenticated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))
			tt.externalAuth.Name = "test-auth"
			tt.externalAuth.Namespace = "default"
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.externalAuth).Build()

			strategy, err := DiscoverAndResolveAuth(
				t.Context(),
				&mcpv1beta1.ExternalAuthConfigRef{Name: tt.externalAuth.Name},
				tt.externalAuth.Namespace,
				k8sClient,
			)

			if tt.wantError != "" {
				require.Error(t, err)
				assert.Nil(t, strategy)
				assert.Contains(t, err.Error(), tt.wantError)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, strategy)
			assert.Equal(t, tt.wantType, strategy.Type)
		})
	}
}
