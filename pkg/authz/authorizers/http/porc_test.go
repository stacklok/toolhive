// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"reflect"
	"testing"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

func TestBuildPORC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		feature    authorizers.MCPFeature
		operation  authorizers.MCPOperation
		resourceID string
		claims     map[string]interface{}
		arguments  map[string]interface{}
		wantOp     string
		wantRes    string
	}{
		{
			name:       "tool call",
			feature:    authorizers.MCPFeatureTool,
			operation:  authorizers.MCPOperationCall,
			resourceID: "weather",
			claims: map[string]interface{}{
				"sub": "user@example.com",
			},
			arguments: map[string]interface{}{
				"location": "New York",
			},
			wantOp:  "mcp:tool:call",
			wantRes: "mrn:mcp:test:tool:weather",
		},
		{
			name:       "prompt get",
			feature:    authorizers.MCPFeaturePrompt,
			operation:  authorizers.MCPOperationGet,
			resourceID: "greeting",
			claims: map[string]interface{}{
				"sub":    "user@example.com",
				"mroles": []string{"developer"},
			},
			arguments: nil,
			wantOp:    "mcp:prompt:get",
			wantRes:   "mrn:mcp:test:prompt:greeting",
		},
		{
			name:       "resource read",
			feature:    authorizers.MCPFeatureResource,
			operation:  authorizers.MCPOperationRead,
			resourceID: "file://data.json",
			claims: map[string]interface{}{
				"sub": "user@example.com",
			},
			arguments: nil,
			wantOp:    "mcp:resource:read",
			wantRes:   "mrn:mcp:test:resource:file://data.json",
		},
		{
			name:       "tool list",
			feature:    authorizers.MCPFeatureTool,
			operation:  authorizers.MCPOperationList,
			resourceID: "",
			claims: map[string]interface{}{
				"sub": "user@example.com",
			},
			arguments: nil,
			wantOp:    "mcp:tool:list",
			wantRes:   "mrn:mcp:test:tool:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			porc := buildPORC(tt.feature, tt.operation, tt.resourceID, tt.claims, tt.arguments)

			// Check operation
			if op, ok := porc["operation"].(string); !ok || op != tt.wantOp {
				t.Errorf("buildPORC() operation = %v, want %v", porc["operation"], tt.wantOp)
			}

			// Check resource
			if res, ok := porc["resource"].(string); !ok || res != tt.wantRes {
				t.Errorf("buildPORC() resource = %v, want %v", porc["resource"], tt.wantRes)
			}

			// Check principal exists (returns map[string]interface{} for PDP compatibility)
			if _, ok := porc["principal"].(map[string]interface{}); !ok {
				t.Error("buildPORC() missing principal")
			}

			// Check context exists (returns map[string]interface{} for PDP compatibility)
			if _, ok := porc["context"].(map[string]interface{}); !ok {
				t.Error("buildPORC() missing context")
			}
		})
	}
}

// defaultContextConfig returns a ContextConfig with all options enabled for tests.
func defaultContextConfig() ContextConfig {
	return ContextConfig{
		IncludeArgs:      true,
		IncludeOperation: true,
	}
}

// defaultClaimMapper returns the default MPE claim mapper for tests.
func defaultClaimMapper() ClaimMapper {
	return &MPEClaimMapper{}
}

func TestBuildPrincipal(t *testing.T) {
	t.Parallel()

	// Helper for empty mannotations
	emptyAnnotations := make(map[string]interface{})

	tests := []struct {
		name   string
		claims map[string]interface{}
		want   map[string]interface{}
	}{
		{
			name:   "nil claims",
			claims: nil,
			want:   map[string]interface{}{},
		},
		{
			name: "basic claims",
			claims: map[string]interface{}{
				"sub": "user@example.com",
			},
			want: map[string]interface{}{
				"sub":          "user@example.com",
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "mroles claim",
			claims: map[string]interface{}{
				"sub":    "user@example.com",
				"mroles": []string{"developer"},
			},
			want: map[string]interface{}{
				"sub":          "user@example.com",
				"mroles":       []string{"developer"},
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "roles mapped to mroles",
			claims: map[string]interface{}{
				"sub":   "user@example.com",
				"roles": []string{"admin"},
			},
			want: map[string]interface{}{
				"sub":          "user@example.com",
				"mroles":       []string{"admin"},
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "groups mapped to mgroups",
			claims: map[string]interface{}{
				"sub":    "user@example.com",
				"groups": []string{"engineering"},
			},
			want: map[string]interface{}{
				"sub":          "user@example.com",
				"mgroups":      []string{"engineering"},
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "scope mapped to scopes",
			claims: map[string]interface{}{
				"sub":   "user@example.com",
				"scope": "read write",
			},
			want: map[string]interface{}{
				"sub":          "user@example.com",
				"scopes":       "read write",
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "clearance mapped to mclearance",
			claims: map[string]interface{}{
				"sub":       "user@example.com",
				"clearance": "TOP_SECRET",
			},
			want: map[string]interface{}{
				"sub":          "user@example.com",
				"mclearance":   "TOP_SECRET",
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "annotations mapped to mannotations",
			claims: map[string]interface{}{
				"sub":         "user@example.com",
				"annotations": map[string]string{"dept": "engineering"},
			},
			want: map[string]interface{}{
				"sub":          "user@example.com",
				"mannotations": map[string]string{"dept": "engineering"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewPORCBuilder("test", defaultContextConfig(), defaultClaimMapper())

			got := builder.buildPrincipal(tt.claims)

			// Check expected fields
			for k, v := range tt.want {
				if !reflect.DeepEqual(got[k], v) {
					t.Errorf("buildPrincipal()[%s] = %v, want %v", k, got[k], v)
				}
			}

			// Verify mannotations exists (required for some PDPs in identity phase)
			if tt.claims != nil {
				if _, ok := got["mannotations"]; !ok {
					t.Error("buildPrincipal() missing mannotations field")
				}
			}
		})
	}
}

func TestBuildOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		feature   authorizers.MCPFeature
		operation authorizers.MCPOperation
		want      string
	}{
		{authorizers.MCPFeatureTool, authorizers.MCPOperationCall, "mcp:tool:call"},
		{authorizers.MCPFeatureTool, authorizers.MCPOperationList, "mcp:tool:list"},
		{authorizers.MCPFeaturePrompt, authorizers.MCPOperationGet, "mcp:prompt:get"},
		{authorizers.MCPFeaturePrompt, authorizers.MCPOperationList, "mcp:prompt:list"},
		{authorizers.MCPFeatureResource, authorizers.MCPOperationRead, "mcp:resource:read"},
		{authorizers.MCPFeatureResource, authorizers.MCPOperationList, "mcp:resource:list"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			builder := NewPORCBuilder("test", defaultContextConfig(), defaultClaimMapper())
			if got := builder.buildOperation(tt.feature, tt.operation); got != tt.want {
				t.Errorf("buildOperation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		feature    authorizers.MCPFeature
		resourceID string
		want       string
	}{
		{authorizers.MCPFeatureTool, "weather", "mrn:mcp:test:tool:weather"},
		{authorizers.MCPFeaturePrompt, "greeting", "mrn:mcp:test:prompt:greeting"},
		{authorizers.MCPFeatureResource, "file://data.json", "mrn:mcp:test:resource:file://data.json"},
		{authorizers.MCPFeatureTool, "", "mrn:mcp:test:tool:"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			builder := NewPORCBuilder("test", defaultContextConfig(), defaultClaimMapper())
			if got := builder.buildResource(tt.feature, tt.resourceID); got != tt.want {
				t.Errorf("buildResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		feature    authorizers.MCPFeature
		operation  authorizers.MCPOperation
		resourceID string
		arguments  map[string]interface{}
		wantArgs   bool
	}{
		{
			name:       "basic context",
			feature:    authorizers.MCPFeatureTool,
			operation:  authorizers.MCPOperationCall,
			resourceID: "weather",
			arguments:  nil,
			wantArgs:   false,
		},
		{
			name:       "context with arguments",
			feature:    authorizers.MCPFeatureTool,
			operation:  authorizers.MCPOperationCall,
			resourceID: "weather",
			arguments: map[string]interface{}{
				"location": "New York",
				"units":    "celsius",
			},
			wantArgs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder := NewPORCBuilder("test", defaultContextConfig(), defaultClaimMapper())
			got := builder.buildContext(tt.feature, tt.operation, tt.resourceID, tt.arguments)

			// Check that mcp object exists
			mcpObj, ok := got["mcp"].(map[string]interface{})
			if !ok {
				t.Fatal("buildContext() missing or invalid mcp object")
			}

			// Check feature, operation, resource_id values in nested mcp object
			if mcpObj["feature"] != string(tt.feature) {
				t.Errorf("buildContext()[mcp.feature] = %v, want %v", mcpObj["feature"], tt.feature)
			}
			if mcpObj["operation"] != string(tt.operation) {
				t.Errorf("buildContext()[mcp.operation] = %v, want %v", mcpObj["operation"], tt.operation)
			}
			if mcpObj["resource_id"] != tt.resourceID {
				t.Errorf("buildContext()[mcp.resource_id] = %v, want %v", mcpObj["resource_id"], tt.resourceID)
			}

			// Check args
			if tt.wantArgs {
				if _, ok := mcpObj["args"]; !ok {
					t.Error("buildContext() missing mcp.args when arguments provided")
				}
			} else {
				if _, ok := mcpObj["args"]; ok {
					t.Error("buildContext() has mcp.args when no arguments provided")
				}
			}
		})
	}
}

func TestBuildContext_ConfigOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        ContextConfig
		arguments     map[string]interface{}
		wantMcpObject bool
		wantOperation bool
		wantArgs      bool
	}{
		{
			name:          "default config - no context",
			config:        ContextConfig{},
			arguments:     map[string]interface{}{"location": "New York"},
			wantMcpObject: false,
			wantOperation: false,
			wantArgs:      false,
		},
		{
			name: "include operation only",
			config: ContextConfig{
				IncludeOperation: true,
			},
			arguments:     map[string]interface{}{"location": "New York"},
			wantMcpObject: true,
			wantOperation: true,
			wantArgs:      false,
		},
		{
			name: "include args only",
			config: ContextConfig{
				IncludeArgs: true,
			},
			arguments:     map[string]interface{}{"location": "New York"},
			wantMcpObject: true,
			wantOperation: false,
			wantArgs:      true,
		},
		{
			name: "include args only - no arguments provided",
			config: ContextConfig{
				IncludeArgs: true,
			},
			arguments:     nil,
			wantMcpObject: false,
			wantOperation: false,
			wantArgs:      false,
		},
		{
			name: "include both",
			config: ContextConfig{
				IncludeArgs:      true,
				IncludeOperation: true,
			},
			arguments:     map[string]interface{}{"location": "New York"},
			wantMcpObject: true,
			wantOperation: true,
			wantArgs:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder := NewPORCBuilder("test", tt.config, defaultClaimMapper())
			got := builder.buildContext(authorizers.MCPFeatureTool, authorizers.MCPOperationCall, "weather", tt.arguments)

			mcpObj, hasMcp := got["mcp"].(map[string]interface{})

			if tt.wantMcpObject {
				if !hasMcp {
					t.Fatal("buildContext() expected mcp object but not found")
				}

				// Check operation fields
				_, hasFeature := mcpObj["feature"]
				_, hasOperation := mcpObj["operation"]
				_, hasResourceID := mcpObj["resource_id"]

				if tt.wantOperation {
					if !hasFeature || !hasOperation || !hasResourceID {
						t.Error("buildContext() missing operation fields when IncludeOperation is true")
					}
				} else {
					if hasFeature || hasOperation || hasResourceID {
						t.Error("buildContext() has operation fields when IncludeOperation is false")
					}
				}

				// Check args
				_, hasArgs := mcpObj["args"]
				if tt.wantArgs {
					if !hasArgs {
						t.Error("buildContext() missing args when IncludeArgs is true")
					}
				} else {
					if hasArgs {
						t.Error("buildContext() has args when IncludeArgs is false")
					}
				}
			} else {
				if hasMcp {
					t.Errorf("buildContext() expected no mcp object but got: %v", mcpObj)
				}
			}
		})
	}
}
