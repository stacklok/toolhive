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

func TestCreateWithRuntimeName_UnavailableDoesNotCallInitializer(t *testing.T) {
	t.Parallel()

	initializerCalled := false
	reg := runtime.NewRegistry()
	reg.Register(&runtime.Info{
		Name:     "unavailable",
		Priority: 100,
		AutoDetector: func() bool {
			return false
		},
		Initializer: func(_ context.Context) (runtime.Runtime, error) {
			initializerCalled = true
			return nil, nil
		},
	})

	f := NewFactoryFromRegistry(reg)
	rt, err := f.CreateWithRuntimeName(context.Background(), "unavailable")

	require.Error(t, err)
	assert.Nil(t, rt)
	assert.False(t, initializerCalled, "Initializer must not be called when AutoDetector reports unavailable")
	assert.Contains(t, err.Error(), `runtime "unavailable" is not available`)
}

func TestFormatNoRuntimeAvailableError_DarwinOnlyAppleHint(t *testing.T) {
	t.Parallel()

	darwinErr := formatNoRuntimeAvailableError("darwin", []string{"docker", "apple", "kubernetes"})
	assert.Contains(t, darwinErr.Error(), "Apple container")
	assert.Contains(t, darwinErr.Error(), "container system start")

	for _, goos := range []string{"linux", "windows"} {
		otherErr := formatNoRuntimeAvailableError(goos, []string{"docker", "kubernetes"})
		assert.NotContains(t, otherErr.Error(), "Apple container")
		assert.NotContains(t, otherErr.Error(), "container system start")
		assert.Contains(t, otherErr.Error(), "Docker-compatible")
		assert.Contains(t, otherErr.Error(), "Kubernetes")
	}
}
