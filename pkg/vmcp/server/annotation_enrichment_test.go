// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
)

func TestAnnotationEnrichmentMiddleware(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name               string
		method             string
		resourceID         string
		capabilities       *aggregator.AggregatedCapabilities
		setDiscovery       bool
		setParsed          bool
		expectedAnnotation *authorizers.ToolAnnotations
	}{
		{
			name:         "enriches_tools_call_with_annotations",
			method:       "tools/call",
			resourceID:   "my_tool",
			setDiscovery: true,
			setParsed:    true,
			capabilities: &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{
					{
						Name: "my_tool",
						Annotations: &vmcp.ToolAnnotations{
							ReadOnlyHint:    boolPtr(true),
							DestructiveHint: boolPtr(false),
						},
					},
				},
			},
			expectedAnnotation: &authorizers.ToolAnnotations{
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
			},
		},
		{
			name:         "passes_through_for_non_tools_call",
			method:       "tools/list",
			resourceID:   "",
			setDiscovery: true,
			setParsed:    true,
			capabilities: &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{
					{
						Name: "my_tool",
						Annotations: &vmcp.ToolAnnotations{
							ReadOnlyHint: boolPtr(true),
						},
					},
				},
			},
			expectedAnnotation: nil,
		},
		{
			name:               "passes_through_when_no_parsed_request",
			method:             "",
			resourceID:         "",
			setDiscovery:       false,
			setParsed:          false,
			capabilities:       nil,
			expectedAnnotation: nil,
		},
		{
			name:               "passes_through_when_no_discovery_context",
			method:             "tools/call",
			resourceID:         "my_tool",
			setDiscovery:       false,
			setParsed:          true,
			capabilities:       nil,
			expectedAnnotation: nil,
		},
		{
			name:         "passes_through_when_tool_not_found",
			method:       "tools/call",
			resourceID:   "nonexistent_tool",
			setDiscovery: true,
			setParsed:    true,
			capabilities: &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{
					{
						Name: "other_tool",
						Annotations: &vmcp.ToolAnnotations{
							ReadOnlyHint: boolPtr(true),
						},
					},
				},
			},
			expectedAnnotation: nil,
		},
		{
			name:         "passes_through_when_tool_has_no_annotations",
			method:       "tools/call",
			resourceID:   "bare_tool",
			setDiscovery: true,
			setParsed:    true,
			capabilities: &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{
					{
						Name:        "bare_tool",
						Annotations: nil,
					},
				},
			},
			expectedAnnotation: nil,
		},
		{
			name:         "passes_through_when_annotations_have_no_hints",
			method:       "tools/call",
			resourceID:   "empty_ann_tool",
			setDiscovery: true,
			setParsed:    true,
			capabilities: &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{
					{
						Name: "empty_ann_tool",
						Annotations: &vmcp.ToolAnnotations{
							Title: "Just a title, no hints",
						},
					},
				},
			},
			expectedAnnotation: nil,
		},
		{
			name:         "enriches_from_composite_tools",
			method:       "tools/call",
			resourceID:   "composite_tool",
			setDiscovery: true,
			setParsed:    true,
			capabilities: &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{},
				CompositeTools: []vmcp.Tool{
					{
						Name: "composite_tool",
						Annotations: &vmcp.ToolAnnotations{
							IdempotentHint: boolPtr(true),
							OpenWorldHint:  boolPtr(false),
						},
					},
				},
			},
			expectedAnnotation: &authorizers.ToolAnnotations{
				IdempotentHint: boolPtr(true),
				OpenWorldHint:  boolPtr(false),
			},
		},
		{
			name:         "empty_resource_id_passes_through",
			method:       "tools/call",
			resourceID:   "",
			setDiscovery: true,
			setParsed:    true,
			capabilities: &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{
					{
						Name: "my_tool",
						Annotations: &vmcp.ToolAnnotations{
							ReadOnlyHint: boolPtr(true),
						},
					},
				},
			},
			expectedAnnotation: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var capturedAnnotation *authorizers.ToolAnnotations
			handlerCalled := false

			inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				capturedAnnotation = authorizers.ToolAnnotationsFromContext(r.Context())
			})

			wrapped := AnnotationEnrichmentMiddleware(inner)

			// Build a minimal request. The MCP body is not needed because we inject
			// the parsed request directly into context below.
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)

			ctx := req.Context()

			// Set parsed MCP request in context if needed.
			// Uses the exported MCPRequestContextKey to inject the parsed request,
			// matching how ParsingMiddleware stores it in production.
			if tt.setParsed && tt.method != "" {
				parsedReq := &mcpparser.ParsedMCPRequest{
					Method:     tt.method,
					ResourceID: tt.resourceID,
				}
				ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, parsedReq)
			}

			// Set discovery context if needed
			if tt.setDiscovery && tt.capabilities != nil {
				ctx = discovery.WithDiscoveredCapabilities(ctx, tt.capabilities)
			}

			req = req.WithContext(ctx)
			recorder := httptest.NewRecorder()

			wrapped.ServeHTTP(recorder, req)

			require.True(t, handlerCalled, "inner handler should always be called")

			if tt.expectedAnnotation == nil {
				assert.Nil(t, capturedAnnotation, "expected no annotations in context")
			} else {
				require.NotNil(t, capturedAnnotation, "expected annotations in context")
				assert.Equal(t, tt.expectedAnnotation.ReadOnlyHint, capturedAnnotation.ReadOnlyHint)
				assert.Equal(t, tt.expectedAnnotation.DestructiveHint, capturedAnnotation.DestructiveHint)
				assert.Equal(t, tt.expectedAnnotation.IdempotentHint, capturedAnnotation.IdempotentHint)
				assert.Equal(t, tt.expectedAnnotation.OpenWorldHint, capturedAnnotation.OpenWorldHint)
			}
		})
	}
}
