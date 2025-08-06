package workloads

import (
	"context"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// loadGroupFromRunConfig attempts to load group information from the runconfig
// Returns empty string if runconfig doesn't exist or doesn't have group info
func loadGroupFromRunConfig(ctx context.Context, name string) (string, error) {
	// Try to load the runconfig
	runConfig, err := runner.LoadState(ctx, name)
	if err != nil {
		if errors.IsRunConfigNotFound(err) {
			return "", nil
		}
		return "", err
	}

	// Return the group from the runconfig
	return runConfig.Group, nil
}

// WorkloadFromContainerInfo creates a Workload struct from the runtime container info.
func WorkloadFromContainerInfo(container *runtime.ContainerInfo) (core.Workload, error) {
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
		return core.Workload{}, err
	}

	// Translate to the domain model.
	return core.Workload{
		Name:          container.Name,
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
