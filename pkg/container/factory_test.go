// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func noopInit(_ context.Context) (runtime.Runtime, error) {
	return nil, nil
}

func TestNewFactoryFromRegistry_SeedsFromRegistry(t *testing.T) {
	t.Parallel()

	reg := runtime.NewRegistry()
	reg.Register(&runtime.Info{Name: "test-a", Priority: 100, Initializer: noopInit})
	reg.Register(&runtime.Info{Name: "test-b", Priority: 200, Initializer: noopInit})

	f := NewFactoryFromRegistry(reg)
	runtimes := f.ListRuntimes()

	assert.Len(t, runtimes, 2)
	assert.Contains(t, runtimes, "test-a")
	assert.Contains(t, runtimes, "test-b")
}

func TestNewFactoryFromRegistry_EmptyRegistryYieldsEmptyFactory(t *testing.T) {
	t.Parallel()

	reg := runtime.NewRegistry()
	f := NewFactoryFromRegistry(reg)
	assert.Empty(t, f.ListRuntimes())
}

func TestAutoDetectRuntime_RespectsPriority(t *testing.T) {
	t.Parallel()

	reg := runtime.NewRegistry()
	reg.Register(&runtime.Info{
		Name: "high-prio", Priority: 300, Initializer: noopInit,
		AutoDetector: func() bool { return true },
	})
	reg.Register(&runtime.Info{
		Name: "low-prio", Priority: 50, Initializer: noopInit,
		AutoDetector: func() bool { return true },
	})
	reg.Register(&runtime.Info{
		Name: "mid-prio", Priority: 150, Initializer: noopInit,
		AutoDetector: func() bool { return true },
	})

	f := NewFactoryFromRegistry(reg)
	name, info := f.autoDetectRuntime()

	require.NotNil(t, info)
	assert.Equal(t, "low-prio", name)
}

func TestAutoDetectRuntime_SkipsUnavailable(t *testing.T) {
	t.Parallel()

	reg := runtime.NewRegistry()
	reg.Register(&runtime.Info{
		Name: "unavailable", Priority: 50, Initializer: noopInit,
		AutoDetector: func() bool { return false },
	})
	reg.Register(&runtime.Info{
		Name: "available", Priority: 100, Initializer: noopInit,
		AutoDetector: func() bool { return true },
	})

	f := NewFactoryFromRegistry(reg)
	name, info := f.autoDetectRuntime()

	require.NotNil(t, info)
	assert.Equal(t, "available", name)
}

func TestAutoDetectRuntime_NilDetectorMeansAvailable(t *testing.T) {
	t.Parallel()

	reg := runtime.NewRegistry()
	reg.Register(&runtime.Info{
		Name: "no-detector", Priority: 100, Initializer: noopInit,
		AutoDetector: nil,
	})

	f := NewFactoryFromRegistry(reg)
	name, info := f.autoDetectRuntime()

	require.NotNil(t, info)
	assert.Equal(t, "no-detector", name)
}

func TestAutoDetectRuntime_NoneAvailable(t *testing.T) {
	t.Parallel()

	reg := runtime.NewRegistry()
	reg.Register(&runtime.Info{
		Name: "unavailable", Priority: 100, Initializer: noopInit,
		AutoDetector: func() bool { return false },
	})

	f := NewFactoryFromRegistry(reg)
	name, info := f.autoDetectRuntime()

	assert.Nil(t, info)
	assert.Empty(t, name)
}
