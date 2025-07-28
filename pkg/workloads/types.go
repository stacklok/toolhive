package workloads

import (
	"context"
	"time"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Workload is a domain model representing a workload in the system.
// This is used in our API to hide details of the underlying runtime.
type Workload struct {
	// Name is the name of the workload.
	// It is used as a unique identifier.
	Name string `json:"name"`
	// Package specifies the Workload Package used to create this Workload.
	Package string `json:"package"`
	// URL is the URL of the workload exposed by the ToolHive proxy.
	URL string `json:"url"`
	// Port is the port on which the workload is exposed.
	// This is embedded in the URL.
	Port int `json:"port"`
	// ToolType is the type of tool this workload represents.
	// For now, it will always be "mcp" - representing an MCP server.
	ToolType string `json:"tool_type"`
	// TransportType is the type of transport used for this workload.
	TransportType types.TransportType `json:"transport_type"`
	// Status is the current status of the workload.
	Status runtime.WorkloadStatus `json:"status"`
	// StatusContext provides additional context about the workload's status.
	// The exact meaning is determined by the status and the underlying runtime.
	StatusContext string `json:"status_context,omitempty"`
	// CreatedAt is the timestamp when the workload was created.
	CreatedAt time.Time `json:"created_at"`
	// Labels are the container labels (excluding standard ToolHive labels)
	Labels map[string]string `json:"labels,omitempty"`
	// Group is the name of the group this workload belongs to, if any.
	Group string `json:"group,omitempty"`
	// ToolsFilter is the filter on tools applied to the workload.
	ToolsFilter []string `json:"tools,omitempty"`
}

// loadGroupFromRunConfig attempts to load group information from the runconfig
// Returns empty string if runconfig doesn't exist or doesn't have group info
func loadGroupFromRunConfig(ctx context.Context, name string) (string, error) {
	// Try to load the runconfig
	runnerInstance, err := runner.LoadState(ctx, name)
	if err != nil {
		if errors.IsRunConfigNotFound(err) {
			return "", nil
		}
		return "", err
	}

	// Return the group from the runconfig
	return runnerInstance.Config.Group, nil
}

// WorkloadFromContainerInfo creates a Workload struct from the runtime container info.
func WorkloadFromContainerInfo(container *runtime.ContainerInfo) (Workload, error) {
	// Get container name from labels
	name := labels.GetContainerName(container.Labels)
	if name == "" {
		name = container.Name // Fallback to container name
	}

	// Get tool type from labels
	toolType := labels.GetToolType(container.Labels)

	// Get port from labels
	port, err := labels.GetPort(container.Labels)
	if err != nil {
		port = 0
	}

	// check if we have the label for transport type (toolhive-transport)
	transportType := labels.GetTransportType(container.Labels)

	// Generate URL for the MCP server
	url := ""
	if port > 0 {
		url = client.GenerateMCPServerURL(transportType, transport.LocalhostIPv4, port, name)
	}
	
	tType, err := types.ParseTransportType(transportType)
	if err != nil {
		// If we can't parse the transport type, default to SSE.
		tType = types.TransportTypeSSE
	}

	// Filter out standard ToolHive labels to show only user-defined labels
	userLabels := make(map[string]string)
	for key, value := range container.Labels {
		if !labels.IsStandardToolHiveLabel(key) {
			userLabels[key] = value
		}
	}

	ctx := context.Background()
	groupName, err := loadGroupFromRunConfig(ctx, name)
	if err != nil {
		return Workload{}, err
	}

	// Translate to the domain model.
	return Workload{
		Name: container.Name,
		// TODO: make this return the thv-specific name.
		Package:       container.Image,
		URL:           url,
		ToolType:      toolType,
		TransportType: tType,
		Status:        container.State,
		StatusContext: container.Status,
		CreatedAt:     container.Created,
		Port:          port,
		Labels:        userLabels,
		Group:         groupName,
	}, nil
}
