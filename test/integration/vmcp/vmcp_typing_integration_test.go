package vmcp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
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

	require.NotNil(t, receivedArgs, "Backend should have received arguments")

	// Verify string stayed as string
	assert.Equal(t, "hello", receivedArgs["str_param"], "string should stay string")

	// Verify integer was coerced (JSON numbers are float64)
	switch v := receivedArgs["int_param"].(type) {
	case float64:
		assert.Equal(t, float64(42), v)
	case int64:
		assert.Equal(t, int64(42), v)
	default:
		t.Errorf("int_param is %T (%v), expected numeric", v, v)
	}

	// Verify number was coerced
	switch v := receivedArgs["num_param"].(type) {
	case float64:
		assert.InDelta(t, 3.14, v, 0.001)
	default:
		t.Errorf("num_param is %T (%v), expected float64", v, v)
	}

	// Verify boolean was coerced
	switch v := receivedArgs["bool_param"].(type) {
	case bool:
		assert.True(t, v)
	default:
		t.Errorf("bool_param is %T (%v), expected bool", v, v)
	}
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

	require.NotNil(t, receivedArgs)

	// Check nested object
	config, ok := receivedArgs["config"].(map[string]any)
	require.True(t, ok, "config should be map[string]any")

	switch v := config["timeout"].(type) {
	case float64:
		assert.Equal(t, float64(30), v)
	case int64:
		assert.Equal(t, int64(30), v)
	default:
		t.Errorf("config.timeout is %T, expected numeric", v)
	}

	switch v := config["enabled"].(type) {
	case bool:
		assert.True(t, v)
	default:
		t.Errorf("config.enabled is %T, expected bool", v)
	}

	switch v := config["ratio"].(type) {
	case float64:
		assert.InDelta(t, 3.14, v, 0.001)
	default:
		t.Errorf("config.ratio is %T, expected float64", v)
	}

	// Check array elements
	ids, ok := receivedArgs["ids"].([]any)
	require.True(t, ok, "ids should be []any")
	require.Len(t, ids, 3)

	for i, id := range ids {
		switch id.(type) {
		case float64, int64, int:
			// Good - numeric type
		default:
			t.Errorf("ids[%d] is %T, expected numeric", i, id)
		}
	}
}
