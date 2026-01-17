package status

import (
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// RuntimeStatus represents the operational status of a vMCP runtime instance.
// This structure captures the health and state of the vMCP server and its backends.
type RuntimeStatus struct {
	// Phase represents the current operational phase of the vMCP server.
	Phase Phase

	// Message is a human-readable message describing the current status.
	// This should provide context about the Phase and any issues encountered.
	Message string

	// Conditions represent fine-grained status conditions.
	// Following Kubernetes conventions for status reporting.
	Conditions []Condition

	// DiscoveredBackends contains information about discovered backend servers.
	DiscoveredBackends []DiscoveredBackend

	// TotalToolCount is the total number of tools available across all backends
	// after conflict resolution and aggregation.
	TotalToolCount int

	// TotalResourceCount is the total number of resources available across all backends.
	TotalResourceCount int

	// TotalPromptCount is the total number of prompts available across all backends.
	TotalPromptCount int

	// HealthyBackendCount is the number of backends in healthy state.
	HealthyBackendCount int

	// UnhealthyBackendCount is the number of backends in unhealthy state.
	UnhealthyBackendCount int

	// DegradedBackendCount is the number of backends in degraded state.
	DegradedBackendCount int

	// LastDiscoveryTime is when backend discovery last completed.
	LastDiscoveryTime time.Time

	// LastUpdateTime is when this status was last updated.
	LastUpdateTime time.Time
}

// Phase represents the operational phase of a vMCP server.
// Following Kubernetes-style phase conventions.
type Phase string

const (
	// PhaseReady indicates the vMCP server is ready and operational.
	// All critical backends are healthy and the server is accepting requests.
	PhaseReady Phase = "Ready"

	// PhaseDegraded indicates the vMCP server is operational but experiencing issues.
	// Some backends may be unhealthy, but the server can still handle requests.
	PhaseDegraded Phase = "Degraded"

	// PhaseFailed indicates the vMCP server has failed and is not operational.
	// Critical errors prevent the server from functioning correctly.
	PhaseFailed Phase = "Failed"

	// PhaseUnknown indicates the vMCP server status is unknown.
	// This is typically the initial state before the first status update.
	PhaseUnknown Phase = "Unknown"

	// PhaseStarting indicates the vMCP server is starting up.
	// Discovery and initialization are in progress.
	PhaseStarting Phase = "Starting"
)

// ConditionType represents a specific aspect of the vMCP server's status.
type ConditionType string

const (
	// ConditionBackendsDiscovered indicates whether backend discovery has completed.
	ConditionBackendsDiscovered ConditionType = "BackendsDiscovered"

	// ConditionBackendsHealthy indicates whether all backends are healthy.
	ConditionBackendsHealthy ConditionType = "BackendsHealthy"

	// ConditionServerReady indicates whether the vMCP server is ready to accept requests.
	ConditionServerReady ConditionType = "ServerReady"

	// ConditionCapabilitiesAggregated indicates whether capability aggregation has completed.
	ConditionCapabilitiesAggregated ConditionType = "CapabilitiesAggregated"
)

// ConditionStatus represents the status of a condition.
type ConditionStatus string

const (
	// ConditionTrue indicates the condition is true/satisfied.
	ConditionTrue ConditionStatus = "True"

	// ConditionFalse indicates the condition is false/not satisfied.
	ConditionFalse ConditionStatus = "False"

	// ConditionUnknown indicates the condition status is unknown.
	ConditionUnknown ConditionStatus = "Unknown"
)

// Condition represents a fine-grained status condition.
// Following Kubernetes API conventions for conditions.
type Condition struct {
	// Type is the type of this condition.
	Type ConditionType

	// Status is the status of this condition (True, False, Unknown).
	Status ConditionStatus

	// LastTransitionTime is the last time the condition transitioned from one status to another.
	LastTransitionTime time.Time

	// Reason is a programmatic identifier indicating the reason for the condition's last transition.
	// Should be a CamelCase string.
	Reason string

	// Message is a human-readable message indicating details about the transition.
	Message string
}

// DiscoveredBackend represents information about a discovered backend server.
type DiscoveredBackend struct {
	// ID is the unique identifier for this backend.
	ID string

	// Name is the human-readable name of the backend.
	Name string

	// HealthStatus is the current health status of the backend.
	HealthStatus vmcp.BackendHealthStatus

	// BaseURL is the backend's MCP server URL.
	BaseURL string

	// TransportType is the MCP transport protocol used.
	TransportType string

	// ToolCount is the number of tools provided by this backend.
	ToolCount int

	// ResourceCount is the number of resources provided by this backend.
	ResourceCount int

	// PromptCount is the number of prompts provided by this backend.
	PromptCount int

	// LastCheckTime is when the backend was last checked.
	LastCheckTime time.Time
}
