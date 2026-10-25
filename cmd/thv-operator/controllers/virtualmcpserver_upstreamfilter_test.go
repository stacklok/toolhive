// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

// TestValidateAuthServerUpstreamFilter covers the reconciler wrapper around the
// shared upstreamfilter.NewGroupBasedFilter constructor: the thin CRD-to-
// runtime translation and the terminal AuthServerConfigValidated=False
// condition it must set on invalid configuration. Exhaustive invariant
// coverage lives alongside the constructor itself in
// pkg/authserver/upstreamfilter; this test asserts only the pieces the
// reconciler contributes.
func TestValidateAuthServerUpstreamFilter(t *testing.T) {
	t.Parallel()

	validAuthServerConfig := &mcpv1beta1.EmbeddedAuthServerConfig{
		Issuer: "https://authserver.example.com",
		UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
			{Name: "okta"},
			{Name: "jira"},
			{Name: "slack"},
		},
	}

	tests := []struct {
		name             string
		authServerConfig *mcpv1beta1.EmbeddedAuthServerConfig
		filter           *mcpv1beta1.AuthServerUpstreamFilterConfig
		expectError      bool
		expectedMessage  string
	}{
		{
			name:             "nil filter is a no-op",
			authServerConfig: validAuthServerConfig,
			filter:           nil,
			expectError:      false,
		},
		{
			name:             "valid filter passes",
			authServerConfig: validAuthServerConfig,
			filter: &mcpv1beta1.AuthServerUpstreamFilterConfig{
				Rules: []mcpv1beta1.UpstreamFilterGroupRule{
					{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira"}},
				},
			},
			expectError: false,
		},
		{
			name:             "filter without authServerConfig is rejected (CEL bypass defense)",
			authServerConfig: nil,
			filter: &mcpv1beta1.AuthServerUpstreamFilterConfig{
				Rules: []mcpv1beta1.UpstreamFilterGroupRule{
					{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira"}},
				},
			},
			expectError:     true,
			expectedMessage: "requires spec.authServerConfig to be set",
		},
		{
			name:             "filter references unknown provider",
			authServerConfig: validAuthServerConfig,
			filter: &mcpv1beta1.AuthServerUpstreamFilterConfig{
				Rules: []mcpv1beta1.UpstreamFilterGroupRule{
					{Groups: []string{"engineering"}, UpstreamProviders: []string{"salesforce"}},
				},
			},
			expectError:     true,
			expectedMessage: `"salesforce" is not a configured upstream`,
		},
		{
			name:             "filter references the mandatory first provider",
			authServerConfig: validAuthServerConfig,
			filter: &mcpv1beta1.AuthServerUpstreamFilterConfig{
				Rules: []mcpv1beta1.UpstreamFilterGroupRule{
					{Groups: []string{"engineering"}, UpstreamProviders: []string{"okta"}},
				},
			},
			expectError:     true,
			expectedMessage: `must not reference the mandatory first upstream`,
		},
		{
			name:             "defaultUpstreams references unknown provider",
			authServerConfig: validAuthServerConfig,
			filter: &mcpv1beta1.AuthServerUpstreamFilterConfig{
				Rules: []mcpv1beta1.UpstreamFilterGroupRule{
					{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira"}},
				},
				DefaultUpstreams: []string{"salesforce"},
			},
			expectError:     true,
			expectedMessage: `"salesforce" is not a configured upstream`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := []v1beta1test.VirtualMCPServerOption{
				v1beta1test.MutateVMCP(func(v *mcpv1beta1.VirtualMCPServer) {
					v.Generation = 1
					v.Spec.AuthServerUpstreamFilter = tc.filter
				}),
			}
			if tc.authServerConfig != nil {
				opts = append(opts, v1beta1test.WithVMCPAuthServerConfig(tc.authServerConfig))
			}
			vmcp := v1beta1test.NewVirtualMCPServer("test-vmcp", "default", opts...)

			r := &VirtualMCPServerReconciler{}
			statusManager := virtualmcpserverstatus.NewStatusManager(vmcp)
			err := r.validateAuthServerUpstreamFilter(vmcp, statusManager)

			if !tc.expectError {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			if tc.expectedMessage != "" {
				assert.Contains(t, err.Error(), tc.expectedMessage)
			}

			// Error path writes phase, message, and the AuthServerConfigValidated
			// condition — UpdateStatus must report a change.
			assert.True(t, statusManager.UpdateStatus(t.Context(), &vmcp.Status))
			assert.Equal(t, mcpv1beta1.VirtualMCPServerPhaseFailed, vmcp.Status.Phase)
			assert.NotEmpty(t, vmcp.Status.Message)
			if tc.expectedMessage != "" {
				assert.Contains(t, vmcp.Status.Message, tc.expectedMessage)
			}
			assert.Equal(t, vmcp.Generation, vmcp.Status.ObservedGeneration)

			found := false
			for _, cond := range vmcp.Status.Conditions {
				if cond.Type != mcpv1beta1.ConditionTypeAuthServerConfigValidated {
					continue
				}
				found = true
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, mcpv1beta1.ConditionReasonAuthServerConfigInvalid, cond.Reason)
				if tc.expectedMessage != "" {
					assert.Contains(t, cond.Message, tc.expectedMessage)
				}
			}
			assert.True(t, found, "AuthServerConfigValidated condition should be set to False")
		})
	}
}
