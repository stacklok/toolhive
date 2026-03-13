// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnrichPORCWithAnnotations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		porc          PORC
		annotationMap map[string]interface{}
		expected      PORC
	}{
		{
			name: "both context and mcp exist as correct types",
			porc: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"server_id": "test-server",
					},
				},
			},
			annotationMap: map[string]interface{}{
				"readOnlyHint": true,
			},
			expected: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"server_id":   "test-server",
						"annotations": map[string]interface{}{"readOnlyHint": true},
					},
				},
			},
		},
		{
			name: "context exists but mcp does not exist",
			porc: PORC{
				"context": map[string]interface{}{
					"other_key": "some_value",
				},
			},
			annotationMap: map[string]interface{}{
				"destructiveHint": true,
			},
			expected: PORC{
				"context": map[string]interface{}{
					"other_key": "some_value",
					"mcp":       map[string]interface{}{"annotations": map[string]interface{}{"destructiveHint": true}},
				},
			},
		},
		{
			name: "context does not exist",
			porc: PORC{
				"principal": "user1",
			},
			annotationMap: map[string]interface{}{
				"openWorldHint": false,
			},
			expected: PORC{
				"principal": "user1",
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"annotations": map[string]interface{}{"openWorldHint": false},
					},
				},
			},
		},
		{
			name: "context is nil",
			porc: PORC{
				"context": nil,
			},
			annotationMap: map[string]interface{}{
				"readOnlyHint": true,
			},
			expected: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"annotations": map[string]interface{}{"readOnlyHint": true},
					},
				},
			},
		},
		{
			name: "context is wrong type (string)",
			porc: PORC{
				"context": "unexpected-string",
			},
			annotationMap: map[string]interface{}{
				"idempotentHint": true,
			},
			expected: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"annotations": map[string]interface{}{"idempotentHint": true},
					},
				},
			},
		},
		{
			name: "context exists but mcp is wrong type (string)",
			porc: PORC{
				"context": map[string]interface{}{
					"mcp": "unexpected-string",
				},
			},
			annotationMap: map[string]interface{}{
				"readOnlyHint": true,
			},
			expected: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"annotations": map[string]interface{}{"readOnlyHint": true},
					},
				},
			},
		},
		{
			name: "existing mcp fields are preserved when annotations are added",
			porc: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"server_id": "my-server",
						"tool":      "calculate",
					},
				},
			},
			annotationMap: map[string]interface{}{
				"readOnlyHint":    true,
				"destructiveHint": false,
			},
			expected: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"server_id": "my-server",
						"tool":      "calculate",
						"annotations": map[string]interface{}{
							"readOnlyHint":    true,
							"destructiveHint": false,
						},
					},
				},
			},
		},
		{
			name: "empty annotation map",
			porc: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"server_id": "test-server",
					},
				},
			},
			annotationMap: map[string]interface{}{},
			expected: PORC{
				"context": map[string]interface{}{
					"mcp": map[string]interface{}{
						"server_id":   "test-server",
						"annotations": map[string]interface{}{},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			enrichPORCWithAnnotations(tt.porc, tt.annotationMap)

			// Verify the top-level context key exists and is the right type.
			rawCtx, ok := tt.porc["context"]
			require.True(t, ok, "porc must have a \"context\" key after enrichment")
			porcCtx, ok := rawCtx.(map[string]interface{})
			require.True(t, ok, "porc[\"context\"] must be map[string]interface{}")

			// Verify the mcp key exists and is the right type.
			rawMCP, ok := porcCtx["mcp"]
			require.True(t, ok, "porc[\"context\"] must have an \"mcp\" key after enrichment")
			mcpCtx, ok := rawMCP.(map[string]interface{})
			require.True(t, ok, "porc[\"context\"][\"mcp\"] must be map[string]interface{}")

			// Verify annotations were set.
			assert.Equal(t, tt.annotationMap, mcpCtx["annotations"])

			// Verify full PORC matches expected structure.
			assert.Equal(t, tt.expected, tt.porc)
		})
	}
}
