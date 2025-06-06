package workloads

import "time"

// WorkloadStatus is an enum representing the possible statuses of a workload.
type WorkloadStatus string

const (
	// WorkloadStatusRunning indicates that the workload is currently running.
	WorkloadStatusRunning WorkloadStatus = "running"
	// WorkloadStatusStopped indicates that the workload is stopped.
	WorkloadStatusStopped WorkloadStatus = "stopped"
	// WorkloadStatusError indicates that the workload encountered an error.
	WorkloadStatusError WorkloadStatus = "error"
	// WorkloadStatusStarting indicates that the workload is in the process of starting.
	// TODO: this is not used yet.
	WorkloadStatusStarting WorkloadStatus = "starting"
)

// DefaultToolType is the default value we use for the ToolType field in Workload.
const DefaultToolType = "mcp"

// Workload is a domain model representing a workload in the system.
// This is used in our API to hide details of the underlying runtime.
type Workload struct {
	// Name is the name of the workload.
	// It is used as a unique identifier.
	Name string `json:"name"`
	// Image specifies the image used to create this workload.
	Image string `json:"server_uri"`
	// URL is the URL of the workload exposed by the ToolHive proxy.
	URL string `json:"url"`
	// ToolType is the type of tool this workload represents.
	// For now, it will always be "mcp" - representing an MCP server.
	ToolType string `json:"tool_type"`
	// Status is the current status of the workload.
	Status WorkloadStatus `json:"status"`
	// StatusContext provides additional context about the workload's status.
	// The exact meaning is determined by the status and the underlying runtime.
	StatusContext string `json:"status_context,omitempty"`
	// CreatedAt is the timestamp when the workload was created.
	CreatedAt time.Time `json:"created_at"`
}
