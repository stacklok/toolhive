// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

const (
	testTenant      = "contoso.onmicrosoft.com"
	testClientID    = "client-123"
	testSecretName  = "obo-secret"
	testSecretKey   = "client-secret"
	testSecretValue = "s3cr3t-value"
	testNamespace   = "default"

	// testTokenURL is the endpoint entra.TokenURL derives from testTenant with
	// the default authority — pinned here so the test fails if the mapping drifts.
	testTokenURL = "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/token"
)

// validOBOCR returns a well-formed obo-typed MCPExternalAuthConfig: a domain
// tenant, a client ID, a secret reference, and a single scope.
func validOBOCR() *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "obo-config", Namespace: testNamespace},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO: &mcpv1beta1.OBOConfig{
				TenantID:        testTenant,
				ClientID:        testClientID,
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: testSecretName, Key: testSecretKey},
				Scopes:          []string{"api://backend/.default"},
			},
		},
	}
}

// TestOBOConverter_IsRegistered verifies the default registry resolves the obo
// type to the built-in OBOConverter.
func TestOBOConverter_IsRegistered(t *testing.T) {
	t.Parallel()

	got, err := DefaultRegistry().GetConverter(mcpv1beta1.ExternalAuthTypeOBO)
	require.NoError(t, err)
	assert.IsType(t, &OBOConverter{}, got)
	assert.Equal(t, authtypes.StrategyTypeOBO, got.StrategyType())
}

// TestOBOConvertToStrategy_WellFormed proves a well-formed OBO config produces a
// valid BackendAuthStrategy: tenant+authority collapsed to the Entra token
// endpoint, fields mapped through, and no secret source set (the operator sets
// a per-config ClientSecretEnv in inline mode; ResolveSecrets sets ClientSecret in
// discovered mode).
func TestOBOConvertToStrategy_WellFormed(t *testing.T) {
	t.Parallel()

	cr := validOBOCR()
	cr.Spec.OBO.Audience = "api://backend"
	cr.Spec.OBO.SubjectTokenProviderName = "entra"
	cr.Spec.OBO.CacheSkew = &metav1.Duration{Duration: 45_000_000_000} // 45s

	strategy, err := ConvertToStrategy(cr)
	require.NoError(t, err)
	require.NotNil(t, strategy)
	require.NotNil(t, strategy.OBO)

	assert.Equal(t, authtypes.StrategyTypeOBO, strategy.Type)
	assert.Equal(t, testTokenURL, strategy.OBO.TokenURL)
	assert.Equal(t, testClientID, strategy.OBO.ClientID)
	assert.Equal(t, "api://backend", strategy.OBO.Audience)
	assert.Equal(t, []string{"api://backend/.default"}, strategy.OBO.Scopes)
	assert.Equal(t, "entra", strategy.OBO.SubjectTokenProviderName)
	require.NotNil(t, strategy.OBO.CacheSkewSeconds)
	assert.Equal(t, int32(45), *strategy.OBO.CacheSkewSeconds)

	assert.Empty(t, strategy.OBO.ClientSecret)
	assert.Empty(t, strategy.OBO.ClientSecretEnv)
}

// TestOBOConvertToStrategy_SovereignAuthority verifies a non-default authority is
// honored in the derived token endpoint.
func TestOBOConvertToStrategy_SovereignAuthority(t *testing.T) {
	t.Parallel()

	cr := validOBOCR()
	cr.Spec.OBO.Authority = "https://login.microsoftonline.us"

	strategy, err := ConvertToStrategy(cr)
	require.NoError(t, err)
	assert.Equal(t,
		"https://login.microsoftonline.us/contoso.onmicrosoft.com/oauth2/v2.0/token",
		strategy.OBO.TokenURL)
}

// TestOBOConvertToStrategy_CacheSkewTruncation proves the metav1.Duration cache skew
// is truncated toward zero to whole seconds — identical to the operator handler
// (int(d.Seconds())) — so the same CRD value yields the same seconds on both
// surfaces. A sub-second skew truncates to 0 (the runtime's "use cache default").
func TestOBOConvertToStrategy_CacheSkewTruncation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		skew time.Duration
		want int32
	}{
		{name: "whole seconds", skew: 45 * time.Second, want: 45},
		{name: "truncates fractional toward zero", skew: 1500 * time.Millisecond, want: 1},
		{name: "truncates just under two seconds", skew: 1900 * time.Millisecond, want: 1},
		{name: "sub-second truncates to zero", skew: 600 * time.Millisecond, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cr := validOBOCR()
			cr.Spec.OBO.CacheSkew = &metav1.Duration{Duration: tt.skew}

			strategy, err := ConvertToStrategy(cr)
			require.NoError(t, err)
			require.NotNil(t, strategy.OBO.CacheSkewSeconds)
			assert.Equal(t, tt.want, *strategy.OBO.CacheSkewSeconds)
		})
	}
}

// TestOBOConvertToStrategy_Malformed proves malformed configs are rejected with
// a message that names the actual reason.
func TestOBOConvertToStrategy_Malformed(t *testing.T) {
	t.Parallel()

	// wantMsg pins the specific rejection reason so a validation-ordering bug that
	// reports the wrong reason is caught (not just "some error occurred"). The
	// converter-owned reasons use its message constants; the tenant/authority rows
	// assert the entra.TokenURL message the converter surfaces verbatim.
	tests := []struct {
		name    string
		mutate  func(*mcpv1beta1.OBOConfig)
		nilOBO  bool
		wantMsg string
	}{
		{name: "nil obo", nilOBO: true, wantMsg: msgOBONil},
		{name: "missing client id", mutate: func(o *mcpv1beta1.OBOConfig) { o.ClientID = "" }, wantMsg: msgClientID},
		{name: "whitespace-only client id", mutate: func(o *mcpv1beta1.OBOConfig) { o.ClientID = " \t " }, wantMsg: msgClientID},
		{name: "whitespace-only secret ref key", mutate: func(o *mcpv1beta1.OBOConfig) { o.ClientSecretRef.Key = "  " }, wantMsg: msgClientSecretRef},
		{name: "missing client secret ref", mutate: func(o *mcpv1beta1.OBOConfig) { o.ClientSecretRef = nil }, wantMsg: msgClientSecretRef},
		{name: "empty secret ref name", mutate: func(o *mcpv1beta1.OBOConfig) { o.ClientSecretRef.Name = "  " }, wantMsg: msgClientSecretRef},
		{name: "empty secret ref key", mutate: func(o *mcpv1beta1.OBOConfig) { o.ClientSecretRef.Key = "" }, wantMsg: msgClientSecretRef},
		{name: "no audience or scopes", mutate: func(o *mcpv1beta1.OBOConfig) { o.Scopes = nil; o.Audience = "" }, wantMsg: msgAudienceOrScopes},
		{name: "negative cache skew", mutate: func(o *mcpv1beta1.OBOConfig) {
			o.CacheSkew = &metav1.Duration{Duration: -1}
		}, wantMsg: msgCacheSkewNegative},
		{name: "cache skew overflows int32 seconds", mutate: func(o *mcpv1beta1.OBOConfig) {
			o.CacheSkew = &metav1.Duration{Duration: (math.MaxInt32 + 1) * time.Second}
		}, wantMsg: msgCacheSkewTooLarge},
		// tenant/authority rejections are delegated to entra.TokenURL, whose
		// (non-secret) message the converter wraps verbatim.
		{name: "missing tenant", mutate: func(o *mcpv1beta1.OBOConfig) { o.TenantID = "" }, wantMsg: "TenantID is required"},
		{name: "tenant neither guid nor domain", mutate: func(o *mcpv1beta1.OBOConfig) { o.TenantID = "not a tenant" }, wantMsg: "must be a directory GUID or a domain name"},
		{name: "tenant with path separator", mutate: func(o *mcpv1beta1.OBOConfig) { o.TenantID = "evil/../x" }, wantMsg: "must be a directory GUID or a domain name"},
		{name: "non-https authority", mutate: func(o *mcpv1beta1.OBOConfig) { o.Authority = "http://login.microsoftonline.com" }, wantMsg: "must use https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cr := validOBOCR()
			if tt.nilOBO {
				cr.Spec.OBO = nil
			} else {
				tt.mutate(cr.Spec.OBO)
			}

			strategy, err := ConvertToStrategy(cr)
			require.Error(t, err)
			assert.Nil(t, strategy)
			assert.ErrorContains(t, err, tt.wantMsg,
				"the rejection must name the actual reason, not merely error")
		})
	}
}

// TestOBOResolveSecrets_PopulatesFromK8s proves ResolveSecrets fetches the
// referenced secret and sets it on the strategy without leaking it onto any
// non-secret field.
func TestOBOResolveSecrets_PopulatesFromK8s(t *testing.T) {
	t.Parallel()

	cr := validOBOCR()
	strategy, err := ConvertToStrategy(cr)
	require.NoError(t, err)
	// A stale env-var reference (the inline-mode secret source) must give way to
	// the resolved value.
	strategy.OBO.ClientSecretEnv = "STALE_OBO_SECRET_ENV"

	k8sClient := fakeClientWithSecret(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testNamespace},
		Data:       map[string][]byte{testSecretKey: []byte(testSecretValue)},
	})

	resolved, err := ResolveSecretsForStrategy(t.Context(), cr, k8sClient, testNamespace, strategy)
	require.NoError(t, err)
	require.NotNil(t, resolved.OBO)

	assert.Equal(t, testSecretValue, resolved.OBO.ClientSecret)
	assert.Empty(t, resolved.OBO.ClientSecretEnv, "the env-var reference must be cleared once resolved")

	// Non-leak: the secret value must appear nowhere but ClientSecret.
	assert.NotContains(t, resolved.OBO.TokenURL, testSecretValue)
	assert.NotContains(t, resolved.OBO.ClientID, testSecretValue)
	assert.NotContains(t, resolved.OBO.Audience, testSecretValue)
}

// TestOBOResolveSecrets_Errors proves ResolveSecrets surfaces a missing secret or
// missing key as an error without echoing any secret value.
func TestOBOResolveSecrets_Errors(t *testing.T) {
	t.Parallel()

	t.Run("secret missing", func(t *testing.T) {
		t.Parallel()
		cr := validOBOCR()
		strategy, err := ConvertToStrategy(cr)
		require.NoError(t, err)

		k8sClient := fakeClientWithSecret(t) // no secret objects
		_, err = ResolveSecretsForStrategy(t.Context(), cr, k8sClient, testNamespace, strategy)
		require.Error(t, err)
	})

	t.Run("key missing in secret", func(t *testing.T) {
		t.Parallel()
		cr := validOBOCR()
		strategy, err := ConvertToStrategy(cr)
		require.NoError(t, err)

		client := fakeClientWithSecret(t, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testNamespace},
			Data:       map[string][]byte{"some-other-key": []byte(testSecretValue)},
		})
		_, err = ResolveSecretsForStrategy(t.Context(), cr, client, testNamespace, strategy)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), testSecretValue, "the error must not echo the secret value")
		assert.Empty(t, strategy.OBO.ClientSecret, "a failed resolve must not set the secret")
	})

	t.Run("empty secret value", func(t *testing.T) {
		t.Parallel()
		cr := validOBOCR()
		strategy, err := ConvertToStrategy(cr)
		require.NoError(t, err)

		client := fakeClientWithSecret(t, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testNamespace},
			Data:       map[string][]byte{testSecretKey: {}},
		})
		_, err = ResolveSecretsForStrategy(t.Context(), cr, client, testNamespace, strategy)
		require.ErrorContains(t, err, "is empty")
		assert.ErrorContains(t, err, testSecretName)
		assert.ErrorContains(t, err, testSecretKey)
		assert.Empty(t, strategy.OBO.ClientSecret)
	})
}

// TestOBOResolveSecrets_Guards covers the inputs ResolveSecrets rejects or
// passes through without contacting Kubernetes. The fake client holds no
// secrets, so any lookup would fail the passthrough row.
func TestOBOResolveSecrets_Guards(t *testing.T) {
	t.Parallel()

	withSecretEnv := func() *authtypes.BackendAuthStrategy {
		return &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeOBO,
			OBO:  &authtypes.OBOConfig{TokenURL: testTokenURL, ClientID: testClientID, ClientSecretEnv: "OBO_SECRET_ENV"},
		}
	}

	tests := []struct {
		name            string
		cr              func() *mcpv1beta1.MCPExternalAuthConfig
		strategy        func() *authtypes.BackendAuthStrategy
		wantErrContains string
	}{
		{
			name:            "nil strategy",
			cr:              validOBOCR,
			strategy:        func() *authtypes.BackendAuthStrategy { return nil },
			wantErrContains: msgStrategyNil,
		},
		{
			name: "strategy without OBO",
			cr:   validOBOCR,
			strategy: func() *authtypes.BackendAuthStrategy {
				return &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeOBO}
			},
			wantErrContains: msgStrategyNil,
		},
		{
			name: "external auth config without OBO",
			cr: func() *mcpv1beta1.MCPExternalAuthConfig {
				cr := validOBOCR()
				cr.Spec.OBO = nil
				return cr
			},
			strategy:        withSecretEnv,
			wantErrContains: msgOBONil,
		},
		{
			name: "no client secret reference passes the strategy through",
			cr: func() *mcpv1beta1.MCPExternalAuthConfig {
				cr := validOBOCR()
				cr.Spec.OBO.ClientSecretRef = nil
				return cr
			},
			strategy: withSecretEnv,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := tt.strategy()
			got, err := (&OBOConverter{}).ResolveSecrets(t.Context(), tt.cr(), fakeClientWithSecret(t), testNamespace, in)
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Same(t, in, got)
			assert.Equal(t, withSecretEnv(), got, "the strategy must be returned unchanged")
		})
	}
}

// fakeClientWithSecret builds a controller-runtime fake client seeded with the
// given secrets under a scheme that knows core/v1.
func fakeClientWithSecret(t *testing.T, secrets ...*corev1.Secret) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, s := range secrets {
		builder = builder.WithObjects(s)
	}
	return builder.Build()
}
