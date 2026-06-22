// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcpconfig

import (
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// newMCPAuthzConfig builds an MCPAuthzConfig of the given type in the "default"
// namespace whose Config.Raw is the supplied backend payload. When valid is true
// the resource carries a True Valid condition so ValidateAuthzConfigReady accepts it.
func newMCPAuthzConfig(name, authzType, payload string, valid bool) *mcpv1beta1.MCPAuthzConfig {
	cfg := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{
			Type:   authzType,
			Config: runtime.RawExtension{Raw: []byte(payload)},
		},
		Status: mcpv1beta1.MCPAuthzConfigStatus{
			ConfigHash: "hash-abc",
		},
	}
	if valid {
		cfg.Status.Conditions = []metav1.Condition{{
			Type:   mcpv1beta1.ConditionTypeAuthzConfigValid,
			Status: metav1.ConditionTrue,
			Reason: "Validated",
		}}
	}
	return cfg
}

// newAuthzVmcpForRef builds a VirtualMCPServer that references the named
// MCPAuthzConfig via spec.incomingAuth.authzConfigRef.
func newAuthzVmcpForRef(refName string) *mcpv1beta1.VirtualMCPServer {
	return &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type:           "anonymous",
				AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: refName},
			},
		},
	}
}

// cedarRefPayload is the backend-specific (inner) Cedar config stored in
// MCPAuthzConfig.spec.config — BuildFullAuthzConfigJSON wraps it under the
// "cedar" key with version/type envelope.
func cedarRefPayload() string {
	return `{"policies":["permit(principal in ClaimGroup::\"engineering\", action == Action::\"call_tool\", resource);"],` +
		`"entities_json":"[{\"uid\":{\"type\":\"ClaimGroup\",\"id\":\"engineering\"}}]",` +
		`"group_claim_name":"groups","role_claim_name":"roles","group_entity_type":"ClaimGroup"}`
}

// TestConvertAuthzConfigRef_CedarSuccess verifies a cedarv1 MCPAuthzConfig
// reference resolves end-to-end into the vmcp Cedar authz config.
func TestConvertAuthzConfigRef_CedarSuccess(t *testing.T) {
	t.Parallel()

	authzCfg := newMCPAuthzConfig("shared-authz", "cedarv1", cedarRefPayload(), true)
	vmcp := newAuthzVmcpForRef("shared-authz")

	ctx := log.IntoContext(t.Context(), logr.Discard())
	converter := converterWithObjects(t, authzCfg)

	cfg, _, err := converter.Convert(ctx, vmcp, nil)
	require.NoError(t, err)
	require.NotNil(t, cfg.IncomingAuth)
	require.NotNil(t, cfg.IncomingAuth.Authz)

	authz := cfg.IncomingAuth.Authz
	assert.Equal(t, "cedar", authz.Type)
	require.Len(t, authz.Policies, 1)
	assert.Contains(t, authz.Policies[0], `ClaimGroup::"engineering"`)
	assert.Contains(t, authz.EntitiesJSON, `"ClaimGroup"`)
	assert.Equal(t, "groups", authz.GroupClaimName)
	assert.Equal(t, "roles", authz.RoleClaimName)
	assert.Equal(t, "ClaimGroup", authz.GroupEntityType)
}

// TestConvertAuthzConfigRef_NonCedarFailsFast verifies a non-Cedar
// MCPAuthzConfig reference fails fast with a clear error rather than carrying
// inert config through the Cedar-only vMCP runtime.
func TestConvertAuthzConfigRef_NonCedarFailsFast(t *testing.T) {
	t.Parallel()

	authzCfg := newMCPAuthzConfig("http-authz", "httpv1",
		`{"http":{"url":"https://pdp.example.com"}}`, true)
	vmcp := newAuthzVmcpForRef("http-authz")

	ctx := log.IntoContext(t.Context(), logr.Discard())
	converter := converterWithObjects(t, authzCfg)

	_, _, err := converter.Convert(ctx, vmcp, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"httpv1"`)
	assert.Contains(t, err.Error(), "only cedarv1 is supported")
}

// TestConvertAuthzConfigRef_InlineAndRefMutuallyExclusive verifies the
// defense-in-depth guard that rejects a spec setting both inline authzConfig and
// authzConfigRef, independent of the apiserver CEL rule.
func TestConvertAuthzConfigRef_InlineAndRefMutuallyExclusive(t *testing.T) {
	t.Parallel()

	authzCfg := newMCPAuthzConfig("shared-authz", "cedarv1", cedarRefPayload(), true)
	vmcp := newAuthzVmcpForRef("shared-authz")
	vmcp.Spec.IncomingAuth.AuthzConfig = &mcpv1beta1.AuthzConfigRef{
		Type:   mcpv1beta1.AuthzConfigTypeInline,
		Inline: &mcpv1beta1.InlineAuthzConfig{Policies: []string{`permit(principal, action, resource);`}},
	}

	ctx := log.IntoContext(t.Context(), logr.Discard())
	converter := converterWithObjects(t, authzCfg)

	_, _, err := converter.Convert(ctx, vmcp, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestConvertAuthzConfigRef_NotFoundAndNotValid verifies the converter surfaces
// errors when the referenced MCPAuthzConfig is missing or not yet Valid.
func TestConvertAuthzConfigRef_NotFoundAndNotValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		objs      []*mcpv1beta1.MCPAuthzConfig
		expectErr string
	}{
		{
			name:      "not found",
			objs:      nil,
			expectErr: "failed to get MCPAuthzConfig",
		},
		{
			name:      "not valid",
			objs:      []*mcpv1beta1.MCPAuthzConfig{newMCPAuthzConfig("shared-authz", "cedarv1", cedarRefPayload(), false)},
			expectErr: "is not valid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcp := newAuthzVmcpForRef("shared-authz")
			ctx := log.IntoContext(t.Context(), logr.Discard())
			converter := func() *Converter {
				if len(tt.objs) == 0 {
					return converterWithObjects(t)
				}
				return converterWithObjects(t, tt.objs[0])
			}()

			_, _, err := converter.Convert(ctx, vmcp, nil)
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tt.expectErr),
				"expected error containing %q, got %q", tt.expectErr, err.Error())
		})
	}
}
