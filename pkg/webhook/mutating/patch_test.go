// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mutating

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		patch   []JSONPatchOp
		wantErr bool
	}{
		{
			name:    "valid add op",
			patch:   []JSONPatchOp{{Op: "add", Path: "/mcp_request/params/arguments/key", Value: json.RawMessage(`"value"`)}},
			wantErr: false,
		},
		{
			name:    "valid remove op",
			patch:   []JSONPatchOp{{Op: "remove", Path: "/mcp_request/params/arguments/key"}},
			wantErr: false,
		},
		{
			name:    "valid replace op",
			patch:   []JSONPatchOp{{Op: "replace", Path: "/mcp_request/params/arguments/key", Value: json.RawMessage(`"new"`)}},
			wantErr: false,
		},
		{
			name:    "valid copy op",
			patch:   []JSONPatchOp{{Op: "copy", Path: "/mcp_request/params/dest", From: "/mcp_request/params/src"}},
			wantErr: false,
		},
		{
			name:    "valid move op",
			patch:   []JSONPatchOp{{Op: "move", Path: "/mcp_request/params/dest", From: "/mcp_request/params/src"}},
			wantErr: false,
		},
		{
			name:    "valid test op",
			patch:   []JSONPatchOp{{Op: "test", Path: "/mcp_request/params/key", Value: json.RawMessage(`"expected"`)}},
			wantErr: false,
		},
		{
			name:    "invalid op name",
			patch:   []JSONPatchOp{{Op: "delete", Path: "/mcp_request/params/key"}},
			wantErr: true,
		},
		{
			name:    "missing path",
			patch:   []JSONPatchOp{{Op: "add", Value: json.RawMessage(`"value"`)}},
			wantErr: true,
		},
		{
			name:    "copy missing from",
			patch:   []JSONPatchOp{{Op: "copy", Path: "/mcp_request/params/dest"}},
			wantErr: true,
		},
		{
			name:    "move missing from",
			patch:   []JSONPatchOp{{Op: "move", Path: "/mcp_request/params/dest"}},
			wantErr: true,
		},
		{
			name:    "empty patch",
			patch:   []JSONPatchOp{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePatch(tt.patch)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestIsPatchScopedToMCPRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		patch []JSONPatchOp
		want  bool
	}{
		{
			name:  "scoped path",
			patch: []JSONPatchOp{{Op: "add", Path: "/mcp_request/params/key", Value: json.RawMessage(`"v"`)}},
			want:  true,
		},
		{
			name: "multiple scoped paths",
			patch: []JSONPatchOp{
				{Op: "add", Path: "/mcp_request/params/key1", Value: json.RawMessage(`"v1"`)},
				{Op: "add", Path: "/mcp_request/params/key2", Value: json.RawMessage(`"v2"`)},
			},
			want: true,
		},
		{
			name:  "path outside mcp_request (principal)",
			patch: []JSONPatchOp{{Op: "replace", Path: "/principal/email", Value: json.RawMessage(`"hacked@evil.com"`)}},
			want:  false,
		},
		{
			name:  "path outside mcp_request (context)",
			patch: []JSONPatchOp{{Op: "add", Path: "/context/extra", Value: json.RawMessage(`"x"`)}},
			want:  false,
		},
		{
			name: "mixed: some scoped, some not",
			patch: []JSONPatchOp{
				{Op: "add", Path: "/mcp_request/params/key", Value: json.RawMessage(`"v"`)},
				{Op: "replace", Path: "/principal/sub", Value: json.RawMessage(`"attacker"`)},
			},
			want: false,
		},
		{
			name:  "copy from outside mcp_request",
			patch: []JSONPatchOp{{Op: "copy", Path: "/mcp_request/params/dest", From: "/principal/email"}},
			want:  false,
		},
		{
			name:  "copy both scoped",
			patch: []JSONPatchOp{{Op: "copy", Path: "/mcp_request/params/dest", From: "/mcp_request/params/src"}},
			want:  true,
		},
		{
			name:  "empty patch",
			patch: []JSONPatchOp{},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := IsPatchScopedToMCPRequest(tt.patch)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestApplyPatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		original string
		patch    []JSONPatchOp
		check    func(t *testing.T, result []byte)
		wantErr  bool
	}{
		{
			name:     "add field",
			original: `{"mcp_request":{"params":{"arguments":{"query":"SELECT *"}}}}`,
			patch: []JSONPatchOp{
				{Op: "add", Path: "/mcp_request/params/arguments/audit_user", Value: json.RawMessage(`"user@example.com"`)},
			},
			check: func(t *testing.T, result []byte) {
				t.Helper()
				var doc map[string]interface{}
				require.NoError(t, json.Unmarshal(result, &doc))
				mcpReq := doc["mcp_request"].(map[string]interface{})
				params := mcpReq["params"].(map[string]interface{})
				args := params["arguments"].(map[string]interface{})
				assert.Equal(t, "user@example.com", args["audit_user"])
				assert.Equal(t, "SELECT *", args["query"])
			},
		},
		{
			name:     "remove field",
			original: `{"mcp_request":{"params":{"arguments":{"query":"SELECT *","secret":"pass"}}}}`,
			patch: []JSONPatchOp{
				{Op: "remove", Path: "/mcp_request/params/arguments/secret"},
			},
			check: func(t *testing.T, result []byte) {
				t.Helper()
				var doc map[string]interface{}
				require.NoError(t, json.Unmarshal(result, &doc))
				mcpReq := doc["mcp_request"].(map[string]interface{})
				params := mcpReq["params"].(map[string]interface{})
				args := params["arguments"].(map[string]interface{})
				_, hasSecret := args["secret"]
				assert.False(t, hasSecret)
				assert.Equal(t, "SELECT *", args["query"])
			},
		},
		{
			name:     "replace field",
			original: `{"mcp_request":{"params":{"arguments":{"env":"staging"}}}}`,
			patch: []JSONPatchOp{
				{Op: "replace", Path: "/mcp_request/params/arguments/env", Value: json.RawMessage(`"production"`)},
			},
			check: func(t *testing.T, result []byte) {
				t.Helper()
				var doc map[string]interface{}
				require.NoError(t, json.Unmarshal(result, &doc))
				mcpReq := doc["mcp_request"].(map[string]interface{})
				params := mcpReq["params"].(map[string]interface{})
				args := params["arguments"].(map[string]interface{})
				assert.Equal(t, "production", args["env"])
			},
		},
		{
			name:     "multiple ops",
			original: `{"mcp_request":{"params":{"arguments":{"query":"SELECT *"}}}}`,
			patch: []JSONPatchOp{
				{Op: "add", Path: "/mcp_request/params/arguments/user", Value: json.RawMessage(`"alice"`)},
				{Op: "add", Path: "/mcp_request/params/arguments/dept", Value: json.RawMessage(`"eng"`)},
			},
			check: func(t *testing.T, result []byte) {
				t.Helper()
				var doc map[string]interface{}
				require.NoError(t, json.Unmarshal(result, &doc))
				mcpReq := doc["mcp_request"].(map[string]interface{})
				params := mcpReq["params"].(map[string]interface{})
				args := params["arguments"].(map[string]interface{})
				assert.Equal(t, "alice", args["user"])
				assert.Equal(t, "eng", args["dept"])
			},
		},
		{
			name:     "invalid JSON original",
			original: `{not valid json`,
			patch:    []JSONPatchOp{{Op: "add", Path: "/mcp_request/key", Value: json.RawMessage(`"v"`)}},
			wantErr:  true,
		},
		{
			name:     "patch to nonexistent path",
			original: `{"mcp_request":{}}`,
			patch:    []JSONPatchOp{{Op: "remove", Path: "/mcp_request/nonexistent"}},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := ApplyPatch([]byte(tt.original), tt.patch)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, result)
			}
		})
	}
}
