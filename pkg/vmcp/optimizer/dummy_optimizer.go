package optimizer

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

// DummyOptimizer implements the Optimizer interface using exact string matching.
//
// This implementation is intended for testing and development. It performs
// case-insensitive substring matching on tool names and descriptions.
//
// For production use, see the EmbeddingOptimizer which uses semantic similarity.
type DummyOptimizer struct {
	// tools contains all available tools indexed by name.
	tools map[string]vmcp.Tool

	// router routes tool calls to backend servers.
	router router.Router

	// backendClient executes tool calls on backend servers.
	backendClient vmcp.BackendClient
}

// NewDummyOptimizer creates a new DummyOptimizer with the given tools.
//
// The tools slice should only include backend tools (not composite tools).
// Composite tools are not supported in this initial implementation as they
// require execution through the composer, not the router/backendClient.
// TODO(jeremy): Add composite tool support.
//
// The router and backendClient are used by CallTool to route and execute
// tool invocations on backend servers.
// TODO: replace the dummy optimizer with a similarity search optimizer.
func NewDummyOptimizer(
	tools []vmcp.Tool,
	router router.Router,
	backendClient vmcp.BackendClient,
) *DummyOptimizer {
	toolMap := make(map[string]vmcp.Tool, len(tools))
	for _, tool := range tools {
		// Skip composite tools (no backend) - not supported in this implementation
		if tool.BackendID == "" {
			continue
		}
		toolMap[tool.Name] = tool
	}

	return &DummyOptimizer{
		tools:         toolMap,
		router:        router,
		backendClient: backendClient,
	}
}

// FindTool searches for tools using exact substring matching.
//
// The search is case-insensitive and matches against:
//   - Tool name (substring match)
//   - Tool description (substring match)
//
// Returns all matching tools with a score of 1.0 (exact match semantics).
// TokenMetrics are returned as zero values (not implemented in dummy).
func (d *DummyOptimizer) FindTool(_ context.Context, input FindToolInput) (*FindToolOutput, error) {
	if input.ToolDescription == "" {
		return nil, fmt.Errorf("tool_description is required")
	}

	searchTerm := strings.ToLower(input.ToolDescription)

	var matches []ToolMatch
	for _, tool := range d.tools {
		nameLower := strings.ToLower(tool.Name)
		descLower := strings.ToLower(tool.Description)

		// Check if search term matches name or description
		if strings.Contains(nameLower, searchTerm) || strings.Contains(descLower, searchTerm) {
			matches = append(matches, ToolMatch{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
				Score:       1.0, // Exact match semantics
			})
		}
	}

	return &FindToolOutput{
		Tools:        matches,
		TokenMetrics: TokenMetrics{}, // Zero values for dummy
	}, nil
}

// CallTool invokes a tool by name using the router and backend client.
//
// The tool is looked up by exact name match. If found, the request is
// routed to the appropriate backend and executed.
func (d *DummyOptimizer) CallTool(ctx context.Context, input CallToolInput) (map[string]any, error) {
	if input.ToolName == "" {
		return nil, fmt.Errorf("tool_name is required")
	}

	// Verify the tool exists
	tool, exists := d.tools[input.ToolName]
	if !exists {
		return nil, fmt.Errorf("tool not found: %s", input.ToolName)
	}

	// Route to the correct backend
	target, err := d.router.RouteTool(ctx, tool.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to route tool %s: %w", input.ToolName, err)
	}

	// Get the backend name for this tool (handles conflict resolution renaming)
	backendToolName := target.GetBackendCapabilityName(tool.Name)

	// Execute the tool call and return result directly
	return d.backendClient.CallTool(ctx, target, backendToolName, input.Parameters)
}
