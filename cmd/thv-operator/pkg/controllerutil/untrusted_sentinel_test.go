// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// mcpPodTemplate builds a pod template patch with a single backend (mcp) container
// carrying the given env vars.
func mcpPodTemplate(env ...corev1.EnvVar) *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "mcp", Env: env}},
	}}
}

// envByName returns the env var with the given name, or nil.
func envByName(envs []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == name {
			return &envs[i]
		}
	}
	return nil
}

func TestInjectUntrustedSentinels(t *testing.T) {
	t.Parallel()

	provider := func(name, envName string) mcpv1beta1.ProviderEgress {
		return mcpv1beta1.ProviderEgress{
			Provider:          name,
			AllowedHosts:      []string{"api.example.com"},
			CredentialEnvName: envName,
		}
	}

	tests := []struct {
		name        string
		podTemplate *corev1.PodTemplateSpec
		policy      *mcpv1beta1.EgressPolicy
		wantEnv     map[string]string // expected literal env on the mcp container afterwards
		wantAbsent  []string          // env names that must NOT be present afterwards
		wantErr     string
	}{
		{
			name:        "single provider appends one literal sentinel env var",
			podTemplate: mcpPodTemplate(),
			policy:      &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "GITHUB_TOKEN")}},
			wantEnv:     map[string]string{"GITHUB_TOKEN": "thv-untrusted-sentinel:github"},
		},
		{
			name:        "provider with empty credentialEnvName injects nothing",
			podTemplate: mcpPodTemplate(),
			policy:      &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "")}},
			wantAbsent:  []string{"GITHUB_TOKEN"},
		},
		{
			name: "collision with existing env name of a different value is an error",
			podTemplate: mcpPodTemplate(
				corev1.EnvVar{Name: "GITHUB_TOKEN", Value: "user-declared"},
			),
			policy:  &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "GITHUB_TOKEN")}},
			wantErr: `env var "GITHUB_TOKEN" on container "mcp" already exists with a different value`,
		},
		{
			name: "collision with existing ValueFrom env is an error",
			podTemplate: mcpPodTemplate(
				corev1.EnvVar{Name: "GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{Key: "token"},
				}},
			),
			policy:  &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "GITHUB_TOKEN")}},
			wantErr: `env var "GITHUB_TOKEN" on container "mcp" already exists with a different value`,
		},
		{
			name: "existing exact sentinel literal is idempotent: no duplicate, no error",
			podTemplate: mcpPodTemplate(
				corev1.EnvVar{Name: "GITHUB_TOKEN", Value: "thv-untrusted-sentinel:github"},
			),
			policy:  &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "GITHUB_TOKEN")}},
			wantEnv: map[string]string{"GITHUB_TOKEN": "thv-untrusted-sentinel:github"},
		},
		{
			name:        "two providers sharing a credentialEnvName is an error",
			podTemplate: mcpPodTemplate(),
			policy: &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{
				provider("github", "API_TOKEN"),
				provider("gitlab", "API_TOKEN"),
			}},
			wantErr: `both declare credentialEnvName "API_TOKEN"`,
		},
		{
			name:        "two distinct providers each get their sentinel",
			podTemplate: mcpPodTemplate(),
			policy: &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{
				provider("github", "GITHUB_TOKEN"),
				provider("google", "GOOGLE_TOKEN"),
			}},
			wantEnv: map[string]string{
				"GITHUB_TOKEN": "thv-untrusted-sentinel:github",
				"GOOGLE_TOKEN": "thv-untrusted-sentinel:google",
			},
		},
		{
			name:        "nil policy is a no-op",
			podTemplate: mcpPodTemplate(),
			policy:      nil,
		},
		{
			name:        "nil pod template is a no-op",
			podTemplate: nil,
			policy:      &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "GITHUB_TOKEN")}},
		},
		{
			name: "absent backend container is created carrying the sentinels",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "not-mcp"}},
			}},
			policy:  &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "GITHUB_TOKEN")}},
			wantEnv: map[string]string{"GITHUB_TOKEN": "thv-untrusted-sentinel:github"},
		},
		{
			name:        "empty pod template gets a new backend container with the sentinel",
			podTemplate: &corev1.PodTemplateSpec{},
			policy:      &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "GITHUB_TOKEN")}},
			wantEnv:     map[string]string{"GITHUB_TOKEN": "thv-untrusted-sentinel:github"},
		},
		{
			name: "env on other containers is not a collision",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sidecar", Env: []corev1.EnvVar{{Name: "GITHUB_TOKEN", Value: "sidecar-owned"}}},
					{Name: "mcp"},
				},
			}},
			policy:  &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{provider("github", "GITHUB_TOKEN")}},
			wantEnv: map[string]string{"GITHUB_TOKEN": "thv-untrusted-sentinel:github"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := InjectUntrustedSentinels(tt.podTemplate, "mcp", tt.policy)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)

			if tt.podTemplate == nil {
				return
			}
			var mcpEnv []corev1.EnvVar
			for _, c := range tt.podTemplate.Spec.Containers {
				if c.Name == "mcp" {
					mcpEnv = c.Env
				}
			}
			for name, wantValue := range tt.wantEnv {
				env := envByName(mcpEnv, name)
				require.NotNil(t, env, "expected env var %q on mcp container", name)
				assert.Equal(t, wantValue, env.Value)
				assert.Nil(t, env.ValueFrom, "sentinel env must be a literal Value, never ValueFrom")
				// Idempotency pins exactly one entry per name.
				count := 0
				for _, e := range mcpEnv {
					if e.Name == name {
						count++
					}
				}
				assert.Equal(t, 1, count, "env var %q must appear exactly once", name)
			}
			for _, name := range tt.wantAbsent {
				assert.Nil(t, envByName(mcpEnv, name), "env var %q must not be injected", name)
			}
		})
	}
}

// TestInjectUntrustedSentinels_ComposesWithGate proves the injected literals pass the
// Wave-0 gate: both functions compose at the deploymentForMCPServer seam.
func TestInjectUntrustedSentinels_ComposesWithGate(t *testing.T) {
	t.Parallel()

	podTemplate := mcpPodTemplate()
	policy := &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{{
		Provider:          "github",
		AllowedHosts:      []string{"api.github.com"},
		CredentialEnvName: "GITHUB_TOKEN",
	}}}

	require.NoError(t, InjectUntrustedSentinels(podTemplate, "mcp", policy))
	require.NoError(t, ValidateNoSecretEnvForUntrusted(podTemplate, "mcp", true),
		"injected literal sentinels must pass the Wave-0 untrusted env gate")

	// Re-running the injection (re-reconcile) keeps the gate passing and adds nothing.
	envBefore := podTemplate.Spec.Containers[0].Env
	require.NoError(t, InjectUntrustedSentinels(podTemplate, "mcp", policy))
	assert.Equal(t, envBefore, podTemplate.Spec.Containers[0].Env, "re-injection must be a no-op")
	require.NoError(t, ValidateNoSecretEnvForUntrusted(podTemplate, "mcp", true))
}

func TestValidateUntrustedSentinelForgery(t *testing.T) {
	t.Parallel()

	policy := &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{{
		Provider:          "github",
		AllowedHosts:      []string{"api.github.com"},
		CredentialEnvName: "GITHUB_TOKEN",
	}}}

	tests := []struct {
		name        string
		podTemplate *corev1.PodTemplateSpec
		policy      *mcpv1beta1.EgressPolicy
		wantErr     string
	}{
		{
			name: "user env value with the reserved sentinel prefix is rejected",
			podTemplate: mcpPodTemplate(
				corev1.EnvVar{Name: "MY_TOKEN", Value: "thv-untrusted-sentinel:fake"},
			),
			policy:  policy,
			wantErr: `env var "MY_TOKEN" on container "mcp" uses the reserved sentinel prefix`,
		},
		{
			name: "operator-injected sentinel for a declared provider is exempt",
			podTemplate: mcpPodTemplate(
				corev1.EnvVar{Name: "GITHUB_TOKEN", Value: "thv-untrusted-sentinel:github"},
			),
			policy: policy,
		},
		{
			name: "sentinel literal under a different env name is still forgery",
			podTemplate: mcpPodTemplate(
				corev1.EnvVar{Name: "SOME_OTHER_NAME", Value: "thv-untrusted-sentinel:github"},
			),
			policy:  policy,
			wantErr: `env var "SOME_OTHER_NAME" on container "mcp" uses the reserved sentinel prefix`,
		},
		{
			name: "sentinel literal for an undeclared provider is forgery",
			podTemplate: mcpPodTemplate(
				corev1.EnvVar{Name: "GITLAB_TOKEN", Value: "thv-untrusted-sentinel:gitlab"},
			),
			policy:  policy,
			wantErr: `env var "GITLAB_TOKEN" on container "mcp" uses the reserved sentinel prefix`,
		},
		{
			name: "ordinary literal env values pass",
			podTemplate: mcpPodTemplate(
				corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"},
				corev1.EnvVar{Name: "NOT_A_SENTINEL", Value: "thv-untrusted-but-not-the-prefix"},
			),
			policy: policy,
		},
		{
			name: "sentinel-prefixed env on a non-backend container is out of scope",
			podTemplate: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sidecar", Env: []corev1.EnvVar{{Name: "X", Value: "thv-untrusted-sentinel:fake"}}},
					{Name: "mcp"},
				},
			}},
			policy: policy,
		},
		{
			name:        "nil pod template is a no-op",
			podTemplate: nil,
			policy:      policy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateUntrustedSentinelForgery(tt.podTemplate, "mcp", tt.policy)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestSentinelInjectionThenForgeryCheck exercises the exact call-site ordering:
// inject first, then the forgery check must accept the operator's own literals.
func TestSentinelInjectionThenForgeryCheck(t *testing.T) {
	t.Parallel()

	podTemplate := mcpPodTemplate(corev1.EnvVar{Name: "LOG_LEVEL", Value: "info"})
	policy := &mcpv1beta1.EgressPolicy{Providers: []mcpv1beta1.ProviderEgress{{
		Provider:          "github",
		AllowedHosts:      []string{"api.github.com"},
		CredentialEnvName: "GITHUB_TOKEN",
	}}}

	require.NoError(t, InjectUntrustedSentinels(podTemplate, "mcp", policy))
	require.NoError(t, ValidateUntrustedSentinelForgery(podTemplate, "mcp", policy),
		"forgery check must not flag operator-injected sentinels")
}
