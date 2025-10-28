package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WorkloadInfo represents workload information returned by list
type WorkloadInfo struct {
	Name      string `json:"name"`
	Server    string `json:"server,omitempty"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	URL       string `json:"url,omitempty"`
}

// ListServersResponse represents the response from listing servers
type ListServersResponse struct {
	Servers []WorkloadInfo `json:"servers"`
}

// ListServers lists all running MCP servers
func (h *Handler) ListServers(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// List all workloads (including stopped ones)
	wklds, err := h.workloadManager.ListWorkloads(ctx, true)
	if err != nil {
		return NewToolResultError(fmt.Sprintf("Failed to list workloads: %v", err)), nil
	}

	// Format results with structured data
	var results []WorkloadInfo
	for _, workload := range wklds {
		info := WorkloadInfo{
			Name:      workload.Name,
			Status:    string(workload.Status),
			CreatedAt: workload.CreatedAt.Format("2006-01-02 15:04:05"),
		}

		// Add server name from labels if available
		if serverName, ok := workload.Labels["toolhive.server"]; ok {
			info.Server = serverName
		}

		// Add URL if port is available
		if workload.Port > 0 {
			info.URL = fmt.Sprintf("http://localhost:%d", workload.Port)
		}

		results = append(results, info)
	}

	// Create structured response
	response := ListServersResponse{
		Servers: results,
	}

	return NewToolResultStructuredOnly(response), nil
}
