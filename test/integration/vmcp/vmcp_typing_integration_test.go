package vmcp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/test/integration/vmcp/helpers"
)

// TestVMCPServer_TypeCoercion verifies that composite tools correctly coerce
// template-expanded string values to their expected types (integer, number,
// boolean) when the backend tool's InputSchema specifies those types.
// This tests the fix for issue #3113.
//
//nolint:paralleltest // uses shared test fixtures
func TestVMCPServer_TypeCoercion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Track what types were received by the backend
	var receivedArgs map[string]any

	// Backend tool with typed InputSchema
	backendServer := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendToolWithSchema(
			"typed_tool",
			"Tool with typed parameters",
			mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"str_param":  map[string]any{"type": "string"},
					"int_param":  map[string]any{"type": "integer"},
					"num_param":  map[string]any{"type": "number"},
					"bool_param": map[string]any{"type": "boolean"},
				},
				Required: []string{"str_param", "int_param", "num_param", "bool_param"},
			},
			func(_ context.Context, args map[string]any) string {
				receivedArgs = args
				result, _ := json.Marshal(args)
				return string(result)
			},
		),
	}, helpers.WithBackendName("typed-mcp"))
	defer backendServer.Close()

	backends := []vmcp.Backend{
		helpers.NewBackend("typed",
			helpers.WithURL(backendServer.URL+"/mcp"),
			helpers.WithMetadata("group", "test-group"),
		),
	}

	// Composite tool that uses template expansion for all parameters
	workflowDefs := map[string]*composer.WorkflowDefinition{
		"coerce_types": {
			Name:        "coerce_types",
			Description: "Test type coercion for all primitive types",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"str_param":  map[string]any{"type": "string"},
					"int_param":  map[string]any{"type": "integer"},
					"num_param":  map[string]any{"type": "number"},
					"bool_param": map[string]any{"type": "boolean"},
				},
				"required": []string{"str_param", "int_param", "num_param", "bool_param"},
			},
			Steps: []composer.WorkflowStep{
				{
					ID:   "call_typed",
					Type: "tool",
					Tool: "typed_typed_tool",
					Arguments: map[string]any{
						// Template expansion converts all values to strings
						"str_param":  "{{.params.str_param}}",
						"int_param":  "{{.params.int_param}}",
						"num_param":  "{{.params.num_param}}",
						"bool_param": "{{.params.bool_param}}",
					},
				},
			},
			Timeout: 30 * time.Second,
		},
	}

	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
		helpers.WithWorkflowDefinitions(workflowDefs),
	)

	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	client := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer client.Close()

	// Call with typed parameters
	resp := client.CallTool(ctx, "coerce_types", map[string]any{
		"str_param":  "hello",
		"int_param":  42,
		"num_param":  3.14,
		"bool_param": true,
	})
	helpers.AssertToolCallSuccess(t, resp)

	// Verify all types were coerced correctly
	// JSON transport converts all numbers to float64
	require.Equal(t, map[string]any{
		"str_param":  "hello",
		"int_param":  float64(42),
		"num_param":  3.14,
		"bool_param": true,
	}, receivedArgs)
}

// TestVMCPServer_TypeCoercion_NestedAndArrays verifies type coercion for
// nested objects and arrays.
//
//nolint:paralleltest // uses shared test fixtures
func TestVMCPServer_TypeCoercion_NestedAndArrays(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var receivedArgs map[string]any

	backendServer := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendToolWithSchema(
			"nested_tool",
			"Tool with nested parameters",
			mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"config": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"timeout": map[string]any{"type": "integer"},
							"enabled": map[string]any{"type": "boolean"},
							"ratio":   map[string]any{"type": "number"},
						},
					},
					"ids": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "integer"},
					},
				},
			},
			func(_ context.Context, args map[string]any) string {
				receivedArgs = args
				result, _ := json.Marshal(args)
				return string(result)
			},
		),
	}, helpers.WithBackendName("nested-mcp"))
	defer backendServer.Close()

	backends := []vmcp.Backend{
		helpers.NewBackend("nested",
			helpers.WithURL(backendServer.URL+"/mcp"),
			helpers.WithMetadata("group", "test-group"),
		),
	}

	workflowDefs := map[string]*composer.WorkflowDefinition{
		"test_nested": {
			Name:        "test_nested",
			Description: "Test nested type coercion",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"timeout": map[string]any{"type": "integer"},
					"enabled": map[string]any{"type": "boolean"},
					"ratio":   map[string]any{"type": "number"},
				},
			},
			Steps: []composer.WorkflowStep{
				{
					ID:   "call_nested",
					Type: "tool",
					Tool: "nested_nested_tool",
					Arguments: map[string]any{
						"config": map[string]any{
							"timeout": "{{.params.timeout}}",
							"enabled": "{{.params.enabled}}",
							"ratio":   "{{.params.ratio}}",
						},
						// Static array with string values to test array coercion
						"ids": []any{"1", "2", "3"},
					},
				},
			},
			Timeout: 30 * time.Second,
		},
	}

	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
		helpers.WithWorkflowDefinitions(workflowDefs),
	)

	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	client := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer client.Close()

	resp := client.CallTool(ctx, "test_nested", map[string]any{
		"timeout": 30,
		"enabled": true,
		"ratio":   3.14,
	})
	helpers.AssertToolCallSuccess(t, resp)

	// Verify nested object and array coercion
	// JSON transport converts all numbers to float64
	require.Equal(t, map[string]any{
		"config": map[string]any{
			"timeout": float64(30),
			"enabled": true,
			"ratio":   3.14,
		},
		"ids": []any{float64(1), float64(2), float64(3)},
	}, receivedArgs)
}
