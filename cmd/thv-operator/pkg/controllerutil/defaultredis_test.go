// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

func TestReadDefaultRedisConfig_NotSet(t *testing.T) {
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME", "")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY", "")

	got := ctrlutil.ReadDefaultRedisConfig()
	assert.Nil(t, got)
}

func TestReadDefaultRedisConfig_AddrOnly(t *testing.T) {
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "myredis:6379")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME", "")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY", "")

	got := ctrlutil.ReadDefaultRedisConfig()
	require.NotNil(t, got)
	assert.Equal(t, "myredis:6379", got.Addr)
	assert.Empty(t, got.SecretName)
	assert.Empty(t, got.SecretKey)
}

func TestReadDefaultRedisConfig_WithSecret(t *testing.T) {
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_ADDR", "myredis:6379")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_NAME", "redis-creds")
	t.Setenv("TOOLHIVE_DEFAULT_REDIS_SECRET_KEY", "my-password")

	got := ctrlutil.ReadDefaultRedisConfig()
	require.NotNil(t, got)
	assert.Equal(t, "myredis:6379", got.Addr)
	assert.Equal(t, "redis-creds", got.SecretName)
	assert.Equal(t, "my-password", got.SecretKey)
}
