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
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
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
