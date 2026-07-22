// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// UntrustedSentinelPrefix is the literal prefix injected as the value of each
// egressPolicy.providers[].credentialEnvName env var for untrusted workloads. The value
// is a pure compatibility shim (ADR-0001): servers that refuse to start tokenless still
// boot; the broker ignores the sentinel and injects the real credential.
const UntrustedSentinelPrefix = "thv-untrusted-sentinel:"

// InjectUntrustedSentinels appends one literal env var per declared
// EgressPolicy provider CredentialEnvName to the container named containerName in
// the pod-template patch. Literal values deliberately use Value (never ValueFrom)
// so the injected result passes the Wave-0 gate ValidateNoSecretEnvForUntrusted.
//
// It is idempotent: an existing env var whose value is already the exact sentinel
// literal for the same provider is left untouched (re-reconcile is a no-op).
// It returns an error (a terminal spec error at the call site) when
// CredentialEnvName collides with an existing env var of a different value (the
// user already set that name in spec.env, or the builder did via WithSecrets), or
// when two providers declare the same non-empty CredentialEnvName. When the patch
// has no container named containerName yet (no user template and no spec.secrets —
// the expected untrusted shape) the container is created; the proxy runner
// strategic-merges the patch over its own base container.
//
// A nil podTemplate or nil policy is a no-op. The caller's podTemplate is mutated
// (it is the freshly-built patch owned by deploymentForMCPServer).
func InjectUntrustedSentinels(
	podTemplate *corev1.PodTemplateSpec, containerName string, policy *mcpv1beta1.EgressPolicy,
) error {
	if podTemplate == nil || policy == nil {
		return nil
	}

	providers := namedProviders(policy)
	if err := checkDuplicateCredentialEnvNames(providers); err != nil {
		return err
	}
	if len(providers) == 0 {
		return nil
	}

	for i := range podTemplate.Spec.Containers {
		container := &podTemplate.Spec.Containers[i]
		if container.Name != containerName {
			continue
		}
		return injectSentinelsIntoContainer(container, containerName, providers)
	}

	// The backend container is absent from the patch (no user template named it and
	// no spec.secrets generated it — the expected untrusted shape). Create the
	// patch container with exactly the sentinel env vars: the proxy runner
	// strategic-merges the patch over its own base mcp container.
	env := make([]corev1.EnvVar, 0, len(providers))
	for _, p := range providers {
		env = append(env, corev1.EnvVar{Name: p.CredentialEnvName, Value: UntrustedSentinelPrefix + p.Provider})
	}
	podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, corev1.Container{
		Name: containerName,
		Env:  env,
	})
	return nil
}

// namedProviders returns the providers that declare a CredentialEnvName, in order.
func namedProviders(policy *mcpv1beta1.EgressPolicy) []mcpv1beta1.ProviderEgress {
	var out []mcpv1beta1.ProviderEgress
	for _, p := range policy.Providers {
		if p.CredentialEnvName != "" {
			out = append(out, p)
		}
	}
	return out
}

// checkDuplicateCredentialEnvNames rejects two providers fighting over the same env name.
func checkDuplicateCredentialEnvNames(providers []mcpv1beta1.ProviderEgress) error {
	seen := map[string]string{}
	for _, p := range providers {
		if prev, dup := seen[p.CredentialEnvName]; dup {
			return fmt.Errorf(
				"untrusted workload: egressPolicy providers %q and %q both declare credentialEnvName %q; "+
					"each provider must use a distinct credentialEnvName",
				prev, p.Provider, p.CredentialEnvName)
		}
		seen[p.CredentialEnvName] = p.Provider
	}
	return nil
}

// injectSentinelsIntoContainer merges the sentinel literals into one existing container.
func injectSentinelsIntoContainer(
	container *corev1.Container, containerName string, providers []mcpv1beta1.ProviderEgress,
) error {
	existing := map[string]int{}
	for j, env := range container.Env {
		existing[env.Name] = j
	}
	for _, p := range providers {
		sentinel := UntrustedSentinelPrefix + p.Provider
		if j, found := existing[p.CredentialEnvName]; found {
			if container.Env[j].Value != sentinel || container.Env[j].ValueFrom != nil {
				return fmt.Errorf(
					"untrusted workload: env var %q on container %q already exists with a different value; "+
						"egressPolicy provider %q cannot inject its sentinel over user-declared env",
					p.CredentialEnvName, containerName, p.Provider)
			}
			// Already the exact sentinel literal: idempotent re-reconcile, no duplicate.
			continue
		}
		container.Env = append(container.Env, corev1.EnvVar{Name: p.CredentialEnvName, Value: sentinel})
		existing[p.CredentialEnvName] = len(container.Env) - 1
	}
	return nil
}

// ValidateUntrustedSentinelForgery rejects literal env values on the container named
// containerName that start with the reserved UntrustedSentinelPrefix — a user forging
// a sentinel in spec.env (or a raw podTemplateSpec) would confuse operators reading pod
// specs into believing the broker manages a credential the broker has never heard of.
// The prefix is reserved for operator-injected values only. Sentinel injection runs
// before this check at the call site, so operator-injected literals are intentionally
// exempt (the check scopes to what the user declared, which is already validated by
// InjectUntrustedSentinels for collisions).
//
// A nil podTemplate is a no-op.
func ValidateUntrustedSentinelForgery(
	podTemplate *corev1.PodTemplateSpec, containerName string, policy *mcpv1beta1.EgressPolicy,
) error {
	if podTemplate == nil {
		return nil
	}
	for _, container := range podTemplate.Spec.Containers {
		if container.Name != containerName {
			continue
		}
		for _, env := range container.Env {
			if !strings.HasPrefix(env.Value, UntrustedSentinelPrefix) {
				continue
			}
			if isOperatorInjectedSentinel(env, policy) {
				continue
			}
			return fmt.Errorf(
				"untrusted workload: env var %q on container %q uses the reserved sentinel prefix %q; "+
					"sentinel values are injected by the operator from egressPolicy and must not be declared by users",
				env.Name, containerName, UntrustedSentinelPrefix)
		}
	}
	return nil
}

// isOperatorInjectedSentinel reports whether env is exactly the sentinel literal a
// declared provider would inject for credentialEnvName env.Name.
func isOperatorInjectedSentinel(env corev1.EnvVar, policy *mcpv1beta1.EgressPolicy) bool {
	if policy == nil {
		return false
	}
	for _, p := range policy.Providers {
		if p.CredentialEnvName == env.Name && env.Value == UntrustedSentinelPrefix+p.Provider {
			return true
		}
	}
	return false
}
