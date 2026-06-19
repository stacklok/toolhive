// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1test_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
)

func TestNewMCPServer_Defaults(t *testing.T) {
	t.Parallel()

	m := v1beta1test.NewMCPServer("srv", "default")

	assert.Equal(t, "srv", m.Name)
	assert.Equal(t, "default", m.Namespace)
	assert.Equal(t, "test-image:latest", m.Spec.Image)
	assert.Equal(t, "stdio", m.Spec.Transport)
	assert.Equal(t, int32(8080), m.Spec.ProxyPort)
	assert.Nil(t, m.Spec.GroupRef)
}

func TestNewMCPServer_Options(t *testing.T) {
	t.Parallel()

	m := v1beta1test.NewMCPServer("srv", "toolhive",
		v1beta1test.WithImage("ghcr.io/example/mcp:1.2.3"),
		v1beta1test.WithMCPGroupRef("my-group"),
		v1beta1test.WithEnv(mcpv1beta1.EnvVar{Name: "FOO", Value: "bar"}),
	)

	assert.Equal(t, "ghcr.io/example/mcp:1.2.3", m.Spec.Image)
	assert.Equal(t, "my-group", m.Spec.GroupRef.Name)
	assert.Equal(t, "stdio", m.Spec.Transport, "untouched fields keep their defaults")
	assert.Len(t, m.Spec.Env, 1)
}

func TestNewMCPServer_RefOptions(t *testing.T) {
	t.Parallel()

	m := v1beta1test.NewMCPServer("srv", "ns",
		v1beta1test.WithToolConfigRef("tools"),
		v1beta1test.WithExternalAuthConfigRef("extauth"),
		v1beta1test.WithWebhookConfigRef("hook"),
		v1beta1test.WithTelemetryConfigRef("otel"),
	)

	assert.Equal(t, "tools", m.Spec.ToolConfigRef.Name)
	assert.Equal(t, "extauth", m.Spec.ExternalAuthConfigRef.Name)
	assert.Equal(t, "hook", m.Spec.WebhookConfigRef.Name)
	assert.Equal(t, "otel", m.Spec.TelemetryConfigRef.Name)
}

func TestNewMCPServer_TypedFieldOptions(t *testing.T) {
	t.Parallel()

	replicas := int32(3)
	m := v1beta1test.NewMCPServer("srv", "ns",
		v1beta1test.WithOIDCConfigRef("oidc", "aud"),
		v1beta1test.WithAuthzConfigRef("authz"),
		v1beta1test.WithMCPPort(9090),
		v1beta1test.WithReplicas(replicas),
		v1beta1test.WithStatus(mcpv1beta1.MCPServerStatus{Phase: mcpv1beta1.MCPServerPhaseReady}),
	)

	assert.Equal(t, "oidc", m.Spec.OIDCConfigRef.Name)
	assert.Equal(t, "aud", m.Spec.OIDCConfigRef.Audience)
	assert.Equal(t, "authz", m.Spec.AuthzConfigRef.Name)
	assert.Equal(t, int32(9090), m.Spec.MCPPort)
	require.NotNil(t, m.Spec.Replicas)
	assert.Equal(t, int32(3), *m.Spec.Replicas)
	assert.Equal(t, mcpv1beta1.MCPServerPhaseReady, m.Status.Phase)
}

func TestNewMCPServer_MutateRunsLast(t *testing.T) {
	t.Parallel()

	m := v1beta1test.NewMCPServer("srv", "ns",
		v1beta1test.WithImage("from-option"),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.Image = "from-mutate"
			m.Spec.Secrets = []mcpv1beta1.SecretRef{{Name: "s"}}
		}),
	)

	assert.Equal(t, "from-mutate", m.Spec.Image, "Mutate runs after typed options")
	assert.Len(t, m.Spec.Secrets, 1)
}
