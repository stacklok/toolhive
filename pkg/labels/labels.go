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
	
	// LabelTransport is the label that contains the transport mode
	LabelTransport = "vibetool-transport"
	
	// LabelPort is the label that contains the port
	LabelPort = "vibetool-port"
)

// AddStandardLabels adds standard labels to a container
func AddStandardLabels(labels map[string]string, containerName, transportType string, port int) {
	// Add standard labels
	labels[LabelEnabled] = "true"
	labels[LabelName] = containerName
	labels[LabelTransport] = transportType
	labels[LabelPort] = fmt.Sprintf("%d", port)
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