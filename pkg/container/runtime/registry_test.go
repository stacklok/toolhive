// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noopInitializer(_ context.Context) (Runtime, error) {
	return nil, nil
}

func TestRegistry_Register(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		info      *Info
		panicMsg  string
		wantPanic bool
	}{
		{
			name:      "nil info panics",
			info:      nil,
			wantPanic: true,
			panicMsg:  "runtime info cannot be nil",
		},
		{
			name:      "empty name panics",
			info:      &Info{Name: "", Initializer: noopInitializer},
			wantPanic: true,
			panicMsg:  "runtime name cannot be empty",
		},
		{
			name:      "nil initializer panics",
			info:      &Info{Name: "test", Initializer: nil},
			wantPanic: true,
			panicMsg:  "runtime initializer cannot be nil",
		},
		{
			name:      "negative priority panics",
			info:      &Info{Name: "test", Priority: -1, Initializer: noopInitializer},
			wantPanic: true,
			panicMsg:  "runtime priority must be non-negative",
		},
		{
			name: "valid registration succeeds",
			info: &Info{
				Name:        "test-rt",
				Priority:    100,
				Initializer: noopInitializer,
			},
			wantPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := NewRegistry()

			if tt.wantPanic {
				assert.PanicsWithValue(t, tt.panicMsg, func() {
					reg.Register(tt.info)
				})
			} else {
				require.NotPanics(t, func() {
					reg.Register(tt.info)
				})
				got := reg.Get(tt.info.Name)
				require.NotNil(t, got)
				assert.Equal(t, tt.info.Name, got.Name)
				assert.Equal(t, tt.info.Priority, got.Priority)
			}
		})
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	info := &Info{
		Name:        "dup-rt",
		Priority:    100,
		Initializer: noopInitializer,
	}
	reg.Register(info)

	assert.PanicsWithValue(t, "runtime already registered: dup-rt", func() {
		reg.Register(info)
	})
}

func TestRegistry_Get_NotFound(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	assert.Nil(t, reg.Get("nonexistent"))
}

func TestRegistry_IsRegistered(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	assert.False(t, reg.IsRegistered("check-rt"))

	reg.Register(&Info{
		Name:        "check-rt",
		Priority:    100,
		Initializer: noopInitializer,
	})

	assert.True(t, reg.IsRegistered("check-rt"))
}

func TestRegistry_All(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	assert.Empty(t, reg.All())

	reg.Register(&Info{Name: "a", Priority: 200, Initializer: noopInitializer})
	reg.Register(&Info{Name: "b", Priority: 100, Initializer: noopInitializer})

	runtimes := reg.All()
	assert.Len(t, runtimes, 2)

	names := make(map[string]bool)
	for _, r := range runtimes {
		names[r.Name] = true
	}
	assert.True(t, names["a"])
	assert.True(t, names["b"])
}

func TestRegistry_ByPriority(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	reg.Register(&Info{Name: "high", Priority: 300, Initializer: noopInitializer})
	reg.Register(&Info{Name: "low", Priority: 50, Initializer: noopInitializer})
	reg.Register(&Info{Name: "mid", Priority: 150, Initializer: noopInitializer})

	ordered := reg.ByPriority()
	require.Len(t, ordered, 3)
	assert.Equal(t, "low", ordered[0].Name)
	assert.Equal(t, "mid", ordered[1].Name)
	assert.Equal(t, "high", ordered[2].Name)
}

func TestRegistry_ByPriority_SamePrioritySortedByName(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()

	reg.Register(&Info{Name: "charlie", Priority: 100, Initializer: noopInitializer})
	reg.Register(&Info{Name: "alpha", Priority: 100, Initializer: noopInitializer})
	reg.Register(&Info{Name: "bravo", Priority: 100, Initializer: noopInitializer})

	ordered := reg.ByPriority()
	require.Len(t, ordered, 3)
	assert.Equal(t, "alpha", ordered[0].Name)
	assert.Equal(t, "bravo", ordered[1].Name)
	assert.Equal(t, "charlie", ordered[2].Name)
}

func TestRegistry_Isolation(t *testing.T) {
	t.Parallel()

	reg1 := NewRegistry()
	reg2 := NewRegistry()

	reg1.Register(&Info{Name: "only-in-reg1", Priority: 100, Initializer: noopInitializer})
	reg2.Register(&Info{Name: "only-in-reg2", Priority: 100, Initializer: noopInitializer})

	assert.True(t, reg1.IsRegistered("only-in-reg1"))
	assert.False(t, reg1.IsRegistered("only-in-reg2"))

	assert.True(t, reg2.IsRegistered("only-in-reg2"))
	assert.False(t, reg2.IsRegistered("only-in-reg1"))
}
