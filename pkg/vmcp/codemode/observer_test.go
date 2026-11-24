// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/script"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// recordingObserver collects every ScriptStats it receives.
type recordingObserver struct {
	mu    sync.Mutex
	stats []ScriptStats
}

func (r *recordingObserver) observe(_ context.Context, s ScriptStats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats = append(r.stats, s)
}

func (r *recordingObserver) recorded() []ScriptStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ScriptStats(nil), r.stats...)
}

func TestCallTool_ObserverCountsToolCalls(t *testing.T) {
	t.Parallel()

	failing := func() *fakeCore {
		inner := echoBackend()
		inner.callTool = func(context.Context, *auth.Identity, string, map[string]any, map[string]any) (*vmcp.ToolCallResult, error) {
			return nil, errors.New("backend down")
		}
		return inner
	}

	tests := []struct {
		name  string
		inner func() *fakeCore
		cfg   Config
		args  map[string]any
		want  int
	}{
		{
			name:  "sequential calls",
			inner: echoBackend,
			args:  map[string]any{"script": `a = echo(value="x")` + "\n" + `return [a, status()]`},
			want:  2,
		},
		{
			name:  "calls under parallel() count individually",
			inner: echoBackend,
			cfg:   Config{ParallelMax: 4},
			args: map[string]any{"script": `
results = parallel([lambda i=i: echo(value=str(i)) for i in range(25)])
return len(results)`},
			want: 25,
		},
		{
			name:  "a failed call still counts",
			inner: failing,
			args:  map[string]any{"script": `return echo(value="x")`},
			want:  1,
		},
		{
			name:  "script with no tool calls",
			inner: echoBackend,
			args:  map[string]any{"script": `return 1`},
			want:  0,
		},
		{
			name:  "invalid arguments",
			inner: echoBackend,
			args:  map[string]any{},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingObserver{}
			cfg := tt.cfg
			cfg.OnScript = rec.observe
			d := NewDecorator(tt.inner(), &cfg)

			_, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, tt.args, nil)
			require.NoError(t, err)
			assert.Equal(t, []ScriptStats{{ToolCalls: tt.want}}, rec.recorded(),
				"observer must be called exactly once per script call")
		})
	}
}

func TestCallTool_ObserverOnTransportError(t *testing.T) {
	t.Parallel()

	rec := &recordingObserver{}
	d := NewDecorator(collidingBackend(), &Config{OnScript: rec.observe})

	_, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName,
		map[string]any{"script": `return 1`}, nil)
	require.ErrorIs(t, err, ErrReservedToolName)
	assert.Equal(t, []ScriptStats{{ToolCalls: 0}}, rec.recorded())
}

func TestCallTool_ObserverNotCalledForOtherTools(t *testing.T) {
	t.Parallel()

	rec := &recordingObserver{}
	d := NewDecorator(echoBackend(), &Config{OnScript: rec.observe})

	_, err := d.CallTool(context.Background(), nil, "status", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, rec.recorded())
}

func TestCallTool_ObserverDoesNotChangeResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		observer ScriptObserver
	}{
		{name: "no-op observer", observer: func(context.Context, ScriptStats) {}},
		{name: "panicking observer", observer: func(context.Context, ScriptStats) { panic("observer bug") }},
	}

	args := map[string]any{"script": `return echo(value="same")`}
	want, err := NewDecorator(echoBackend(), nil).CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := NewDecorator(echoBackend(), &Config{OnScript: tt.observer})
			var got *vmcp.ToolCallResult
			var callErr error
			require.NotPanics(t, func() {
				got, callErr = d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
			})
			require.NoError(t, callErr)
			assert.Equal(t, want, got)
		})
	}
}
