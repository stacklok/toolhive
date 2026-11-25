// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcpconfig

import (
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// mappedCedarOptionJSONTags lists every cedar.ConfigOptions field (by JSON tag)
// the vMCP converter must carry through from the authz payload into
// vmcpconfig.AuthzConfig, on both the MCPAuthzConfig-ref and ConfigMap paths.
// The reflection check in TestConvertAuthzConfig_MapsEveryCedarOption fails
// when a new ConfigOptions field is added without being accounted for here
// (or in excludedCedarOptionJSONTags), so the next option cannot be silently
// dropped the way multi_valued_claims was (issue #6737).
var mappedCedarOptionJSONTags = map[string]bool{
	"policies":            true,
	"entities_json":       true,
	"group_claim_name":    true,
	"role_claim_name":     true,
	"group_entity_type":   true,
	"multi_valued_claims": true,
}

// excludedCedarOptionJSONTags lists cedar.ConfigOptions fields the converter
// deliberately does NOT copy from the authz payload. PrimaryUpstreamProvider
// is an auth-server (control-plane) property resolved from
// spec.authServerConfig by resolvePrimaryUpstreamProvider; a data-plane
// config author must never pick the trusted upstream.
var excludedCedarOptionJSONTags = map[string]bool{
	"primary_upstream_provider": true,
}

// assertEveryCedarOptionAccountedFor fails when a cedar.ConfigOptions field is
// neither mapped through the converter nor deliberately excluded.
func assertEveryCedarOptionAccountedFor(t *testing.T) {
	t.Helper()

	typ := reflect.TypeOf(cedar.ConfigOptions{})
	for i := range typ.NumField() {
		field := typ.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		assert.True(t,
			mappedCedarOptionJSONTags[tag] || excludedCedarOptionJSONTags[tag],
			"cedar.ConfigOptions field %q (json tag %q) is neither mapped through "+
				"the vMCP converter nor listed as a deliberate exclusion; "+
				"add it to mappedCedarOptionJSONTags (and plumb it through "+
				"resolveAuthzConfigRef/convertAuthzConfig) or to "+
				"excludedCedarOptionJSONTags with a comment explaining why",
			field.Name, tag)
	}
}

// assertAuthzCarriesFullCedarPayload checks the converted AuthzConfig carries
// every mapped option and that the excluded PrimaryUpstreamProvider was NOT
// copied from the payload.
func assertAuthzCarriesFullCedarPayload(t *testing.T, authz *vmcpconfig.AuthzConfig) {
	t.Helper()

	require.Len(t, authz.Policies, 1)
	assert.Contains(t, authz.Policies[0], `ClaimGroup::"engineering"`)
	assert.Contains(t, authz.EntitiesJSON, `"ClaimGroup"`)
	assert.Equal(t, "groups", authz.GroupClaimName)
	assert.Equal(t, "roles", authz.RoleClaimName)
	assert.Equal(t, "ClaimGroup", authz.GroupEntityType)
	assert.Equal(t, []string{"scope"}, authz.MultiValuedClaims)
	assert.Empty(t, authz.PrimaryUpstreamProvider,
		"primary_upstream_provider in the payload must not be copied; it is resolved from spec.authServerConfig")
}

// TestConvertAuthzConfig_MapsEveryCedarOption is the guard test for #6737.
// Both authz sources (MCPAuthzConfig reference and ConfigMap) are fed a
// payload with every Cedar option set, and the converted config must carry
// them all through.
func TestConvertAuthzConfig_MapsEveryCedarOption(t *testing.T) {
	t.Parallel()

	assertEveryCedarOptionAccountedFor(t)

	t.Run("MCPAuthzConfig ref path", func(t *testing.T) {
		t.Parallel()

		payload := `{"policies":["permit(principal in ClaimGroup::\"engineering\", action == Action::\"call_tool\", resource);"],` +
			`"entities_json":"[{\"uid\":{\"type\":\"ClaimGroup\",\"id\":\"engineering\"}}]",` +
			`"group_claim_name":"groups","role_claim_name":"roles","group_entity_type":"ClaimGroup",` +
			`"multi_valued_claims":["scope"],"primary_upstream_provider":"google"}`
		authzCfg := newMCPAuthzConfig("shared-authz", "cedarv1", payload, true)
		vmcp := newAuthzVmcpForRef("shared-authz")

		ctx := log.IntoContext(t.Context(), logr.Discard())
		converter := converterWithObjects(t, authzCfg)

		cfg, _, err := converter.Convert(ctx, vmcp, nil)
		require.NoError(t, err)
		require.NotNil(t, cfg.IncomingAuth)
		require.NotNil(t, cfg.IncomingAuth.Authz)

		assertAuthzCarriesFullCedarPayload(t, cfg.IncomingAuth.Authz)
	})

	t.Run("ConfigMap path", func(t *testing.T) {
		t.Parallel()

		payload := newCedarV1Payload(func(m map[string]any) {
			m["cedar"] = map[string]any{
				"policies": []string{
					`permit(principal in ClaimGroup::"engineering", action == Action::"call_tool", resource);`,
				},
				"entities_json":             `[{"uid":{"type":"ClaimGroup","id":"engineering"}}]`,
				"group_claim_name":          "groups",
				"role_claim_name":           "roles",
				"group_entity_type":         "ClaimGroup",
				"multi_valued_claims":       []string{"scope"},
				"primary_upstream_provider": "google",
			}
		})
		cm := newAuthzConfigMap("authz-cm", "default", "authz.json", payload)
		vmcp := newAuthzVmcpForConfigMap("authz-cm", "authz.json", authServerCfg{}, nil)

		ctx := log.IntoContext(t.Context(), logr.Discard())
		converter := converterWithObjects(t, cm)

		cfg, _, err := converter.Convert(ctx, vmcp, nil)
		require.NoError(t, err)
		require.NotNil(t, cfg.IncomingAuth)
		require.NotNil(t, cfg.IncomingAuth.Authz)

		assertAuthzCarriesFullCedarPayload(t, cfg.IncomingAuth.Authz)
	})
}
