// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestPodIdentityResolver(t *testing.T) {
	t.Parallel()

	full := map[string]string{
		egressbroker.EnvIssuer:     "https://issuer.example.com",
		egressbroker.EnvSubjectRaw: "user-123",
		egressbroker.EnvSessionID:  "session-abc",
		egressbroker.EnvMCPServer:  "github-mcp",
	}

	t.Run("valid env resolves identity", func(t *testing.T) {
		t.Parallel()
		r, err := egressbroker.NewPodIdentityResolver(envFrom(full))
		require.NoError(t, err)
		id := r.PodIdentity()
		assert.Equal(t, "https://issuer.example.com", id.Issuer)
		assert.Equal(t, "user-123", id.Subject)
		assert.Equal(t, "session-abc", id.SessionID)
		assert.Equal(t, "github-mcp", id.MCPServer)
	})

	t.Run("nil env lookup → error", func(t *testing.T) {
		t.Parallel()
		_, err := egressbroker.NewPodIdentityResolver(nil)
		require.Error(t, err)
	})

	t.Run("missing/empty downward-API env fails closed at startup", func(t *testing.T) {
		t.Parallel()
		for _, missing := range []string{
			egressbroker.EnvIssuer,
			egressbroker.EnvSubjectRaw,
			egressbroker.EnvSessionID,
			egressbroker.EnvMCPServer,
		} {
			for _, variant := range map[string]string{"missing": "", "whitespace-only": "   "} {
				values := map[string]string{}
				for k, v := range full {
					values[k] = v
				}
				values[missing] = variant
				_, err := egressbroker.NewPodIdentityResolver(envFrom(values))
				require.Error(t, err, "%s %s must fail construction", missing, variant)
				assert.Contains(t, err.Error(), missing)
			}
		}
	})
}

func TestResourceNaming(t *testing.T) {
	t.Parallel()

	t.Run("generation-qualified names share the base prefix and are deterministic", func(t *testing.T) {
		t.Parallel()
		gen := egressbroker.CAGeneration([]byte("cert-pem-bytes"))
		require.Len(t, gen, 16)

		n := egressbroker.ResourceNamesFor("github-mcp", gen)
		assert.Equal(t, "github-mcp-bump-ca-"+gen, n.CASecret)
		assert.Equal(t, "github-mcp-bump-ca-bundle-"+gen, n.CABundle)
		assert.Equal(t, "github-mcp-egress-policy", n.EgressPolicy)
		assert.Equal(t, gen, n.Generation)

		// Same cert → same generation → same names (idempotent reconcile).
		assert.Equal(t, n, egressbroker.ResourceNamesFor("github-mcp", egressbroker.CAGeneration([]byte("cert-pem-bytes"))))
		// Different cert → different generation.
		assert.NotEqual(t, gen, egressbroker.CAGeneration([]byte("other-cert")))
	})

	t.Run("TrimGeneration round-trips generation-qualified names", func(t *testing.T) {
		t.Parallel()
		base := egressbroker.BaseCASecretName("github-mcp")
		gen, ok := egressbroker.TrimGeneration("github-mcp-bump-ca-0123456789abcdef", base)
		require.True(t, ok)
		assert.Equal(t, "0123456789abcdef", gen)

		_, ok = egressbroker.TrimGeneration("github-mcp-bump-ca", base)
		assert.False(t, ok, "base name without generation must not parse")
		_, ok = egressbroker.TrimGeneration("github-mcp-bump-ca-", base)
		assert.False(t, ok, "empty generation must not parse")
		_, ok = egressbroker.TrimGeneration("other-bump-ca-abc", base)
		assert.False(t, ok, "foreign name must not parse")
	})

	t.Run("identity contract: every env maps to a distinct annotation", func(t *testing.T) {
		t.Parallel()
		seen := map[string]string{}
		for env, annotation := range egressbroker.EnvToAnnotation {
			got, ok := egressbroker.AnnotationForEnv(env)
			require.True(t, ok)
			assert.Equal(t, annotation, got)
			_, dup := seen[annotation]
			assert.False(t, dup, "annotation %s mapped twice", annotation)
			seen[annotation] = env
			assert.Contains(t, egressbroker.AnnotationFieldRef(annotation), annotation)
		}
		_, ok := egressbroker.AnnotationForEnv("THV_UNTRUSTED_NOPE")
		assert.False(t, ok)
	})
}
