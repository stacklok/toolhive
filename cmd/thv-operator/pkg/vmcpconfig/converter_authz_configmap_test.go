// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcpconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// newAuthzConfigMap builds a ConfigMap whose data[key] contains the given Cedar-v1 payload.
func newAuthzConfigMap(name, namespace, key, payload string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string]string{key: payload},
	}
}

// authServerCfg captures the embedded auth server configuration a test wants to
// attach to its VirtualMCPServer fixture. Empty Upstreams leaves AuthServerConfig
// unset on the spec.
type authServerCfg struct {
	upstreams []string
	primary   string
}

// newAuthzVmcpForConfigMap builds a VirtualMCPServer that references the named authz
// ConfigMap. The optional authServerCfg controls whether the spec carries an
// EmbeddedAuthServerConfig and which upstream is named as primary.
func newAuthzVmcpForConfigMap(
	cmName, cmKey string,
	auth authServerCfg,
	authzRefMutate func(r *mcpv1beta1.AuthzConfigRef),
) *mcpv1beta1.VirtualMCPServer {
	authzRef := &mcpv1beta1.AuthzConfigRef{
		Type: mcpv1beta1.AuthzConfigTypeConfigMap,
		ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
			Name: cmName,
			Key:  cmKey,
		},
	}
	if authzRefMutate != nil {
		authzRefMutate(authzRef)
	}

	vmcp := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type:        "anonymous",
				AuthzConfig: authzRef,
			},
		},
	}
	if len(auth.upstreams) > 0 {
		ups := make([]mcpv1beta1.UpstreamProviderConfig, 0, len(auth.upstreams))
		for _, name := range auth.upstreams {
			ups = append(ups, mcpv1beta1.UpstreamProviderConfig{Name: name})
		}
		vmcp.Spec.AuthServerConfig = &mcpv1beta1.EmbeddedAuthServerConfig{
			UpstreamProviders:       ups,
			PrimaryUpstreamProvider: auth.primary,
		}
	}
	return vmcp
}

// mustJSON encodes the value to a JSON string; failures panic since these are static
// payloads in tests.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// newCedarV1Payload returns the canonical Cedar v1 JSON payload, optionally overriding
// individual fields via the mutator. Defaults match the enterprise authz.json shape.
func newCedarV1Payload(mutate func(m map[string]any)) string {
	m := map[string]any{
		"version": "1.0",
		"type":    "cedarv1",
		"cedar": map[string]any{
			"policies":      []string{`permit(principal, action, resource);`},
			"entities_json": `[]`,
		},
	}
	if mutate != nil {
		mutate(m)
	}
	return mustJSON(m)
}

// converterWithObjects wires a Converter with both the VirtualMCPServer's runtime
// dependencies (OIDC resolver) and arbitrary k8s objects (ConfigMaps for authz).
func converterWithObjects(t *testing.T, objects ...client.Object) *Converter {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	converter, err := NewConverter(newNoOpMockResolver(t), k8sClient)
	require.NoError(t, err)
	return converter
}

// TestConvertAuthzConfig_ConfigMapPath verifies the converter resolves the authz
// ConfigMap via the shared loader and surfaces all Cedar fields end-to-end.
func TestConvertAuthzConfig_ConfigMapPath(t *testing.T) {
	t.Parallel()

	const cmName = "authz-cm"
	const cmKey = "authz.json"
	const ns = "default"

	tests := []struct {
		name           string
		payload        func() string // payload written to the configMap
		mutateAuthzRef func(r *mcpv1beta1.AuthzConfigRef)
		authServer     authServerCfg
		expectErr      string
		validate       func(t *testing.T, authz *vmcpconfig.AuthzConfig)
	}{
		{
			name: "success: full payload round-trips into vmcp config",
			payload: func() string {
				return newCedarV1Payload(func(m map[string]any) {
					m["cedar"] = map[string]any{
						"policies": []string{
							`permit(principal in ClaimGroup::"engineering", action == Action::"call_tool", resource);`,
						},
						"entities_json":     `[{"uid":{"type":"ClaimGroup","id":"engineering"}}]`,
						"group_claim_name":  "groups",
						"role_claim_name":   "roles",
						"group_entity_type": "ClaimGroup",
					}
				})
			},
			validate: func(t *testing.T, authz *vmcpconfig.AuthzConfig) {
				t.Helper()
				require.Equal(t, "cedar", authz.Type)
				require.Len(t, authz.Policies, 1)
				assert.Contains(t, authz.Policies[0], `ClaimGroup::"engineering"`)
				assert.Contains(t, authz.EntitiesJSON, `"ClaimGroup"`)
				assert.Equal(t, "groups", authz.GroupClaimName)
				assert.Equal(t, "roles", authz.RoleClaimName)
				assert.Equal(t, "ClaimGroup", authz.GroupEntityType)
			},
		},
		{
			name: "spec-level override wins over ConfigMap value",
			payload: func() string {
				return newCedarV1Payload(func(m map[string]any) {
					m["cedar"] = map[string]any{
						"policies":          []string{`permit(principal, action, resource);`},
						"entities_json":     `[]`,
						"group_claim_name":  "cm-groups",
						"role_claim_name":   "cm-roles",
						"group_entity_type": "CMGroup",
					}
				})
			},
			mutateAuthzRef: func(r *mcpv1beta1.AuthzConfigRef) {
				r.GroupClaimName = "spec-groups"
				r.GroupEntityType = "SpecGroup"
				// RoleClaimName intentionally left unset on spec to assert ConfigMap fallback survives.
			},
			validate: func(t *testing.T, authz *vmcpconfig.AuthzConfig) {
				t.Helper()
				assert.Equal(t, "spec-groups", authz.GroupClaimName)
				assert.Equal(t, "cm-roles", authz.RoleClaimName)
				assert.Equal(t, "SpecGroup", authz.GroupEntityType)
			},
		},
		{
			// primaryUpstreamProvider now lives on spec.authServerConfig, not in
			// the ConfigMap payload. A primary_upstream_provider entry in the
			// payload is silently ignored: authz.PrimaryUpstreamProvider takes
			// the spec value (first upstream auto-selected when spec is empty).
			name: "primary_upstream_provider in ConfigMap payload is ignored",
			payload: func() string {
				return newCedarV1Payload(func(m map[string]any) {
					m["cedar"] = map[string]any{
						"policies":                  []string{`permit(principal, action, resource);`},
						"entities_json":             `[]`,
						"primary_upstream_provider": "google",
					}
				})
			},
			authServer: authServerCfg{upstreams: []string{"okta"}},
			validate: func(t *testing.T, authz *vmcpconfig.AuthzConfig) {
				t.Helper()
				assert.Equal(t, "okta", authz.PrimaryUpstreamProvider)
			},
		},
		{
			// spec.authServerConfig.primaryUpstreamProvider is the only source
			// for the canonical primary value when the converter is on the
			// configMap path.
			name: "spec.authServerConfig.primaryUpstreamProvider is honored on configMap path",
			payload: func() string {
				return newCedarV1Payload(nil)
			},
			authServer: authServerCfg{upstreams: []string{"okta", "github"}, primary: "github"},
			validate: func(t *testing.T, authz *vmcpconfig.AuthzConfig) {
				t.Helper()
				assert.Equal(t, "github", authz.PrimaryUpstreamProvider)
			},
		},
		{
			name:      "missing configMap returns error",
			payload:   nil, // do not create the configMap
			expectErr: `failed to get Authz ConfigMap default/authz-cm`,
		},
		{
			name: "missing key returns error",
			payload: func() string {
				// create the configMap but under a different key
				return ""
			},
			expectErr: `is missing key "authz.json"`,
		},
		{
			name: "empty value returns error",
			payload: func() string {
				return "   "
			},
			expectErr: `is empty`,
		},
		{
			name: "malformed payload returns error",
			payload: func() string {
				return "{ this is not valid json or yaml"
			},
			expectErr: `failed to parse authz config from ConfigMap`,
		},
		{
			// spec.authServerConfig.primaryUpstreamProvider that doesn't match
			// any declared upstream is rejected at convert time.
			name: "spec primary provider not matching any upstream is rejected",
			payload: func() string {
				return newCedarV1Payload(nil)
			},
			authServer: authServerCfg{upstreams: []string{"okta"}, primary: "google"},
			expectErr:  `does not match any upstream declared on spec.authServerConfig.upstreamProviders`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var objs []client.Object
			if tt.payload != nil {
				key := cmKey
				payload := tt.payload()
				// "missing key" case: store under a different key.
				if tt.name == "missing key returns error" {
					key = "other.json"
					payload = newCedarV1Payload(nil)
				}
				objs = append(objs, newAuthzConfigMap(cmName, ns, key, payload))
			}
			vmcp := newAuthzVmcpForConfigMap(cmName, cmKey, tt.authServer, tt.mutateAuthzRef)
			ctx := log.IntoContext(t.Context(), logr.Discard())
			converter := converterWithObjects(t, objs...)

			cfg, _, err := converter.Convert(ctx, vmcp, nil)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.True(t, strings.Contains(err.Error(), tt.expectErr),
					"expected error containing %q, got %q", tt.expectErr, err.Error())
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg.IncomingAuth)
			require.NotNil(t, cfg.IncomingAuth.Authz)
			if tt.validate != nil {
				tt.validate(t, (*vmcpconfig.AuthzConfig)(cfg.IncomingAuth.Authz))
			}
		})
	}
}

// TestConvertAuthzConfig_InlinePath_NewFieldsSourcedFromAuthzConfigRef confirms that
// after the schema lift the source-agnostic Cedar settings (group claim name etc.)
// flow from AuthzConfigRef directly into the vmcp config for inline-mode users.
func TestConvertAuthzConfig_InlinePath_NewFieldsSourcedFromAuthzConfigRef(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
				AuthzConfig: &mcpv1beta1.AuthzConfigRef{
					Type: mcpv1beta1.AuthzConfigTypeInline,
					Inline: &mcpv1beta1.InlineAuthzConfig{
						Policies:     []string{`permit(principal, action, resource);`},
						EntitiesJSON: `[{"uid":{"type":"ClaimGroup","id":"engineering"}}]`,
					},
					GroupClaimName:  "groups",
					RoleClaimName:   "roles",
					GroupEntityType: "ClaimGroup",
				},
			},
		},
	}

	ctx := log.IntoContext(t.Context(), logr.Discard())
	converter := converterWithObjects(t)
	cfg, _, err := converter.Convert(ctx, vmcp, nil)
	require.NoError(t, err)
	require.NotNil(t, cfg.IncomingAuth)
	require.NotNil(t, cfg.IncomingAuth.Authz)

	authz := cfg.IncomingAuth.Authz
	assert.Equal(t, "cedar", authz.Type)
	assert.Equal(t, []string{`permit(principal, action, resource);`}, authz.Policies)
	assert.Equal(t, `[{"uid":{"type":"ClaimGroup","id":"engineering"}}]`, authz.EntitiesJSON)
	assert.Equal(t, "groups", authz.GroupClaimName)
	assert.Equal(t, "roles", authz.RoleClaimName)
	assert.Equal(t, "ClaimGroup", authz.GroupEntityType)
}
