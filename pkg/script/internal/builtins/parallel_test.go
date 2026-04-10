// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package builtins

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/stacklok/toolhive/pkg/script/internal/core"
)

func TestNewParallel_OrderedResults(t *testing.T) {
	t.Parallel()

	globals := starlark.StringDict{
		"parallel": NewParallel(0),
	}

	result, err := core.Execute(`
results = parallel([
    lambda: "first",
    lambda: "second",
    lambda: "third",
])
return results
`, globals, 100_000)
	require.NoError(t, err)

	list, ok := result.Value.(*starlark.List)
	require.True(t, ok)
	require.Equal(t, 3, list.Len())
	require.Equal(t, starlark.String("first"), list.Index(0))
	require.Equal(t, starlark.String("second"), list.Index(1))
	require.Equal(t, starlark.String("third"), list.Index(2))
}

func TestNewParallel_EmptyList(t *testing.T) {
	t.Parallel()

	globals := starlark.StringDict{
		"parallel": NewParallel(0),
	}

	result, err := core.Execute(`return parallel([])`, globals, 100_000)
	require.NoError(t, err)

	list, ok := result.Value.(*starlark.List)
	require.True(t, ok)
	require.Equal(t, 0, list.Len())
}

func TestNewParallel_ErrorPropagation(t *testing.T) {
	t.Parallel()

	// Create a builtin that fails
	failing := starlark.NewBuiltin("failing", func(
		_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple,
	) (starlark.Value, error) {
		return nil, fmt.Errorf("intentional failure")
	})

	globals := starlark.StringDict{
		"parallel": NewParallel(0),
		"failing":  failing,
	}

	_, err := core.Execute(`return parallel([lambda: failing()])`, globals, 100_000)
	require.Error(t, err)
	require.Contains(t, err.Error(), "intentional failure")
}

func TestNewParallel_ConcurrencyLimit(t *testing.T) {
	t.Parallel()

	var maxConcurrent atomic.Int32
	var current atomic.Int32

	slow := starlark.NewBuiltin("slow", func(
		_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple,
	) (starlark.Value, error) {
		cur := current.Add(1)
		for {
			old := maxConcurrent.Load()
			if cur <= old {
				break
			}
			if maxConcurrent.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		current.Add(-1)
		return starlark.String("done"), nil
	})

	globals := starlark.StringDict{
		"parallel": NewParallel(2), // limit to 2 concurrent
		"slow":     slow,
	}

	done := make(chan struct{})
	go func() {
		result, err := core.Execute(`
return parallel([
    lambda: slow(),
    lambda: slow(),
    lambda: slow(),
    lambda: slow(),
])
`, globals, 1_000_000)
		require.NoError(t, err)

		list, ok := result.Value.(*starlark.List)
		require.True(t, ok)
		require.Equal(t, 4, list.Len())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for parallel execution")
	}

	require.LessOrEqual(t, maxConcurrent.Load(), int32(2),
		"should never exceed concurrency limit of 2")
}
