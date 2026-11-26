// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package composer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestWorkflow_StepOutputFromContentWithoutStructuredContent pins that a tool
// step whose backend result carries only Content (no StructuredContent) still
// exposes that content as step output: the backend client no longer synthesizes
// StructuredContent, so the workflow engine's own fallback is what makes
// {{.steps.<id>.output.text}} work, for plain tool steps and forEach inner steps.
func TestWorkflow_StepOutputFromContentWithoutStructuredContent(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	expectContentOnlyCall := func(toolName string, args map[string]any, text string) {
		target := &vmcp.BackendTarget{WorkloadID: "test-backend", BaseURL: "http://test:8080"}
		te.Router.EXPECT().RouteTool(gomock.Any(), toolName).Return(target, nil)
		te.Backend.EXPECT().CallTool(gomock.Any(), target, toolName, args, gomock.Any(), gomock.Any()).
			Return(&vmcp.ToolCallResult{
				Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: text}},
			}, nil)
	}

	expectContentOnlyCall("docs.fetch", map[string]any{}, "release notes")
	expectContentOnlyCall("notes.save", map[string]any{"body": "release notes"}, "saved")
	te.expectToolCall("pkg.list", map[string]any{}, map[string]any{"names": json.RawMessage(`["a","b"]`)})
	expectContentOnlyCall("pkg.check", map[string]any{"name": "a"}, "a: ok")
	expectContentOnlyCall("pkg.check", map[string]any{"name": "b"}, "b: ok")

	def := simpleWorkflow("content-only-output",
		toolStep("fetch", "docs.fetch", map[string]any{}),
		toolStepWithDeps("save", "notes.save",
			map[string]any{"body": "{{.steps.fetch.output.text}}"}, []string{"fetch"}),
		toolStep("list", "pkg.list", map[string]any{}),
		WorkflowStep{
			ID:          "check",
			Type:        StepTypeForEach,
			Collection:  "{{json .steps.list.output.names}}",
			MaxParallel: 1,
			InnerStep: &WorkflowStep{
				ID:        "inner",
				Type:      StepTypeTool,
				Tool:      "pkg.check",
				Arguments: map[string]any{"name": "{{.forEach.item}}"},
			},
			DependsOn: []string{"list"},
		},
	)

	result, err := execute(t, te.Engine, def, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, WorkflowStatusCompleted, result.Status)

	assert.Equal(t, map[string]any{"text": "release notes"}, result.Steps["fetch"].Output)

	iterations, ok := result.Steps["check"].Output["iterations"].([]any)
	require.True(t, ok)
	require.Len(t, iterations, 2)
	for i, want := range []string{"a: ok", "b: ok"} {
		iter, ok := iterations[i].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, map[string]any{"text": want}, iter["output"])
	}
}
