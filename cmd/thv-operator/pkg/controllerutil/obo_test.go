// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	oboTestNamespace  = "default"
	oboTestSecretName = "obo-client-secret"
	oboTestSecretKey  = "client-secret"
	oboTestSecretVal  = "super-secret-value"
	oboTestTenant     = "contoso.onmicrosoft.com"
	oboTestClientID   = "11111111-1111-1111-1111-111111111111"
	// oboWantTokenURL is the endpoint derived from oboTestTenant with the
	// default authority, pinned so a change to the URL construction is caught.
	oboWantTokenURL = "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/token"
)

// validOBOConfig returns a well-formed shared-secret OBO config.
func validOBOConfig() *mcpv1beta1.OBOConfig {
	return &mcpv1beta1.OBOConfig{
		TenantID:        oboTestTenant,
		ClientID:        oboTestClientID,
		ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: oboTestSecretName, Key: oboTestSecretKey},
		Audience:        "api://backend/.default",
	}
}

// mutatedOBOConfig returns a valid OBO config with f applied.
func mutatedOBOConfig(f func(*mcpv1beta1.OBOConfig)) *mcpv1beta1.OBOConfig {
	cfg := validOBOConfig()
	f(cfg)
	return cfg
}

func oboCR(name string, cfg *mcpv1beta1.OBOConfig) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: oboTestNamespace},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  cfg,
		},
	}
}

// oboClient returns a fake client holding objs plus the referenced client Secret.
func oboClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	objs = append(objs, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: oboTestSecretName, Namespace: oboTestNamespace},
		Data:       map[string][]byte{oboTestSecretKey: []byte(oboTestSecretVal)},
	})
	return fake.NewClientBuilder().WithScheme(testutil.NewScheme(t)).WithObjects(objs...).Build()
}

func TestOBOValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *mcpv1beta1.OBOConfig
		wantMsg string // empty means valid
	}{
		{name: "audience only", cfg: validOBOConfig()},
		{name: "scopes only", cfg: mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) {
			c.Audience = ""
			c.Scopes = []string{"api://backend/.default"}
		})},
		{name: "GUID tenant", cfg: mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) {
			c.TenantID = "72f988bf-86f1-41af-91ab-2d7cd011db47"
		})},
		{name: "sovereign authority", cfg: mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) {
			c.Authority = "https://login.microsoftonline.us"
		})},
		{name: "non-negative cache skew", cfg: mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) {
			c.CacheSkew = &metav1.Duration{Duration: 30 * time.Second}
		})},
		{name: "nil obo config", cfg: nil, wantMsg: msgOBOConfigRequired},
		{
			name:    "missing client id",
			cfg:     mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.ClientID = "" }),
			wantMsg: msgOBOClientIDRequired,
		},
		{
			name:    "nil client secret ref",
			cfg:     mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.ClientSecretRef = nil }),
			wantMsg: msgOBOClientSecretRef,
		},
		{
			name:    "client secret ref missing key",
			cfg:     mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.ClientSecretRef.Key = "" }),
			wantMsg: msgOBOClientSecretRef,
		},
		{
			name: "no audience and only blank scopes",
			cfg: mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) {
				c.Audience = ""
				c.Scopes = []string{"   "}
			}),
			wantMsg: msgOBOAudienceOrScopes,
		},
		{
			name:    "negative cache skew",
			cfg:     mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.CacheSkew = &metav1.Duration{Duration: -time.Second} }),
			wantMsg: msgOBOCacheSkew,
		},
		{
			name:    "missing tenant",
			cfg:     mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.TenantID = "" }),
			wantMsg: "entra obo: TenantID is required",
		},
		{
			name:    "tenant neither guid nor domain",
			cfg:     mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.TenantID = "not a tenant" }),
			wantMsg: "entra obo: TenantID must be a directory GUID or a domain name",
		},
		{
			name:    "non-loopback http authority",
			cfg:     mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.Authority = "http://login.microsoftonline.com" }),
			wantMsg: "entra obo: Authority must use https (http allowed only for loopback)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := OBOValidate(oboCR("obo-config", tt.cfg))
			if tt.wantMsg == "" {
				require.NoError(t, err)
				return
			}
			var vErr *obo.ValidationError
			require.ErrorAs(t, err, &vErr, "a malformed config must be a permanent ValidationError")
			assert.EqualError(t, err, tt.wantMsg)
			assert.NotContains(t, err.Error(), oboTestSecretName, "the message must not name the Secret")
		})
	}
}

func TestAddExternalAuthConfigOptions_OBO(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		provider        string
		wantProviderKey bool
	}{
		{name: "inbound mode (provider empty)"},
		{name: "embedded auth server (provider set)", provider: "entra-upstream", wantProviderKey: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validOBOConfig()
			cfg.Scopes = []string{"api://backend/.default", "offline_access"}
			cfg.SubjectTokenProviderName = tt.provider
			cfg.CacheSkew = &metav1.Duration{Duration: 30 * time.Second}
			authCfg := oboCR("obo-config", cfg)

			var opts []runner.RunConfigBuilderOption
			require.NoError(t, AddExternalAuthConfigOptions(
				t.Context(), oboClient(t, authCfg), oboTestNamespace, "server",
				&mcpv1beta1.ExternalAuthConfigRef{Name: authCfg.Name},
				&oidc.OIDCConfig{Issuer: "https://issuer.example.com"}, &opts,
			))
			require.Len(t, opts, 1, "exactly one builder option must be appended")

			rc, err := runner.NewOperatorRunConfigBuilder(t.Context(), nil, nil, nil,
				append([]runner.RunConfigBuilderOption{
					runner.WithName("obo-test"),
					runner.WithImage("ghcr.io/example/mcp:latest"),
				}, opts...)...)
			require.NoError(t, err)
			require.Len(t, rc.AdditionalMiddlewareConfigs, 1)

			mc := rc.AdditionalMiddlewareConfigs[0]
			assert.Equal(t, obo.MiddlewareType, mc.Type)

			var got obo.MiddlewareParameters
			require.NoError(t, json.Unmarshal(mc.Parameters, &got))
			assert.Equal(t, obo.MiddlewareParameters{
				TokenURL:                 oboWantTokenURL,
				ClientID:                 oboTestClientID,
				Audience:                 "api://backend/.default",
				Scopes:                   []string{"api://backend/.default", "offline_access"},
				ClientSecretEnvVar:       obo.ClientSecretEnvVar,
				SubjectTokenProviderName: tt.provider,
				CacheSkewSeconds:         30,
			}, got)

			// Only the env var name travels in the contract, never the value.
			assert.NotContains(t, string(mc.Parameters), oboTestSecretVal)
			if tt.wantProviderKey {
				assert.Contains(t, string(mc.Parameters), "subjectTokenProviderName")
			} else {
				assert.NotContains(t, string(mc.Parameters), "subjectTokenProviderName")
			}
		})
	}
}

func TestAddExternalAuthConfigOptions_OBOErrors(t *testing.T) {
	t.Parallel()

	issuer := &oidc.OIDCConfig{Issuer: "https://issuer.example.com"}
	scheme := testutil.NewScheme(t)

	tests := []struct {
		name          string
		cfg           *mcpv1beta1.OBOConfig
		client        func(t *testing.T, authCfg *mcpv1beta1.MCPExternalAuthConfig) client.Client
		oidcConfig    *oidc.OIDCConfig
		wantPermanent bool
		wantMsg       string
	}{
		{
			name: "no incoming OIDC is a permanent ValidationError",
			cfg:  validOBOConfig(),
			client: func(t *testing.T, a *mcpv1beta1.MCPExternalAuthConfig) client.Client {
				t.Helper()
				return oboClient(t, a)
			},
			oidcConfig:    nil,
			wantPermanent: true,
			wantMsg:       MsgOBORequiresIncomingAuth,
		},
		{
			name: "malformed config is a permanent ValidationError",
			cfg:  mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.TenantID = "not a tenant" }),
			client: func(t *testing.T, a *mcpv1beta1.MCPExternalAuthConfig) client.Client {
				t.Helper()
				return oboClient(t, a)
			},
			oidcConfig:    issuer,
			wantPermanent: true,
		},
		{
			name: "missing secret is transient",
			cfg:  validOBOConfig(),
			client: func(_ *testing.T, a *mcpv1beta1.MCPExternalAuthConfig) client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(a).Build()
			},
			oidcConfig: issuer,
		},
		{
			name: "secret missing the key is transient",
			cfg:  validOBOConfig(),
			client: func(_ *testing.T, a *mcpv1beta1.MCPExternalAuthConfig) client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(a, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: oboTestSecretName, Namespace: oboTestNamespace},
					Data:       map[string][]byte{"wrong-key": []byte(oboTestSecretVal)},
				}).Build()
			},
			oidcConfig: issuer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			authCfg := oboCR("obo-config", tt.cfg)

			var opts []runner.RunConfigBuilderOption
			err := AddExternalAuthConfigOptions(
				t.Context(), tt.client(t, authCfg), oboTestNamespace, "server",
				&mcpv1beta1.ExternalAuthConfigRef{Name: authCfg.Name}, tt.oidcConfig, &opts,
			)
			require.Error(t, err)
			var vErr *obo.ValidationError
			assert.Equal(t, tt.wantPermanent, errors.As(err, &vErr))
			if tt.wantMsg != "" {
				assert.EqualError(t, err, tt.wantMsg)
			}
			assert.Empty(t, opts, "no option appended on error")
		})
	}
}

func TestValidateOBOIncomingAuth(t *testing.T) {
	t.Parallel()

	oboCfg := oboCR("obo-config", validOBOConfig())
	otherCfg := &mcpv1beta1.MCPExternalAuthConfig{
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{Type: mcpv1beta1.ExternalAuthTypeTokenExchange},
	}

	tests := []struct {
		name    string
		cfg     *mcpv1beta1.MCPExternalAuthConfig
		hasOIDC bool
		wantErr bool
	}{
		{name: "obo without incoming OIDC is rejected", cfg: oboCfg, wantErr: true},
		{name: "obo with incoming OIDC is accepted", cfg: oboCfg, hasOIDC: true},
		{name: "other types are not affected", cfg: otherCfg},
		{name: "nil config is accepted", cfg: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateOBOIncomingAuth(tt.cfg, tt.hasOIDC)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			var vErr *obo.ValidationError
			require.ErrorAs(t, err, &vErr)
			assert.Equal(t, MsgOBORequiresIncomingAuth, vErr.Message)
		})
	}
}

// TestAddOBOSecretEnvVars covers the OBO-only secret-env dispatcher. Only obo
// contributes an env var; the other secret-bearing types are populated so the
// empty result is a real invariant, not an artifact of an absent secret ref.
func TestAddOBOSecretEnvVars(t *testing.T) {
	t.Parallel()

	others := []*mcpv1beta1.MCPExternalAuthConfig{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "te", Namespace: oboTestNamespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1beta1.TokenExchangeConfig{
					ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "te-secret", Key: "client-secret"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bt", Namespace: oboTestNamespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeBearerToken,
				BearerToken: &mcpv1beta1.BearerTokenConfig{
					TokenSecretRef: &mcpv1beta1.SecretKeyRef{Name: "bt-secret", Key: "token"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "hi", Namespace: oboTestNamespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeHeaderInjection,
				HeaderInjection: &mcpv1beta1.HeaderInjectionConfig{
					ValueSecretRef: &mcpv1beta1.SecretKeyRef{Name: "hi-secret", Key: "value"},
				},
			},
		},
	}
	objs := []client.Object{oboCR("obo", validOBOConfig()), oboCR("obo-bad", nil)}
	for _, o := range others {
		objs = append(objs, o)
	}
	c := oboClient(t, objs...)

	tests := []struct {
		name     string
		ref      *mcpv1beta1.ExternalAuthConfigRef
		wantEnvs []corev1.EnvVar
		wantErr  bool
	}{
		{name: "nil ref returns no env vars"},
		{name: "missing config returns error", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "missing"}, wantErr: true},
		{name: "tokenExchange contributes no env vars here", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "te"}},
		{name: "bearerToken contributes no env vars here", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "bt"}},
		{name: "headerInjection contributes no env vars here", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "hi"}},
		{
			name: "obo mounts the client secret under the fixed proxy env var",
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "obo"},
			wantEnvs: []corev1.EnvVar{
				oboSecretEnvVar(obo.ClientSecretEnvVar, &mcpv1beta1.SecretKeyRef{Name: oboTestSecretName, Key: oboTestSecretKey}),
			},
		},
		{name: "obo without a spec is an error", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "obo-bad"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envVars, err := AddOBOSecretEnvVars(t.Context(), c, oboTestNamespace, tt.ref)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnvs, envVars)
			for _, ev := range envVars {
				assert.Empty(t, ev.Value, "the secret value must never be inlined")
			}
		})
	}
}

func TestOBOVMCPSecretEnvVar_UniquePerConfig(t *testing.T) {
	t.Parallel()

	first, err := OBOVMCPSecretEnvVar(oboCR("backend-a", validOBOConfig()))
	require.NoError(t, err)
	second, err := OBOVMCPSecretEnvVar(oboCR("backend-b", validOBOConfig()))
	require.NoError(t, err)

	require.Len(t, first, 1)
	require.Len(t, second, 1)
	assert.Equal(t, "TOOLHIVE_OBO_CLIENT_SECRET_BACKEND_A", first[0].Name)
	assert.Equal(t, "TOOLHIVE_OBO_CLIENT_SECRET_BACKEND_B", second[0].Name)
	assert.Equal(t, GenerateUniqueOBOSecretEnvVarName("backend-a"), first[0].Name,
		"the deployment and the converter enrichment must agree on the name")

	_, err = OBOVMCPSecretEnvVar(oboCR("bad", mutatedOBOConfig(func(c *mcpv1beta1.OBOConfig) { c.ClientSecretRef = nil })))
	var vErr *obo.ValidationError
	assert.ErrorAs(t, err, &vErr)
}

// withOBOHandlerRestored restores the package-level OBO handler after a test
// that calls the deprecated RegisterOBOHandler.
func withOBOHandlerRestored(t *testing.T) {
	t.Helper()
	oboMu.RLock()
	original := oboHandler
	oboMu.RUnlock()
	t.Cleanup(func() {
		oboMu.Lock()
		oboHandler = original
		oboMu.Unlock()
	})
}

//nolint:paralleltest // Mutates package-level oboHandler; must not race other tests.
func TestRegisterOBOHandler_OverridesBuiltIn(t *testing.T) {
	withOBOHandlerRestored(t)

	sentinel := errors.New("registered handler")
	envs := []corev1.EnvVar{{Name: "OVERRIDE", Value: "x"}}
	RegisterOBOHandler(OBOHandler{
		Validate: func(*mcpv1beta1.MCPExternalAuthConfig) error { return sentinel },
		ApplyRunConfig: func(context.Context, client.Client, string,
			*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption) error {
			return sentinel
		},
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) { return envs, nil },
	})

	assert.ErrorIs(t, OBOValidate(nil), sentinel)
	assert.ErrorIs(t, OBOApplyRunConfig(t.Context(), nil, oboTestNamespace, nil, nil), sentinel)
	got, err := OBOSecretEnvVars(nil)
	require.NoError(t, err)
	assert.Equal(t, envs, got)
}

func TestRegisterOBOHandler_PanicsOnNilField(t *testing.T) {
	t.Parallel()

	validate := func(*mcpv1beta1.MCPExternalAuthConfig) error { return nil }
	apply := func(context.Context, client.Client, string,
		*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption) error {
		return nil
	}
	secretEnvVars := func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) { return nil, nil }

	for name, h := range map[string]OBOHandler{
		"Validate nil":       {ApplyRunConfig: apply, SecretEnvVars: secretEnvVars},
		"ApplyRunConfig nil": {Validate: validate, SecretEnvVars: secretEnvVars},
		"SecretEnvVars nil":  {Validate: validate, ApplyRunConfig: apply},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Panics(t, func() { RegisterOBOHandler(h) })
		})
	}
}
