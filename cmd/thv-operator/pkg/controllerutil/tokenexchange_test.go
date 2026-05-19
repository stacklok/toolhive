// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/runner"
)

// withDefaultOBOHandler captures the package-level OBO handler and restores it
// on cleanup so that tests which call RegisterOBOHandler do not leak state to
// other tests in the package.
func withDefaultOBOHandler(t *testing.T) {
	t.Helper()
	original := oboHandler
	t.Cleanup(func() { oboHandler = original })
}

func TestErrEnterpriseRequired_IsSentinel(t *testing.T) {
	t.Parallel()

	// Wrapping the sentinel and unwrapping with errors.Is must work both
	// directly and through fmt.Errorf("...: %w", ...).
	wrapped := fmt.Errorf("outer wrap: %w", ErrEnterpriseRequired)
	assert.ErrorIs(t, wrapped, ErrEnterpriseRequired)
}

func TestDefaultOBOHandler_ReturnsEnterpriseRequired(t *testing.T) {
	t.Parallel()

	t.Run("Validate", func(t *testing.T) {
		t.Parallel()
		err := oboHandler.Validate(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrEnterpriseRequired)
	})

	t.Run("ApplyRunConfig", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		err := oboHandler.ApplyRunConfig(context.Background(), nil, "ns", nil, &opts)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrEnterpriseRequired)
		assert.Empty(t, opts, "default handler must not mutate the options slice")
	})

	t.Run("SecretEnvVars", func(t *testing.T) {
		t.Parallel()
		envVars, err := oboHandler.SecretEnvVars(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrEnterpriseRequired)
		assert.Nil(t, envVars, "default handler must return nil envVars on the error path")
	})
}

//nolint:paralleltest // Mutates package-level oboHandler; must not race other tests.
func TestOBOValidate_DispatchesThroughRegisteredHandler(t *testing.T) {
	withDefaultOBOHandler(t)

	// With the default handler, OBOValidate returns ErrEnterpriseRequired.
	err := OBOValidate(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEnterpriseRequired)

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

	// With the default handler, OBOSecretEnvVars returns ErrEnterpriseRequired.
	envVars, err := OBOSecretEnvVars(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEnterpriseRequired)
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
		oboHandler.ApplyRunConfig(context.Background(), nil, "ns", nil, nil),
		second,
	)
}
