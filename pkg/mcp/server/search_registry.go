package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/toolhive/pkg/registry"
)

// searchRegistryArgs holds the arguments for searching the registry
type searchRegistryArgs struct {
	Query string `json:"query"`
}

// Info represents server information returned by search
type Info struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Transport   string   `json:"transport"`
	Image       string   `json:"image,omitempty"`
	Args        []string `json:"args,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// SearchRegistryResponse represents the response from searching the registry
type SearchRegistryResponse struct {
	Servers []Info `json:"servers"`
}

// SearchRegistry searches the ToolHive registry
func (h *Handler) SearchRegistry(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := &searchRegistryArgs{}
	if err := BindArguments(request, args); err != nil {
		return NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Search the registry
	servers, err := h.registryProvider.SearchServers(args.Query)
	if err != nil {
		return NewToolResultError(fmt.Sprintf("Failed to search registry: %v", err)), nil
	}

	// Format results with all available information
	var results []Info
	for _, srv := range servers {
		info := Info{
			Name:        srv.GetName(),
			Description: srv.GetDescription(),
			Transport:   srv.GetTransport(),
		}

		// Add image-specific fields if it's an ImageMetadata
		if imgMeta, ok := srv.(*registry.ImageMetadata); ok {
			info.Image = imgMeta.Image
			info.Args = imgMeta.Args
			info.Tools = imgMeta.Tools
			info.Tags = imgMeta.Tags
		}

		results = append(results, info)
	}

	// Create structured response
	response := SearchRegistryResponse{
		Servers: results,
	}

	return NewToolResultStructuredOnly(response), nil
}
