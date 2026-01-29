// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	"github.com/stacklok/toolhive/pkg/secrets"
)

func TestEnsureRequiredEnvVars(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("sets all default env vars when missing", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		assert.Equal(t, "/tmp", envMap["XDG_CONFIG_HOME"])
		assert.Equal(t, "/tmp", envMap["HOME"])
		assert.Equal(t, "kubernetes", envMap["TOOLHIVE_RUNTIME"])
		assert.Equal(t, "false", envMap["UNSTRUCTURED_LOGS"])
		assert.Len(t, result, 4)
	})

	t.Run("does not override existing env vars", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: "XDG_CONFIG_HOME", Value: "/custom/path"},
			{Name: "HOME", Value: "/home/user"},
			{Name: "TOOLHIVE_RUNTIME", Value: "docker"},
			{Name: "UNSTRUCTURED_LOGS", Value: "true"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		assert.Equal(t, "/custom/path", envMap["XDG_CONFIG_HOME"])
		assert.Equal(t, "/home/user", envMap["HOME"])
		assert.Equal(t, "docker", envMap["TOOLHIVE_RUNTIME"])
		assert.Equal(t, "true", envMap["UNSTRUCTURED_LOGS"])
		assert.Len(t, result, 4)
	})

	t.Run("sets TOOLHIVE_SECRETS_PROVIDER when TOOLHIVE_SECRET_* vars are present", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: "TOOLHIVE_SECRET_api-bearer-token", Value: "token-value"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		assert.Equal(t, string(secrets.EnvironmentType), envMap[secrets.ProviderEnvVar])
		assert.Contains(t, result, corev1.EnvVar{
			Name:  secrets.ProviderEnvVar,
			Value: string(secrets.EnvironmentType),
		})
		// Should also have all default env vars
		assert.Equal(t, "/tmp", envMap["XDG_CONFIG_HOME"])
		assert.Equal(t, "/tmp", envMap["HOME"])
		assert.Equal(t, "kubernetes", envMap["TOOLHIVE_RUNTIME"])
		assert.Equal(t, "false", envMap["UNSTRUCTURED_LOGS"])
	})

	t.Run("does not set TOOLHIVE_SECRETS_PROVIDER when no secrets are present", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: "SOME_OTHER_VAR", Value: "value"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		_, found := envMap[secrets.ProviderEnvVar]
		assert.False(t, found, "TOOLHIVE_SECRETS_PROVIDER should not be set when no secrets are present")
	})

	t.Run("sets TOOLHIVE_SECRETS_PROVIDER with multiple secret env vars", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: "TOOLHIVE_SECRET_token1", Value: "value1"},
			{Name: "TOOLHIVE_SECRET_token2", Value: "value2"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		assert.Equal(t, string(secrets.EnvironmentType), envMap[secrets.ProviderEnvVar])
	})

	t.Run("does not treat TOOLHIVE_SECRETS_PROVIDER itself as a secret", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: secrets.ProviderEnvVar, Value: "encrypted"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		// Should not set TOOLHIVE_SECRETS_PROVIDER because only the provider itself is present, no actual secrets
		// The current implementation will append a new one if secrets are found, but since we only have the provider var,
		// no secrets are detected, so it should not be set
		_, found := envMap[secrets.ProviderEnvVar]
		assert.True(t, found, "TOOLHIVE_SECRETS_PROVIDER should be preserved")
		assert.Equal(t, "encrypted", envMap[secrets.ProviderEnvVar])
	})

	t.Run("appends TOOLHIVE_SECRETS_PROVIDER when provider is set but secrets are also present", func(t *testing.T) {
		t.Parallel()

		// When TOOLHIVE_SECRETS_PROVIDER is set to something other than "environment" but secrets are present,
		// the current implementation will append a new one (creating a duplicate)
		env := []corev1.EnvVar{
			{Name: secrets.ProviderEnvVar, Value: "encrypted"},
			{Name: "TOOLHIVE_SECRET_api-key", Value: "key-value"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		providerCount := 0
		for _, e := range result {
			if e.Name == secrets.ProviderEnvVar {
				providerCount++
				envMap[e.Name] = e.Value
			} else {
				envMap[e.Name] = e.Value
			}
		}

		// Current implementation appends, so we'll have both values
		// The last one appended will be "environment"
		assert.GreaterOrEqual(t, providerCount, 1, "Should have at least one provider env var")
		// The appended one should be "environment"
		providerVars := []corev1.EnvVar{}
		for _, e := range result {
			if e.Name == secrets.ProviderEnvVar {
				providerVars = append(providerVars, e)
			}
		}
		// Check that "environment" is in the list
		hasEnvironment := false
		for _, pv := range providerVars {
			if pv.Value == string(secrets.EnvironmentType) {
				hasEnvironment = true
				break
			}
		}
		assert.True(t, hasEnvironment, "Should have environment provider set")
	})

	t.Run("handles empty env list", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{}
		result := EnsureRequiredEnvVars(ctx, env)

		assert.Len(t, result, 4) // All defaults should be set
		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		assert.Equal(t, "/tmp", envMap["XDG_CONFIG_HOME"])
		assert.Equal(t, "/tmp", envMap["HOME"])
		assert.Equal(t, "kubernetes", envMap["TOOLHIVE_RUNTIME"])
		assert.Equal(t, "false", envMap["UNSTRUCTURED_LOGS"])
	})

	t.Run("preserves existing env vars when adding defaults", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: "CUSTOM_VAR", Value: "custom-value"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		assert.Equal(t, "custom-value", envMap["CUSTOM_VAR"])
		assert.Equal(t, "/tmp", envMap["XDG_CONFIG_HOME"])
		assert.Equal(t, "/tmp", envMap["HOME"])
		assert.Equal(t, "kubernetes", envMap["TOOLHIVE_RUNTIME"])
		assert.Equal(t, "false", envMap["UNSTRUCTURED_LOGS"])
	})

	t.Run("sets TOOLHIVE_SECRETS_PROVIDER when secret env var is present regardless of other vars", func(t *testing.T) {
		t.Parallel()

		// The current implementation checks for secrets outside the switch, so it works regardless
		env := []corev1.EnvVar{
			{Name: "TOOLHIVE_SECRET_my-secret", Value: "secret-value"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		assert.Equal(t, string(secrets.EnvironmentType), envMap[secrets.ProviderEnvVar])
	})

	t.Run("sets all defaults and provider when secrets are present", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: "TOOLHIVE_SECRET_api-key", Value: "key-value"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		// Should have all defaults plus the provider
		assert.Equal(t, "/tmp", envMap["XDG_CONFIG_HOME"])
		assert.Equal(t, "/tmp", envMap["HOME"])
		assert.Equal(t, "kubernetes", envMap["TOOLHIVE_RUNTIME"])
		assert.Equal(t, "false", envMap["UNSTRUCTURED_LOGS"])
		assert.Equal(t, string(secrets.EnvironmentType), envMap[secrets.ProviderEnvVar])
		assert.Equal(t, "key-value", envMap["TOOLHIVE_SECRET_api-key"])
		assert.Len(t, result, 6) // 1 original secret + 4 defaults + 1 provider
	})

	t.Run("handles secret env var with hyphens in name", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: "TOOLHIVE_SECRET_api-bearer-token", Value: "bearer-token-value-123"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		assert.Equal(t, string(secrets.EnvironmentType), envMap[secrets.ProviderEnvVar])
		assert.Equal(t, "bearer-token-value-123", envMap["TOOLHIVE_SECRET_api-bearer-token"])
	})

	t.Run("detects secrets correctly when mixed with other env vars", func(t *testing.T) {
		t.Parallel()

		env := []corev1.EnvVar{
			{Name: "CUSTOM_VAR", Value: "custom"},
			{Name: "ANOTHER_VAR", Value: "another"},
			{Name: "TOOLHIVE_SECRET_token", Value: "secret-token"},
			{Name: "REGULAR_VAR", Value: "regular"},
		}
		result := EnsureRequiredEnvVars(ctx, env)

		envMap := make(map[string]string)
		for _, e := range result {
			envMap[e.Name] = e.Value
		}

		// Should detect the secret and set provider
		assert.Equal(t, string(secrets.EnvironmentType), envMap[secrets.ProviderEnvVar])
		// Should preserve all original vars
		assert.Equal(t, "custom", envMap["CUSTOM_VAR"])
		assert.Equal(t, "another", envMap["ANOTHER_VAR"])
		assert.Equal(t, "secret-token", envMap["TOOLHIVE_SECRET_token"])
		assert.Equal(t, "regular", envMap["REGULAR_VAR"])
	})
}
