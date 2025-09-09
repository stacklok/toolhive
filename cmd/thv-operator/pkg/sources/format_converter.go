package sources

import (
	"encoding/json"
	"fmt"
	"slices"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

// FormatConverter defines the interface for converting between registry formats
type FormatConverter interface {
	// Convert transforms registry data from one format to another
	Convert(data []byte, fromFormat, toFormat string) ([]byte, error)
}

// RegistryFormatConverter implements the FormatConverter interface
type RegistryFormatConverter struct {
	validator SourceDataValidator
}

// NewRegistryFormatConverter creates a new registry format converter
func NewRegistryFormatConverter() FormatConverter {
	return &RegistryFormatConverter{
		validator: NewSourceDataValidator(),
	}
}

// Convert transforms registry data from one format to another
func (c *RegistryFormatConverter) Convert(data []byte, fromFormat, toFormat string) ([]byte, error) {
	// Validate input parameters
	if len(data) == 0 {
		return nil, fmt.Errorf("input data cannot be empty")
	}

	// Check if conversion is needed
	if fromFormat == toFormat {
		return data, nil
	}

	// Validate source format
	if err := c.validator.ValidateData(data, fromFormat); err != nil {
		return nil, fmt.Errorf("source data validation failed: %w", err)
	}

	// Check supported formats
	supportedFormats := supportedFormats()
	if !slices.Contains(supportedFormats, fromFormat) {
		return nil, fmt.Errorf("unsupported source format: %s", fromFormat)
	}
	if !slices.Contains(supportedFormats, toFormat) {
		return nil, fmt.Errorf("unsupported target format: %s", toFormat)
	}

	switch {
	case fromFormat == mcpv1alpha1.RegistryFormatUpstream && toFormat == mcpv1alpha1.RegistryFormatToolHive:
		return convertUpstreamToToolhive(data)
	case fromFormat == mcpv1alpha1.RegistryFormatToolHive && toFormat == mcpv1alpha1.RegistryFormatUpstream:
		return convertToolhiveToUpstream(data)
	default:
		return nil, fmt.Errorf("conversion from %s to %s is not supported", fromFormat, toFormat)
	}
}

// supportedFormats returns the list of supported registry formats
func supportedFormats() []string {
	return []string{
		mcpv1alpha1.RegistryFormatToolHive,
		mcpv1alpha1.RegistryFormatUpstream,
	}
}

// detectFormat attempts to automatically detect the format of the provided data
func detectFormat(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("data cannot be empty")
	}

	validator := NewSourceDataValidator()

	// Try to parse as ToolHive format first
	if err := validator.ValidateData(data, mcpv1alpha1.RegistryFormatToolHive); err == nil {
		return mcpv1alpha1.RegistryFormatToolHive, nil
	}

	// Try to parse as Upstream format
	if err := validator.ValidateData(data, mcpv1alpha1.RegistryFormatUpstream); err == nil {
		return mcpv1alpha1.RegistryFormatUpstream, nil
	}

	return "", fmt.Errorf("unable to detect registry format: data does not match any known format")
}

// convertUpstreamToToolhive converts upstream format to ToolHive format
func convertUpstreamToToolhive(data []byte) ([]byte, error) {
	// Parse upstream format
	var upstreamServers []registry.UpstreamServerDetail
	if err := json.Unmarshal(data, &upstreamServers); err != nil {
		return nil, fmt.Errorf("failed to parse upstream format: %w", err)
	}

	// Convert to ToolHive format
	toolhiveRegistry := &registry.Registry{
		Version:       "1.0",
		LastUpdated:   "", // Will be set during sync
		Servers:       make(map[string]*registry.ImageMetadata),
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
	}

	for _, upstreamServer := range upstreamServers {
		serverMetadata, err := registry.ConvertUpstreamToToolhive(&upstreamServer)
		if err != nil {
			return nil, fmt.Errorf("failed to convert server %s: %w", upstreamServer.Server.Name, err)
		}

		// Add to appropriate map based on server type
		switch server := serverMetadata.(type) {
		case *registry.ImageMetadata:
			toolhiveRegistry.Servers[upstreamServer.Server.Name] = server
		case *registry.RemoteServerMetadata:
			toolhiveRegistry.RemoteServers[upstreamServer.Server.Name] = server
		default:
			return nil, fmt.Errorf("unknown server type for %s", upstreamServer.Server.Name)
		}
	}

	// Marshal to JSON
	result, err := json.Marshal(toolhiveRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ToolHive format: %w", err)
	}

	return result, nil
}

// convertToolhiveToUpstream converts ToolHive format to upstream format
func convertToolhiveToUpstream(data []byte) ([]byte, error) {
	// Parse ToolHive format
	var toolhiveRegistry registry.Registry
	if err := json.Unmarshal(data, &toolhiveRegistry); err != nil {
		return nil, fmt.Errorf("failed to parse ToolHive format: %w", err)
	}

	var upstreamServers []registry.UpstreamServerDetail

	// Convert container servers
	for _, server := range toolhiveRegistry.Servers {
		upstreamServer, err := registry.ConvertToolhiveToUpstream(server)
		if err != nil {
			return nil, fmt.Errorf("failed to convert container server %s: %w", server.GetName(), err)
		}
		upstreamServers = append(upstreamServers, *upstreamServer)
	}

	// Convert remote servers
	for _, server := range toolhiveRegistry.RemoteServers {
		upstreamServer, err := registry.ConvertToolhiveToUpstream(server)
		if err != nil {
			return nil, fmt.Errorf("failed to convert remote server %s: %w", server.GetName(), err)
		}
		upstreamServers = append(upstreamServers, *upstreamServer)
	}

	// Marshal to JSON
	result, err := json.Marshal(upstreamServers)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upstream format: %w", err)
	}

	return result, nil
}
