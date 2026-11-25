// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
			got := rec.recorded()
			require.Len(t, got, 1, "observer must be called exactly once per script call")
			assert.Equal(t, tt.want, got[0].ToolCalls)
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
	got := rec.recorded()
	require.Len(t, got, 1)
	assert.Equal(t, 0, got[0].ToolCalls)
	assert.Equal(t, OutcomeError, got[0].Outcome)
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

// errorBackend wraps echoBackend so that the "fail" tool returns err (a plain
// error that does not wrap context errors).
func errorBackend(err error) *fakeCore {
	inner := echoBackend()
	base := inner.callTool
	inner.listTools = func(_ context.Context, _ *auth.Identity) ([]vmcp.Tool, error) {
		return []vmcp.Tool{{Name: "echo"}, {Name: "status"}, {Name: "fail"}}, nil
	}
	inner.callTool = func(ctx context.Context, id *auth.Identity, name string,
		args, meta map[string]any) (*vmcp.ToolCallResult, error) {
		if name == "fail" {
			return nil, err
		}
		return base(ctx, id, name, args, meta)
	}
	return inner
}

func TestCallTool_ObserverOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		inner             func() *fakeCore
		cfg               Config
		args              map[string]any
		wantOutcome       ScriptOutcome
		wantToolCalls     int
		wantParallelCalls int
		wantParallelTasks int
		wantSteps         bool
		wantIsError       bool
	}{
		{
			name:          "success",
			inner:         echoBackend,
			args:          map[string]any{"script": `a = echo(value="x")` + "\n" + `return status()`},
			wantOutcome:   OutcomeSuccess,
			wantToolCalls: 2,
			wantSteps:     true,
		},
		{
			name:  "success with parallel()",
			inner: echoBackend,
			args: map[string]any{"script": `
a = parallel([lambda i=i: echo(value=str(i)) for i in range(3)])
b = parallel([status, status])
return [a, b]`},
			wantOutcome:       OutcomeSuccess,
			wantToolCalls:     5,
			wantParallelCalls: 2,
			wantParallelTasks: 5,
			wantSteps:         true,
		},
		{
			name:        "runtime error",
			inner:       echoBackend,
			args:        map[string]any{"script": `return 1 // 0`},
			wantOutcome: OutcomeError,
			wantSteps:   true,
			wantIsError: true,
		},
		{
			name:          "inner tool error",
			inner:         func() *fakeCore { return errorBackend(errors.New("backend down")) },
			args:          map[string]any{"script": `echo(value="x")` + "\n" + `return fail()`},
			wantOutcome:   OutcomeError,
			wantToolCalls: 2,
			wantSteps:     true,
			wantIsError:   true,
		},
		{
			name:        "missing script argument",
			inner:       echoBackend,
			args:        map[string]any{},
			wantOutcome: OutcomeError,
			wantIsError: true,
		},
		{
			name:        "invalid data argument",
			inner:       echoBackend,
			args:        map[string]any{"script": `return 1`, "data": "nope"},
			wantOutcome: OutcomeError,
			wantIsError: true,
		},
		{
			name:  "step limit",
			inner: echoBackend,
			cfg:   Config{StepLimit: 100},
			args: map[string]any{"script": `
x = 0
for i in range(10000):
    x = x + 1
return x`},
			wantOutcome: OutcomeStepLimit,
			wantSteps:   true,
			wantIsError: true,
		},
		{
			name:  "step limit inside a parallel() child",
			inner: echoBackend,
			cfg:   Config{StepLimit: 200},
			args: map[string]any{"script": `
def spin():
    x = 0
    for i in range(10000):
        x = x + 1
    return x
return parallel([spin, spin])`},
			wantOutcome:       OutcomeStepLimit,
			wantParallelCalls: 1,
			wantParallelTasks: 2,
			wantSteps:         true,
			wantIsError:       true,
		},
		{
			name: "tool call timeout whose error does not wrap the deadline",
			inner: func() *fakeCore {
				inner := echoBackend()
				inner.callTool = func(ctx context.Context, _ *auth.Identity, _ string,
					_, _ map[string]any) (*vmcp.ToolCallResult, error) {
					<-ctx.Done()
					return nil, errors.New("backend request aborted")
				}
				return inner
			},
			cfg:           Config{ToolCallTimeout: 50 * time.Millisecond},
			args:          map[string]any{"script": `return echo(value="x")`},
			wantOutcome:   OutcomeTimeout,
			wantToolCalls: 1,
			wantSteps:     true,
			wantIsError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingObserver{}
			cfg := tt.cfg
			cfg.OnScript = rec.observe
			d := NewDecorator(tt.inner(), &cfg)

			res, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, tt.args, nil)
			require.NoError(t, err)
			require.NotNil(t, res)
			assert.Equal(t, tt.wantIsError, res.IsError)

			all := rec.recorded()
			require.Len(t, all, 1, "observer must be called exactly once per script call")
			got := all[0]
			assert.Equal(t, tt.wantOutcome, got.Outcome)
			assert.Equal(t, tt.wantToolCalls, got.ToolCalls)
			assert.Equal(t, tt.wantParallelCalls, got.ParallelCalls)
			assert.Equal(t, tt.wantParallelTasks, got.ParallelTasks)
			assert.Equal(t, tt.wantSteps, got.Steps > 0, "steps: %d", got.Steps)
			assert.Positive(t, got.Duration)
		})
	}
}

func TestClassifyOutcome(t *testing.T) {
	t.Parallel()

	live := context.Background()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)

	tests := []struct {
		name     string
		ctx      context.Context
		err      error
		timedOut bool
		want     ScriptOutcome
	}{
		{name: "no error", ctx: live, want: OutcomeSuccess},
		{name: "no error ignores timed-out flag", ctx: live, timedOut: true, want: OutcomeSuccess},
		{name: "step limit", ctx: live, err: errors.Join(errors.New("x"), script.ErrStepLimitExceeded), want: OutcomeStepLimit},
		{name: "step limit wins over expired deadline", ctx: expired, err: script.ErrStepLimitExceeded, want: OutcomeStepLimit},
		{name: "wrapped deadline", ctx: live, err: errors.Join(errors.New("x"), context.DeadlineExceeded), want: OutcomeTimeout},
		{name: "script deadline expired", ctx: expired, err: errors.New("x"), want: OutcomeTimeout},
		{name: "inner call timed out", ctx: live, err: errors.New("x"), timedOut: true, want: OutcomeTimeout},
		{name: "plain error", ctx: live, err: errors.New("x"), want: OutcomeError},
		{name: "cancelled is an error", ctx: live, err: context.Canceled, want: OutcomeError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, classifyOutcome(tt.ctx, tt.err, tt.timedOut))
		})
	}
}
