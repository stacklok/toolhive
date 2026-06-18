// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1test_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
		v1beta1test.WithGroupRef("my-group"),
		v1beta1test.WithEnv(mcpv1beta1.EnvVar{Name: "FOO", Value: "bar"}),
	)

	assert.Equal(t, "ghcr.io/example/mcp:1.2.3", m.Spec.Image)
	assert.Equal(t, "my-group", m.Spec.GroupRef.Name)
	assert.Equal(t, "stdio", m.Spec.Transport, "untouched fields keep their defaults")
	assert.Len(t, m.Spec.Env, 1)
}
