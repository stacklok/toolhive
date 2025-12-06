package types

import (
	"context"
	"encoding/json"
	"io"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// minimalRunConfig represents just the fields we need from a run configuration
type minimalRunConfig struct {
	Group     string `json:"group,omitempty" yaml:"group,omitempty"`
	ProxyMode string `json:"proxy_mode,omitempty" yaml:"proxy_mode,omitempty"`
}

// loadRunConfigFields attempts to load specific fields from the runconfig
// Returns empty struct if runconfig doesn't exist
func loadRunConfigFields(ctx context.Context, name string) (*minimalRunConfig, error) {
	// Try to load the runconfig
	runConfig, err := state.LoadRunConfig(ctx, name, func(r io.Reader) (*minimalRunConfig, error) {
		var config minimalRunConfig
		decoder := json.NewDecoder(r)
		if err := decoder.Decode(&config); err != nil {
			return nil, err
		}
		return &config, nil
	})
	if err != nil {
		if errors.IsRunConfigNotFound(err) {
			return &minimalRunConfig{}, nil
		}
		return nil, err
	}

	// Return the runconfig
	return runConfig, nil
}

// WorkloadFromContainerInfo creates a Workload struct from the runtime container info.
func WorkloadFromContainerInfo(container *runtime.ContainerInfo) (core.Workload, error) {
	// Get workload name (base name) from labels for user-facing display
	name := labels.GetContainerBaseName(container.Labels)
	if name == "" {
		// Fallback to full container name if base name is not available
		containerName := labels.GetContainerName(container.Labels)
		if containerName == "" {
			name = container.Name // Final fallback to container name
		} else {
			name = containerName
		}
	}

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
		url = transport.GenerateMCPServerURL(transportType, transport.LocalhostIPv4, port, name, "")
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
	runConfig, err := loadRunConfigFields(ctx, name)
	if err != nil {
		return core.Workload{}, err
	}

	// Calculate the effective proxy mode that clients should use
	effectiveProxyMode := GetEffectiveProxyMode(tType, runConfig.ProxyMode)

	// Translate to the domain model.
	return core.Workload{
		Name:          name, // Use the calculated workload name (base name), not container name
		Package:       container.Image,
		URL:           url,
		TransportType: tType,
		ProxyMode:     effectiveProxyMode,
		Status:        container.State,
		StatusContext: container.Status,
		CreatedAt:     container.Created,
		Port:          port,
		Labels:        userLabels,
		Group:         runConfig.Group,
	}, nil
}

// GetEffectiveProxyMode determines the effective proxy mode that clients should use
// For stdio transports, this returns the proxy mode (sse or streamable-http)
// For direct transports (sse/streamable-http), this returns the transport type as the proxy mode
func GetEffectiveProxyMode(transportType types.TransportType, proxyMode string) string {
	// If the underlying transport is stdio, return the proxy mode (could be empty)
	if transportType == types.TransportTypeStdio {
		return proxyMode
	}

	// For direct transports (sse, streamable-http), return the transport type as the proxy mode
	return transportType.String()
}
