// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/runner"
)

// withDefaultOBOHandler captures the package-level OBO handler and restores it
// on cleanup so that tests which call RegisterOBOHandler do not leak state to
// other tests in the package. The capture and restore both pass through
// oboMu so they participate in the same synchronization contract as
// production reads/writes.
func withDefaultOBOHandler(t *testing.T) {
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

func TestDefaultOBOHandler_ReturnsEnterpriseRequired(t *testing.T) {
	t.Parallel()

	// Read the default handler under the read lock so this test does not
	// data-race other tests that may run sequentially and write through
	// RegisterOBOHandler. The subtests below dispatch through this snapshot
	// rather than through the public wrappers so the assertion targets the
	// default OBOHandler value specifically.
	defaults := currentOBOHandler()

	t.Run("Validate", func(t *testing.T) {
		t.Parallel()
		err := defaults.Validate(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
	})

	t.Run("ApplyRunConfig", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		err := defaults.ApplyRunConfig(context.Background(), nil, "ns", nil, &opts)
		require.Error(t, err)
		assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
		assert.Empty(t, opts, "default handler must not mutate the options slice")
	})

	t.Run("SecretEnvVars", func(t *testing.T) {
		t.Parallel()
		envVars, err := defaults.SecretEnvVars(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
		assert.Nil(t, envVars, "default handler must return nil envVars on the error path")
	})
}

//nolint:paralleltest // Mutates package-level oboHandler; must not race other tests.
func TestOBOValidate_DispatchesThroughRegisteredHandler(t *testing.T) {
	withDefaultOBOHandler(t)

	// With the default handler, OBOValidate returns obo.ErrEnterpriseRequired.
	err := OBOValidate(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)

	// After registering a replacement, OBOValidate must dispatch through it.
	sentinel := errors.New("custom validate failure")
	RegisterOBOHandler(OBOHandler{
		Validate: func(*mcpv1beta1.MCPExternalAuthConfig) error { return sentinel },
		ApplyRunConfig: func(
			context.Context, client.Client, string,
			*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption,
		) error {
			return nil
		},
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
			return nil, nil
		},
	})

	err = OBOValidate(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "OBOValidate must dispatch through the registered handler")
}

//nolint:paralleltest // Mutates package-level oboHandler; must not race other tests.
func TestOBOSecretEnvVars_DispatchesThroughRegisteredHandler(t *testing.T) {
	withDefaultOBOHandler(t)

	// With the default handler, OBOSecretEnvVars returns obo.ErrEnterpriseRequired.
	envVars, err := OBOSecretEnvVars(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
	assert.Nil(t, envVars)

	// After registering a replacement, OBOSecretEnvVars must dispatch through it.
	expected := []corev1.EnvVar{{Name: "OBO_TEST", Value: "value"}}
	RegisterOBOHandler(OBOHandler{
		Validate: func(*mcpv1beta1.MCPExternalAuthConfig) error { return nil },
		ApplyRunConfig: func(
			context.Context, client.Client, string,
			*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption,
		) error {
			return nil
		},
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
			return expected, nil
		},
	})

	envVars, err = OBOSecretEnvVars(nil)
	require.NoError(t, err)
	assert.Equal(t, expected, envVars)
}

//nolint:paralleltest // Mutates package-level oboHandler; must not race other tests.
func TestOBOApplyRunConfig_DispatchesThroughRegisteredHandler(t *testing.T) {
	withDefaultOBOHandler(t)

	// With the default handler, OBOApplyRunConfig returns obo.ErrEnterpriseRequired
	// without mutating the options slice.
	var opts []runner.RunConfigBuilderOption
	err := OBOApplyRunConfig(context.Background(), nil, "ns", nil, &opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, obo.ErrEnterpriseRequired)
	assert.Empty(t, opts, "default handler must not mutate the options slice")

	// After registering a replacement, OBOApplyRunConfig must dispatch through it.
	sentinel := errors.New("custom apply failure")
	RegisterOBOHandler(OBOHandler{
		Validate: func(*mcpv1beta1.MCPExternalAuthConfig) error { return nil },
		ApplyRunConfig: func(
			context.Context, client.Client, string,
			*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption,
		) error {
			return sentinel
		},
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
			return nil, nil
		},
	})

	err = OBOApplyRunConfig(context.Background(), nil, "ns", nil, &opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "OBOApplyRunConfig must dispatch through the registered handler")
}

//nolint:paralleltest // Mutates package-level oboHandler; must not race other tests.
func TestRegisterOBOHandler_LastWriteWins(t *testing.T) {
	withDefaultOBOHandler(t)

	first := errors.New("first")
	second := errors.New("second")

	RegisterOBOHandler(OBOHandler{
		Validate: func(*mcpv1beta1.MCPExternalAuthConfig) error { return first },
		ApplyRunConfig: func(
			context.Context, client.Client, string,
			*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption,
		) error {
			return first
		},
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
			return nil, first
		},
	})
	RegisterOBOHandler(OBOHandler{
		Validate: func(*mcpv1beta1.MCPExternalAuthConfig) error { return second },
		ApplyRunConfig: func(
			context.Context, client.Client, string,
			*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption,
		) error {
			return second
		},
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
			return nil, second
		},
	})

	// The second registration must win on every method.
	assert.ErrorIs(t, OBOValidate(nil), second)
	_, err := OBOSecretEnvVars(nil)
	assert.ErrorIs(t, err, second)
	assert.ErrorIs(t,
		OBOApplyRunConfig(context.Background(), nil, "ns", nil, nil),
		second,
	)
}

//nolint:paralleltest // Mutates package-level oboHandler; must not race other tests.
func TestRegisterOBOHandler_PanicsOnNilField(t *testing.T) {
	withDefaultOBOHandler(t)

	validate := func(*mcpv1beta1.MCPExternalAuthConfig) error { return nil }
	applyRunConfig := func(
		context.Context, client.Client, string,
		*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption,
	) error {
		return nil
	}
	secretEnvVars := func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
		return nil, nil
	}

	tests := []struct {
		name    string
		handler OBOHandler
	}{
		{name: "Validate nil", handler: OBOHandler{ApplyRunConfig: applyRunConfig, SecretEnvVars: secretEnvVars}},
		{name: "ApplyRunConfig nil", handler: OBOHandler{Validate: validate, SecretEnvVars: secretEnvVars}},
		{name: "SecretEnvVars nil", handler: OBOHandler{Validate: validate, ApplyRunConfig: applyRunConfig}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() { RegisterOBOHandler(tt.handler) },
				"RegisterOBOHandler must panic when any function field is nil")
		})
	}
}

// TestAddExternalAuthConfigOptions_OBO proves the obo arm of the
// AddExternalAuthConfigOptions switch dispatches through the registered OBO
// handler. With the default handler the function must return an error wrapping
// obo.ErrEnterpriseRequired AND must not fall through to the default arm's
// "unsupported external auth type" message — external consumers pattern-match
// on errors.Is, and the dispatch arm's purpose is to keep the wired-but-
// disabled state distinguishable from a genuinely unknown type.
func TestAddExternalAuthConfigOptions_OBO(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	authCfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "obo-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  &mcpv1beta1.OBOConfig{},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authCfg).
		Build()

	var options []runner.RunConfigBuilderOption
	err := AddExternalAuthConfigOptions(
		t.Context(),
		fakeClient,
		"default",
		"server-name",
		&mcpv1beta1.ExternalAuthConfigRef{Name: authCfg.Name},
		nil, // oidcConfig — not required for OBO
		&options,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, obo.ErrEnterpriseRequired,
		"the default OBO handler returns obo.ErrEnterpriseRequired; the dispatch arm must propagate it")

	// Generic-error guard: the dispatch arm must short-circuit the default
	// arm's "unsupported external auth type: ..." path and must not leak the
	// middleware-map lookup's "unknown middleware type" path either.
	assert.NotContains(t, err.Error(), "unsupported external auth type")
	assert.NotContains(t, err.Error(), "unknown middleware type")
	assert.Empty(t, options, "default OBO handler must not append to the options slice")
}

// TestAddOBOSecretEnvVars covers the OBO-only secret-env dispatcher across every
// ExternalAuthType. Only obo contributes env vars (and only when a handler is
// registered); every other type, plus a nil ref, yields no env vars. A build
// with the default OBO handler treats obo as inert (no env vars, no error) so the
// deployment builder and drift-check paths stay byte-identical.
//
// Regression guard: newConfig populates the secret-bearing sub-spec for the
// non-obo types that have one (tokenExchange, bearerToken, headerInjection), so
// the "must stay empty" assertions below would fail if a future dev ever wired
// one of those types into AddOBOSecretEnvVars — the empty result is then a real
// invariant, not an artifact of an absent secret ref.
func TestAddOBOSecretEnvVars(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	const ns = "default"

	newConfig := func(name string, typ mcpv1beta1.ExternalAuthType) *mcpv1beta1.MCPExternalAuthConfig {
		cfg := &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       mcpv1beta1.MCPExternalAuthConfigSpec{Type: typ},
		}
		switch typ {
		case mcpv1beta1.ExternalAuthTypeTokenExchange:
			cfg.Spec.TokenExchange = &mcpv1beta1.TokenExchangeConfig{
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: name + "-secret", Key: "client-secret"},
			}
		case mcpv1beta1.ExternalAuthTypeBearerToken:
			cfg.Spec.BearerToken = &mcpv1beta1.BearerTokenConfig{
				TokenSecretRef: &mcpv1beta1.SecretKeyRef{Name: name + "-secret", Key: "token"},
			}
		case mcpv1beta1.ExternalAuthTypeHeaderInjection:
			cfg.Spec.HeaderInjection = &mcpv1beta1.HeaderInjectionConfig{
				ValueSecretRef: &mcpv1beta1.SecretKeyRef{Name: name + "-secret", Key: "value"},
			}
		case mcpv1beta1.ExternalAuthTypeOBO:
			cfg.Spec.OBO = &mcpv1beta1.OBOConfig{}
		case mcpv1beta1.ExternalAuthTypeUnauthenticated,
			mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			mcpv1beta1.ExternalAuthTypeAWSSts,
			mcpv1beta1.ExternalAuthTypeUpstreamInject,
			mcpv1beta1.ExternalAuthTypeXAA:
			// No secret-bearing sub-spec to populate for these types.
		}
		return cfg
	}

	tests := []struct {
		name    string
		seed    *mcpv1beta1.MCPExternalAuthConfig // object stored in the fake client (nil = none)
		ref     *mcpv1beta1.ExternalAuthConfigRef
		wantErr bool
	}{
		{
			name: "nil ref returns no env vars",
			ref:  nil,
		},
		{
			name:    "missing config returns error",
			ref:     &mcpv1beta1.ExternalAuthConfigRef{Name: "does-not-exist"},
			wantErr: true,
		},
		{
			name: "tokenExchange type contributes no env vars here",
			seed: newConfig("te", mcpv1beta1.ExternalAuthTypeTokenExchange),
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "te"},
		},
		{
			name: "bearerToken type contributes no env vars here",
			seed: newConfig("bt", mcpv1beta1.ExternalAuthTypeBearerToken),
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "bt"},
		},
		{
			name: "headerInjection type contributes no env vars here",
			seed: newConfig("hi", mcpv1beta1.ExternalAuthTypeHeaderInjection),
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "hi"},
		},
		{
			name: "unauthenticated type contributes no env vars here",
			seed: newConfig("un", mcpv1beta1.ExternalAuthTypeUnauthenticated),
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "un"},
		},
		{
			name: "embeddedAuthServer type contributes no env vars here",
			seed: newConfig("eas", mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer),
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "eas"},
		},
		{
			name: "awsSts type contributes no env vars here",
			seed: newConfig("aws", mcpv1beta1.ExternalAuthTypeAWSSts),
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "aws"},
		},
		{
			name: "upstreamInject type contributes no env vars here",
			seed: newConfig("ui", mcpv1beta1.ExternalAuthTypeUpstreamInject),
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "ui"},
		},
		{
			name: "obo with default handler is inert",
			seed: newConfig("obo", mcpv1beta1.ExternalAuthTypeOBO),
			ref:  &mcpv1beta1.ExternalAuthConfigRef{Name: "obo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.seed != nil {
				builder = builder.WithObjects(tt.seed)
			}

			envVars, err := AddOBOSecretEnvVars(t.Context(), builder.Build(), ns, tt.ref)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Empty(t, envVars, "only a registered OBO handler contributes env vars through this dispatcher")
		})
	}
}

// TestAddOBOSecretEnvVars_OBOHandlerRegistered proves the obo arm forwards a
// registered handler's env vars verbatim and propagates a genuine
// (non-enterprise) handler error, while still swallowing ErrEnterpriseRequired.
//
//nolint:paralleltest // Mutates package-level oboHandler; must not race other tests.
func TestAddOBOSecretEnvVars_OBOHandlerRegistered(t *testing.T) {
	withDefaultOBOHandler(t)

	scheme := testutil.NewScheme(t)

	const ns = "default"
	authCfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "obo-config", Namespace: ns},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  &mcpv1beta1.OBOConfig{},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(authCfg).Build()
	ref := &mcpv1beta1.ExternalAuthConfigRef{Name: authCfg.Name}

	noopValidate := func(*mcpv1beta1.MCPExternalAuthConfig) error { return nil }
	noopApply := func(
		context.Context, client.Client, string,
		*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption,
	) error {
		return nil
	}

	// A registered handler's env vars are forwarded verbatim.
	expected := []corev1.EnvVar{{
		Name: "OBO_CLIENT_SECRET",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "obo-secret"},
				Key:                  "token",
			},
		},
	}}
	RegisterOBOHandler(OBOHandler{
		Validate:       noopValidate,
		ApplyRunConfig: noopApply,
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
			return expected, nil
		},
	})

	envVars, err := AddOBOSecretEnvVars(t.Context(), fakeClient, ns, ref)
	require.NoError(t, err)
	assert.Equal(t, expected, envVars, "the dispatcher must forward the handler's env vars verbatim")

	// A genuine (non-enterprise) handler error propagates to the caller.
	sentinel := errors.New("secret not found")
	RegisterOBOHandler(OBOHandler{
		Validate:       noopValidate,
		ApplyRunConfig: noopApply,
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
			return nil, sentinel
		},
	})

	envVars, err = AddOBOSecretEnvVars(t.Context(), fakeClient, ns, ref)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "a genuine handler error must propagate, not be swallowed")
	assert.Nil(t, envVars)
}
