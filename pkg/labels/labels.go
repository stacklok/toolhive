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

	// LabelGroup is the label that contains the group name
	LabelGroup = "toolhive-group"

	// LabelToolHiveValue is the value for the LabelToolHive label
	LabelToolHiveValue = "true"
)

// AddStandardLabels adds standard labels to a container
func AddStandardLabels(labels map[string]string, containerName, containerBaseName, transportType string, port int) {
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

// GetGroup gets the group name from labels
func GetGroup(labels map[string]string) string {
	return labels[LabelGroup]
}

// SetGroup sets the group name in labels
func SetGroup(labels map[string]string, groupName string) {
	labels[LabelGroup] = groupName
}

// IsStandardToolHiveLabel checks if a label key is a standard ToolHive label
// that should not be passed through from user input or displayed to users
func IsStandardToolHiveLabel(key string) bool {
	standardLabels := []string{
		LabelToolHive,
		LabelName,
		LabelBaseName,
		LabelTransport,
		LabelPort,
		LabelToolType,
		LabelNetworkIsolation,
	}

	for _, standardLabel := range standardLabels {
		if key == standardLabel {
			return true
		}
	}

	return false
}

// ParseLabel parses a label string in the format "key=value" and validates it
// according to Kubernetes label naming conventions
func ParseLabel(label string) (string, string, error) {
	parts := strings.SplitN(label, "=", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid label format, expected key=value")
	}

	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])

	if key == "" {
		return "", "", fmt.Errorf("label key cannot be empty")
	}

	// Validate key according to Kubernetes label naming conventions
	if err := validateLabelKey(key); err != nil {
		return "", "", fmt.Errorf("invalid label key: %v", err)
	}

	// Validate value according to Kubernetes label naming conventions
	if err := validateLabelValue(value); err != nil {
		return "", "", fmt.Errorf("invalid label value: %v", err)
	}

	return key, value, nil
}

// validateLabelKey validates a label key according to Kubernetes naming conventions
func validateLabelKey(key string) error {
	if len(key) == 0 {
		return fmt.Errorf("key cannot be empty")
	}
	if len(key) > 253 {
		return fmt.Errorf("key cannot be longer than 253 characters")
	}

	// Check for valid prefix (optional)
	parts := strings.Split(key, "/")
	if len(parts) > 2 {
		return fmt.Errorf("key can have at most one '/' separator")
	}

	var name string
	if len(parts) == 2 {
		prefix := parts[0]
		name = parts[1]

		// Validate prefix (should be a valid DNS subdomain)
		if len(prefix) > 253 {
			return fmt.Errorf("prefix cannot be longer than 253 characters")
		}
		if !isValidDNSSubdomain(prefix) {
			return fmt.Errorf("prefix must be a valid DNS subdomain")
		}
	} else {
		name = parts[0]
	}

	// Validate name part
	if len(name) == 0 {
		return fmt.Errorf("name part cannot be empty")
	}
	if len(name) > 63 {
		return fmt.Errorf("name part cannot be longer than 63 characters")
	}
	if !isValidLabelName(name) {
		return fmt.Errorf("name part must consist of alphanumeric characters, '-', '_' or '.', " +
			"and must start and end with an alphanumeric character")
	}

	return nil
}

// validateLabelValue validates a label value according to Kubernetes naming conventions
func validateLabelValue(value string) error {
	if len(value) > 63 {
		return fmt.Errorf("value cannot be longer than 63 characters")
	}
	if value != "" && !isValidLabelName(value) {
		return fmt.Errorf("value must consist of alphanumeric characters, '-', '_' or '.', " +
			"and must start and end with an alphanumeric character")
	}
	return nil
}

// isValidDNSSubdomain checks if a string is a valid DNS subdomain
func isValidDNSSubdomain(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}

	parts := strings.Split(s, ".")
	for _, part := range parts {
		if len(part) == 0 || len(part) > 63 {
			return false
		}
		if !isValidDNSLabel(part) {
			return false
		}
	}
	return true
}

// isValidDNSLabel checks if a string is a valid DNS label
func isValidDNSLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}

	// Must start and end with alphanumeric
	if !isAlphaNumeric(s[0]) || !isAlphaNumeric(s[len(s)-1]) {
		return false
	}

	// Middle characters can be alphanumeric or hyphen
	for i := 1; i < len(s)-1; i++ {
		if !isAlphaNumeric(s[i]) && s[i] != '-' {
			return false
		}
	}

	return true
}

// isValidLabelName checks if a string is a valid label name
func isValidLabelName(s string) bool {
	if len(s) == 0 {
		return false
	}

	// Must start and end with alphanumeric
	if !isAlphaNumeric(s[0]) || !isAlphaNumeric(s[len(s)-1]) {
		return false
	}

	// Middle characters can be alphanumeric, hyphen, underscore, or dot
	for i := 1; i < len(s)-1; i++ {
		if !isAlphaNumeric(s[i]) && s[i] != '-' && s[i] != '_' && s[i] != '.' {
			return false
		}
	}

	return true
}

// isAlphaNumeric checks if a character is alphanumeric
func isAlphaNumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
