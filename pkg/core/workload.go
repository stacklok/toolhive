// Package core provides the core domain model for the ToolHive system.
package core

import (
	"time"

	"github.com/stacklok/toolhive/pkg/container/runtime"
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
