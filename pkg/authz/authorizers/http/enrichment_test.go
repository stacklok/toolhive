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
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testFieldServerID: testFieldTestSrv,
					},
				},
			},
			annotationMap: map[string]interface{}{
				testAnnoReadOnlyHint: true,
			},
			expected: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testFieldServerID:  testFieldTestSrv,
						testKeyAnnotations: map[string]interface{}{testAnnoReadOnlyHint: true},
					},
				},
			},
		},
		{
			name: "context exists but mcp does not exist",
			porc: PORC{
				testKeyContext: map[string]interface{}{
					"other_key": "some_value",
				},
			},
			annotationMap: map[string]interface{}{
				testAnnoDestructiveHint: true,
			},
			expected: PORC{
				testKeyContext: map[string]interface{}{
					"other_key": "some_value",
					testKeyMCP:  map[string]interface{}{testKeyAnnotations: map[string]interface{}{testAnnoDestructiveHint: true}},
				},
			},
		},
		{
			name: "context does not exist",
			porc: PORC{
				testKeyPrincipal: "user1",
			},
			annotationMap: map[string]interface{}{
				"openWorldHint": false,
			},
			expected: PORC{
				testKeyPrincipal: "user1",
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testKeyAnnotations: map[string]interface{}{"openWorldHint": false},
					},
				},
			},
		},
		{
			name: "context is nil",
			porc: PORC{
				testKeyContext: nil,
			},
			annotationMap: map[string]interface{}{
				testAnnoReadOnlyHint: true,
			},
			expected: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testKeyAnnotations: map[string]interface{}{testAnnoReadOnlyHint: true},
					},
				},
			},
		},
		{
			name: "context is wrong type (string)",
			porc: PORC{
				testKeyContext: "unexpected-string",
			},
			annotationMap: map[string]interface{}{
				"idempotentHint": true,
			},
			expected: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testKeyAnnotations: map[string]interface{}{"idempotentHint": true},
					},
				},
			},
		},
		{
			name: "context exists but mcp is wrong type (string)",
			porc: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: "unexpected-string",
				},
			},
			annotationMap: map[string]interface{}{
				testAnnoReadOnlyHint: true,
			},
			expected: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testKeyAnnotations: map[string]interface{}{testAnnoReadOnlyHint: true},
					},
				},
			},
		},
		{
			name: "existing mcp fields are preserved when annotations are added",
			porc: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testFieldServerID: "my-server",
						testFieldTool:     "calculate",
					},
				},
			},
			annotationMap: map[string]interface{}{
				testAnnoReadOnlyHint:    true,
				testAnnoDestructiveHint: false,
			},
			expected: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testFieldServerID: "my-server",
						testFieldTool:     "calculate",
						testKeyAnnotations: map[string]interface{}{
							testAnnoReadOnlyHint:    true,
							testAnnoDestructiveHint: false,
						},
					},
				},
			},
		},
		{
			name: "empty annotation map",
			porc: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testFieldServerID: testFieldTestSrv,
					},
				},
			},
			annotationMap: map[string]interface{}{},
			expected: PORC{
				testKeyContext: map[string]interface{}{
					testKeyMCP: map[string]interface{}{
						testFieldServerID:  testFieldTestSrv,
						testKeyAnnotations: map[string]interface{}{},
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
			rawCtx, ok := tt.porc[testKeyContext]
			require.True(t, ok, "porc must have a \"context\" key after enrichment")
			porcCtx, ok := rawCtx.(map[string]interface{})
			require.True(t, ok, "porc[\"context\"] must be map[string]interface{}")

			// Verify the mcp key exists and is the right type.
			rawMCP, ok := porcCtx[testKeyMCP]
			require.True(t, ok, "porc[\"context\"] must have an \"mcp\" key after enrichment")
			mcpCtx, ok := rawMCP.(map[string]interface{})
			require.True(t, ok, "porc[\"context\"][\"mcp\"] must be map[string]interface{}")

			// Verify annotations were set.
			assert.Equal(t, tt.annotationMap, mcpCtx[testKeyAnnotations])

			// Verify full PORC matches expected structure.
			assert.Equal(t, tt.expected, tt.porc)
		})
	}
}
