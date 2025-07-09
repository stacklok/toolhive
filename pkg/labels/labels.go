// Package labels provides utilities for managing container labels
// used by the toolhive application.
package labels

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// LabelPrefix is the prefix for all ToolHive labels
	LabelPrefix = "toolhive"

	// LabelToolHive is the label that indicates a container is managed by ToolHive
	LabelToolHive = "toolhive"

	// LabelName is the label that contains the container name
	LabelName = "toolhive-name"

	// LabelBaseName is the label that contains the base container name (without timestamp)
	LabelBaseName = "toolhive-basename"

	// LabelTransport is the label that contains the transport mode
	LabelTransport = "toolhive-transport"

	// LabelPort is the label that contains the port
	LabelPort = "toolhive-port"

	// LabelToolType is the label that indicates the type of tool
	LabelToolType = "toolhive-tool-type"

	// LabelNetworkIsolation indicates that the network isolation functionality is enabled.
	LabelNetworkIsolation = "toolhive-network-isolation"

	// LabelToolHiveValue is the value for the LabelToolHive label
	LabelToolHiveValue = "true"
)

// AddStandardLabels adds standard labels to a container
func AddStandardLabels(labels map[string]string, containerName, containerBaseName, transportType string, port int) {
	// Add standard labels
	labels[LabelToolHive] = LabelToolHiveValue
	labels[LabelName] = containerName
	labels[LabelBaseName] = containerBaseName
	labels[LabelTransport] = transportType
	labels[LabelPort] = fmt.Sprintf("%d", port)

	// TODO: In the future, we'll support different tool types beyond just "mcp"
	labels[LabelToolType] = "mcp"
}

// AddNetworkLabels adds network-related labels to a network
func AddNetworkLabels(labels map[string]string, networkName string) {
	labels[LabelToolHive] = LabelToolHiveValue
	labels[LabelName] = networkName
}

// AddNetworkIsolationLabel adds the network isolation label to a container
func AddNetworkIsolationLabel(labels map[string]string, networkIsolation bool) {
	labels[LabelNetworkIsolation] = strconv.FormatBool(networkIsolation)
}

// FormatToolHiveFilter formats a filter for ToolHive containers
func FormatToolHiveFilter() string {
	return fmt.Sprintf("%s=%s", LabelToolHive, LabelToolHiveValue)
}

// IsToolHiveContainer checks if a container is managed by ToolHive
func IsToolHiveContainer(labels map[string]string) bool {
	value, ok := labels[LabelToolHive]
	return ok && strings.ToLower(value) == LabelToolHiveValue
}

// HasNetworkIsolation checks if a container has network isolation enabled.
func HasNetworkIsolation(labels map[string]string) bool {
	value, ok := labels[LabelNetworkIsolation]
	// If the label is not present, assume that network isolation is enabled.
	// This is to ensure that workloads created before this label was introduced
	// will be properly cleaned up during stop/rm.
	return !ok || strings.ToLower(value) == "true"
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
