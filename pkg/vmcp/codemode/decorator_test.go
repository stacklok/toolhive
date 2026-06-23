// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/script"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// fakeCore is a configurable core.VMCP for decorator tests. Only the methods the
// decorator overrides are implemented; the embedded nil interface satisfies the rest
// (and panics if the decorator ever calls one it should not).
type fakeCore struct {
	core.VMCP
	listTools  func(ctx context.Context, id *auth.Identity) ([]vmcp.Tool, error)
	lookupTool func(ctx context.Context, id *auth.Identity, name string) (*vmcp.Tool, error)
	callTool   func(ctx context.Context, id *auth.Identity, name string,
		args, meta map[string]any) (*vmcp.ToolCallResult, error)
}

func (f *fakeCore) ListTools(ctx context.Context, id *auth.Identity) ([]vmcp.Tool, error) {
	return f.listTools(ctx, id)
}

func (f *fakeCore) LookupTool(ctx context.Context, id *auth.Identity, name string) (*vmcp.Tool, error) {
	return f.lookupTool(ctx, id, name)
}

func (f *fakeCore) CallTool(
	ctx context.Context, id *auth.Identity, name string, args, meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	return f.callTool(ctx, id, name, args, meta)
}

// textResult builds a single-text-content tool result whose text is body (used by
// fakes to return JSON the script engine parses into structured data).
func textResult(body string) *vmcp.ToolCallResult {
	return &vmcp.ToolCallResult{Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: body}}}
}

// resultText returns the first text content of a tool result.
func resultText(t *testing.T, r *vmcp.ToolCallResult) string {
	t.Helper()
	require.NotNil(t, r)
	require.NotEmpty(t, r.Content)
	require.Equal(t, vmcp.ContentTypeText, r.Content[0].Type)
	return r.Content[0].Text
}

func echoBackend() *fakeCore {
	return &fakeCore{
		listTools: func(_ context.Context, _ *auth.Identity) ([]vmcp.Tool, error) {
			return []vmcp.Tool{
				{Name: "echo", Description: "Echoes its value back", BackendID: "b1"},
				{Name: "status", Description: "Reports service status", BackendID: "b1"},
			}, nil
		},
		callTool: func(_ context.Context, _ *auth.Identity, name string,
			args, _ map[string]any) (*vmcp.ToolCallResult, error) {
			switch name {
			case "echo":
				return textResult(`{"echoed":"` + args["value"].(string) + `"}`), nil
			case "status":
				return textResult(`{"status":"healthy"}`), nil
			default:
				return nil, errors.New("unknown tool")
			}
		},
	}
}

func TestNewDecorator_NilInnerPanics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "codemode: NewDecorator requires a non-nil inner VMCP", func() {
		NewDecorator(nil, nil)
	})
}

func TestListTools_AppendsVirtualTool(t *testing.T) {
	t.Parallel()
	d := NewDecorator(echoBackend(), nil)

	tools, err := d.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tools, 3)

	virtual := tools[len(tools)-1]
	assert.Equal(t, script.ExecuteToolScriptName, virtual.Name)
	assert.NotEmpty(t, virtual.InputSchema)
	// The dynamic description must enumerate the callable backend tools.
	assert.Contains(t, virtual.Description, "echo")
	assert.Contains(t, virtual.Description, "status")
	assert.Contains(t, virtual.Description, "parallel")
	// The virtual tool must not list itself as callable.
	assert.NotContains(t, virtual.Description, "- "+script.ExecuteToolScriptName)
}

func TestListTools_PropagatesError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom")
	d := NewDecorator(&fakeCore{
		listTools: func(_ context.Context, _ *auth.Identity) ([]vmcp.Tool, error) {
			return nil, sentinel
		},
	}, nil)

	_, err := d.ListTools(context.Background(), nil)
	require.ErrorIs(t, err, sentinel)
}

func TestLookupTool_VirtualAndDelegate(t *testing.T) {
	t.Parallel()
	inner := echoBackend()
	inner.lookupTool = func(_ context.Context, _ *auth.Identity, name string) (*vmcp.Tool, error) {
		return &vmcp.Tool{Name: name, BackendID: "b1"}, nil
	}
	d := NewDecorator(inner, nil)

	virtual, err := d.LookupTool(context.Background(), nil, script.ExecuteToolScriptName)
	require.NoError(t, err)
	assert.Equal(t, script.ExecuteToolScriptName, virtual.Name)
	assert.NotEmpty(t, virtual.InputSchema)

	other, err := d.LookupTool(context.Background(), nil, "echo")
	require.NoError(t, err)
	assert.Equal(t, "echo", other.Name)
	assert.Equal(t, "b1", other.BackendID)
}

func TestCallTool_Passthrough(t *testing.T) {
	t.Parallel()
	var (
		gotName string
		gotArgs map[string]any
		gotMeta map[string]any
	)
	inner := echoBackend()
	inner.callTool = func(_ context.Context, _ *auth.Identity, name string,
		args, meta map[string]any) (*vmcp.ToolCallResult, error) {
		gotName, gotArgs, gotMeta = name, args, meta
		return textResult("ok"), nil
	}
	d := NewDecorator(inner, nil)

	args := map[string]any{"value": "hi"}
	meta := map[string]any{"trace": "abc"}
	res, err := d.CallTool(context.Background(), nil, "echo", args, meta)
	require.NoError(t, err)
	assert.Equal(t, "ok", resultText(t, res))
	assert.Equal(t, "echo", gotName)
	assert.Equal(t, args, gotArgs)
	assert.Equal(t, meta, gotMeta, "client _meta must be forwarded on passthrough")
}

func TestCallTool_RunsScript(t *testing.T) {
	t.Parallel()
	identity := &auth.Identity{}
	var (
		innerIdentity *auth.Identity
		innerName     string
		innerMeta     map[string]any
		innerMetaSeen bool
	)
	inner := echoBackend()
	inner.callTool = func(_ context.Context, id *auth.Identity, name string,
		args, meta map[string]any) (*vmcp.ToolCallResult, error) {
		innerIdentity, innerName, innerMeta, innerMetaSeen = id, name, meta, true
		return textResult(`{"echoed":"` + args["value"].(string) + `"}`), nil
	}
	d := NewDecorator(inner, nil)

	args := map[string]any{"script": `
result = echo(value="hello")
return result["echoed"]
`}
	res, err := d.CallTool(context.Background(), identity, script.ExecuteToolScriptName, args, nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Contains(t, resultText(t, res), "hello")

	// Inner calls must carry the caller's identity (admission seam) and no client _meta.
	assert.Same(t, identity, innerIdentity)
	assert.Equal(t, "echo", innerName)
	assert.True(t, innerMetaSeen)
	assert.Nil(t, innerMeta)
}

func TestCallTool_DataArgsInjected(t *testing.T) {
	t.Parallel()
	d := NewDecorator(echoBackend(), nil)

	args := map[string]any{
		"script": `return echo(value=name)["echoed"]`,
		"data":   map[string]any{"name": "alice"},
	}
	res, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Contains(t, resultText(t, res), "alice")
}

func TestCallTool_MissingScriptArg(t *testing.T) {
	t.Parallel()
	d := NewDecorator(echoBackend(), nil)

	for _, args := range []map[string]any{
		{},             // absent
		{"script": ""}, // empty
		{"script": 42}, // wrong type
	} {
		res, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
		require.NoError(t, err, "script-arg errors surface as IsError, not transport errors")
		require.True(t, res.IsError)
		assert.Contains(t, resultText(t, res), "script")
	}
}

func TestCallTool_InvalidDataArg(t *testing.T) {
	t.Parallel()
	d := NewDecorator(echoBackend(), nil)

	args := map[string]any{"script": `return 1`, "data": "not-an-object"}
	res, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "data")
}

func TestCallTool_ScriptErrorIsNotTransportError(t *testing.T) {
	t.Parallel()
	d := NewDecorator(echoBackend(), nil)

	// Referencing an unbound name is a script runtime error; it must surface as an
	// IsError result so the agent can correct the script, not as a transport error.
	// The exact message is the engine's concern; assert the behavior, not the wording.
	args := map[string]any{"script": `return no_such_tool()`}
	res, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.NotEmpty(t, resultText(t, res))
}

func TestCallTool_VirtualToolNotCallableFromScript(t *testing.T) {
	t.Parallel()
	d := NewDecorator(echoBackend(), nil)

	// execute_tool_script is advertised but must never be bound as a callable, so a
	// script cannot recurse into it.
	args := map[string]any{"script": `return call_tool("execute_tool_script")`}
	res, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

func TestCallTool_InnerCallErrorSurfaces(t *testing.T) {
	t.Parallel()
	inner := echoBackend()
	inner.callTool = func(_ context.Context, _ *auth.Identity, _ string,
		_, _ map[string]any) (*vmcp.ToolCallResult, error) {
		return nil, vmcp.ErrAuthorizationFailed
	}
	d := NewDecorator(inner, nil)

	args := map[string]any{"script": `return echo(value="x")`}
	res, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

func TestCallTool_ToolCallTimeout(t *testing.T) {
	t.Parallel()
	inner := echoBackend()
	inner.callTool = func(ctx context.Context, _ *auth.Identity, _ string,
		_, _ map[string]any) (*vmcp.ToolCallResult, error) {
		// Block until the decorator-imposed per-call deadline fires.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d := NewDecorator(inner, &Config{ToolCallTimeout: 10 * time.Millisecond})

	args := map[string]any{"script": `return echo(value="x")`}
	res, err := d.CallTool(context.Background(), nil, script.ExecuteToolScriptName, args, nil)
	require.NoError(t, err)
	assert.True(t, res.IsError, "a tool call exceeding ToolCallTimeout must fail the script")
}
