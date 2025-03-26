// Package labels provides utilities for managing container labels
// used by the vibetool application.
package labels

import (
	"fmt"
	"strings"
)

const (
	// LabelPrefix is the prefix for all Vibe Tool labels
	LabelPrefix = "vibetool"

	// LabelEnabled is the label that indicates a container is managed by Vibe Tool
	LabelEnabled = "vibetool"

	// LabelName is the label that contains the container name
	LabelName = "vibetool-name"

	// LabelBaseName is the label that contains the base container name (without timestamp)
	LabelBaseName = "vibetool-basename"

	// LabelTransport is the label that contains the transport mode
	LabelTransport = "vibetool-transport"

	// LabelPort is the label that contains the port
	LabelPort = "vibetool-port"

	// LabelToolType is the label that indicates the type of tool
	LabelToolType = "vibetool-tool-type"
)

// AddStandardLabels adds standard labels to a container
func AddStandardLabels(labels map[string]string, containerName, containerBaseName, transportType string, port int) {
	// Add standard labels
	labels[LabelEnabled] = "true"
	labels[LabelName] = containerName
	labels[LabelBaseName] = containerBaseName
	labels[LabelTransport] = transportType
	labels[LabelPort] = fmt.Sprintf("%d", port)

	// TODO: In the future, we'll support different tool types beyond just "mcp"
	labels[LabelToolType] = "mcp"
}

// FormatVibeToolFilter formats a filter for Vibe Tool containers
func FormatVibeToolFilter() string {
	return fmt.Sprintf("%s=true", LabelEnabled)
}

// IsVibeToolContainer checks if a container is managed by Vibe Tool
func IsVibeToolContainer(labels map[string]string) bool {
	value, ok := labels[LabelEnabled]
	return ok && strings.ToLower(value) == "true"
}

// GetContainerName gets the container name from labels
func GetContainerName(labels map[string]string) string {
	return labels[LabelName]
}

// GetContainerBaseName gets the base container name from labels
func GetContainerBaseName(labels map[string]string) string {
	return labels[LabelBaseName]
}

// GetTransportType gets the transport type from labels
func GetTransportType(labels map[string]string) string {
	return labels[LabelTransport]
}

// GetPort gets the port from labels
func GetPort(labels map[string]string) (int, error) {
	portStr, ok := labels[LabelPort]
	if !ok {
		return 0, fmt.Errorf("port label not found")
	}

	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return 0, fmt.Errorf("invalid port: %s", portStr)
	}

	return port, nil
}

// GetToolType gets the tool type from labels
func GetToolType(labels map[string]string) string {
	return labels[LabelToolType]
}
