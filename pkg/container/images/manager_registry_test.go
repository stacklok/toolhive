// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubImageManager struct {
	name string
}

func (*stubImageManager) ImageExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func (*stubImageManager) PullImage(_ context.Context, _ string) error {
	return nil
}

func (*stubImageManager) BuildImage(_ context.Context, _, _ string) error {
	return nil
}

func TestManagerRegistry_RegisterAndLookup(t *testing.T) {
	t.Parallel()

	registry := NewManagerRegistry()
	registry.Register(&ManagerInfo{
		Name:     "kubernetes",
		Priority: 200,
		Initializer: func(_ context.Context) (ImageManager, error) {
			return &stubImageManager{name: "kubernetes"}, nil
		},
	})
	registry.Register(&ManagerInfo{
		Name:     "docker",
		Priority: 100,
		Initializer: func(_ context.Context) (ImageManager, error) {
			return &stubImageManager{name: "docker"}, nil
		},
	})
	registry.Register(&ManagerInfo{
		Name:     "apple",
		Priority: 150,
		Initializer: func(_ context.Context) (ImageManager, error) {
			return &stubImageManager{name: "apple"}, nil
		},
	})
	registry.Register(&ManagerInfo{
		Name:     "alpha",
		Priority: 150,
		Initializer: func(_ context.Context) (ImageManager, error) {
			return &stubImageManager{name: "alpha"}, nil
		},
	})

	ordered := registry.ByPriority()
	require.Len(t, ordered, 4)
	assert.Equal(t, "docker", ordered[0].Name)
	assert.Equal(t, "alpha", ordered[1].Name)
	assert.Equal(t, "apple", ordered[2].Name)
	assert.Equal(t, "kubernetes", ordered[3].Name)

	got := registry.Get("apple")
	require.NotNil(t, got)
	assert.Equal(t, 150, got.Priority)

	// Mutating the returned copy must not mutate registry state.
	got.Priority = 999
	assert.Equal(t, 150, registry.Get("apple").Priority)

	assert.Nil(t, registry.Get("nonexistent"))
}

func TestManagerRegistry_RegisterPanicsOnInvalidInput(t *testing.T) {
	t.Parallel()

	noopInit := func(_ context.Context) (ImageManager, error) {
		return &NoopImageManager{}, nil
	}

	tests := []struct {
		name  string
		setup func(reg *ManagerRegistry)
		info  *ManagerInfo
	}{
		{
			name: "nil info",
			info: nil,
		},
		{
			name: "empty name",
			info: &ManagerInfo{Priority: 100, Initializer: noopInit},
		},
		{
			name: "nil initializer",
			info: &ManagerInfo{Name: "apple", Priority: 100},
		},
		{
			name: "negative priority",
			info: &ManagerInfo{Name: "apple", Priority: -1, Initializer: noopInit},
		},
		{
			name: "duplicate registration",
			setup: func(reg *ManagerRegistry) {
				reg.Register(&ManagerInfo{Name: "apple", Priority: 150, Initializer: noopInit})
			},
			info: &ManagerInfo{Name: "apple", Priority: 150, Initializer: noopInit},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := NewManagerRegistry()
			if tc.setup != nil {
				tc.setup(reg)
			}
			assert.Panics(t, func() {
				reg.Register(tc.info)
			})
		})
	}
}

func TestNewImageManagerFromRegistry_ExplicitRuntimeNoSplitBrain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		runtimeName string
		setup       func(reg *ManagerRegistry)
		expectStub  string
		expectNoop  bool
	}{
		{
			name:        "selects explicitly requested lower-priority runtime",
			runtimeName: "apple",
			setup: func(reg *ManagerRegistry) {
				reg.Register(&ManagerInfo{
					Name:         "docker",
					Priority:     100,
					AutoDetector: func() bool { return true },
					Initializer: func(_ context.Context) (ImageManager, error) {
						return &stubImageManager{name: "docker"}, nil
					},
				})
				reg.Register(&ManagerInfo{
					Name:         "apple",
					Priority:     150,
					AutoDetector: func() bool { return true },
					Initializer: func(_ context.Context) (ImageManager, error) {
						return &stubImageManager{name: "apple"}, nil
					},
				})
			},
			expectStub: "apple",
		},
		{
			name:        "unknown explicit runtime returns noop instead of falling back",
			runtimeName: "unknown",
			setup: func(reg *ManagerRegistry) {
				reg.Register(&ManagerInfo{
					Name:         "docker",
					Priority:     100,
					AutoDetector: func() bool { return true },
					Initializer: func(_ context.Context) (ImageManager, error) {
						return &stubImageManager{name: "docker"}, nil
					},
				})
			},
			expectNoop: true,
		},
		{
			name:        "unavailable explicit runtime returns noop instead of falling back",
			runtimeName: "apple",
			setup: func(reg *ManagerRegistry) {
				reg.Register(&ManagerInfo{
					Name:         "docker",
					Priority:     100,
					AutoDetector: func() bool { return true },
					Initializer: func(_ context.Context) (ImageManager, error) {
						return &stubImageManager{name: "docker"}, nil
					},
				})
				reg.Register(&ManagerInfo{
					Name:         "apple",
					Priority:     150,
					AutoDetector: func() bool { return false },
					Initializer: func(_ context.Context) (ImageManager, error) {
						return &stubImageManager{name: "apple"}, nil
					},
				})
			},
			expectNoop: true,
		},
		{
			name:        "failing explicit runtime initializer returns noop instead of falling back",
			runtimeName: "apple",
			setup: func(reg *ManagerRegistry) {
				reg.Register(&ManagerInfo{
					Name:         "docker",
					Priority:     100,
					AutoDetector: func() bool { return true },
					Initializer: func(_ context.Context) (ImageManager, error) {
						return &stubImageManager{name: "docker"}, nil
					},
				})
				reg.Register(&ManagerInfo{
					Name:         "apple",
					Priority:     150,
					AutoDetector: func() bool { return true },
					Initializer: func(_ context.Context) (ImageManager, error) {
						return nil, errors.New("boom")
					},
				})
			},
			expectNoop: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := NewManagerRegistry()
			tc.setup(reg)

			mgr := NewImageManagerFromRegistry(context.Background(), reg, tc.runtimeName)
			if tc.expectNoop {
				_, ok := mgr.(*NoopImageManager)
				assert.True(t, ok, "expected *NoopImageManager, got %T", mgr)
				return
			}

			stub, ok := mgr.(*stubImageManager)
			require.True(t, ok, "expected *stubImageManager, got %T", mgr)
			assert.Equal(t, tc.expectStub, stub.name)
		})
	}
}

func TestNewImageManagerFromRegistry_AutoDetection(t *testing.T) {
	t.Parallel()

	t.Run("picks highest-priority available runtime", func(t *testing.T) {
		t.Parallel()
		reg := NewManagerRegistry()
		reg.Register(&ManagerInfo{
			Name:         "docker",
			Priority:     100,
			AutoDetector: func() bool { return false },
			Initializer: func(_ context.Context) (ImageManager, error) {
				return &stubImageManager{name: "docker"}, nil
			},
		})
		reg.Register(&ManagerInfo{
			Name:         "apple",
			Priority:     150,
			AutoDetector: func() bool { return true },
			Initializer: func(_ context.Context) (ImageManager, error) {
				return &stubImageManager{name: "apple"}, nil
			},
		})

		mgr := NewImageManagerFromRegistry(context.Background(), reg, "")
		stub, ok := mgr.(*stubImageManager)
		require.True(t, ok)
		assert.Equal(t, "apple", stub.name)
	})

	t.Run("skips failing initializer and tries next candidate", func(t *testing.T) {
		t.Parallel()
		reg := NewManagerRegistry()
		reg.Register(&ManagerInfo{
			Name:         "docker",
			Priority:     100,
			AutoDetector: func() bool { return true },
			Initializer: func(_ context.Context) (ImageManager, error) {
				return nil, errors.New("socket unavailable")
			},
		})
		reg.Register(&ManagerInfo{
			Name:         "apple",
			Priority:     150,
			AutoDetector: func() bool { return true },
			Initializer: func(_ context.Context) (ImageManager, error) {
				return &stubImageManager{name: "apple"}, nil
			},
		})

		mgr := NewImageManagerFromRegistry(context.Background(), reg, "")
		stub, ok := mgr.(*stubImageManager)
		require.True(t, ok)
		assert.Equal(t, "apple", stub.name)
	})

	t.Run("returns noop when no registered runtime is available", func(t *testing.T) {
		t.Parallel()
		reg := NewManagerRegistry()
		reg.Register(&ManagerInfo{
			Name:         "apple",
			Priority:     150,
			AutoDetector: func() bool { return false },
			Initializer: func(_ context.Context) (ImageManager, error) {
				return &stubImageManager{name: "apple"}, nil
			},
		})

		mgr := NewImageManagerFromRegistry(context.Background(), reg, "")
		_, ok := mgr.(*NoopImageManager)
		assert.True(t, ok)
	})
}

//nolint:paralleltest // mutates KUBERNETES_SERVICE_HOST via t.Setenv
func TestNewImageManagerFromRegistry_KubernetesShortCircuitsBeforeDocker(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")

	dockerProbed := false
	reg := NewManagerRegistry()
	reg.Register(&ManagerInfo{
		Name:     "docker",
		Priority: 100,
		AutoDetector: func() bool {
			dockerProbed = true
			return true
		},
		Initializer: func(_ context.Context) (ImageManager, error) {
			dockerProbed = true
			return &stubImageManager{name: "docker"}, nil
		},
	})
	reg.Register(&ManagerInfo{
		Name:     "kubernetes",
		Priority: 200,
		Initializer: func(_ context.Context) (ImageManager, error) {
			return &stubImageManager{name: "kubernetes"}, nil
		},
	})

	mgr := NewImageManagerFromRegistry(context.Background(), reg, "")
	assert.False(t, dockerProbed, "in-cluster resolution must not probe Docker before Kubernetes")
	stub, ok := mgr.(*stubImageManager)
	require.True(t, ok, "expected *stubImageManager, got %T", mgr)
	assert.Equal(t, "kubernetes", stub.name)
}
