// Package status provides platform-agnostic status reporting for vMCP servers.
//
// The StatusReporter abstraction enables vMCP runtime to report operational status
// back to the control plane (Kubernetes operator or CLI state manager). This allows
// the runtime to autonomously update backend discovery results, health status, and
// operational state without relying on the controller to infer it through polling.
//
// This abstraction is essential for issue #3004 (removing operator discovery in dynamic mode)
// because it allows vMCP runtime to discover backends and report the results back.
package status

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reporter provides a platform-agnostic interface for vMCP runtime to report status.
//
// Implementations:
//   - K8SReporter: Updates VirtualMCPServer.Status in Kubernetes cluster (requires RBAC)
//   - NoOpReporter: Silent implementation for CLI mode (no persistent status)
//
// The reporter is designed to be called by vMCP runtime during:
//   - Backend discovery (report discovered backends)
//   - Health checks (update backend health status)
//   - Lifecycle events (server starting, ready, degraded, failed)
type Reporter interface {
	// ReportStatus updates the complete status atomically.
	// This is the primary method for status reporting.
	ReportStatus(ctx context.Context, status *Status) error

	// Start begins periodic background status reporting (if applicable).
	// Returns immediately after starting background goroutine.
	Start(ctx context.Context) error

	// Stop halts periodic status reporting and waits for cleanup.
	// Blocks until all pending reports are flushed.
	Stop(ctx context.Context) error
}

// Status represents the complete runtime status of a vMCP server.
// This is a platform-agnostic representation that can be mapped to:
//   - VirtualMCPServer.Status (Kubernetes)
//   - File-based state (CLI)
//   - Metrics/observability systems
type Status struct {
	// Phase is the current operational phase of the vMCP server
	Phase Phase

	// Message provides human-readable context about the current phase
	Message string

	// Conditions represent fine-grained status aspects
	Conditions []Condition

	// DiscoveredBackends lists backends discovered by vMCP runtime
	// This is the key field for dynamic mode - vMCP discovers and reports
	DiscoveredBackends []DiscoveredBackend

	// ObservedGeneration tracks which spec generation this status reflects
	// Used for detecting spec changes that require re-reconciliation
	ObservedGeneration int64

	// Timestamp when this status was generated
	Timestamp time.Time
}

// Phase represents the operational lifecycle phase of a vMCP server
type Phase string

const (
	// PhasePending indicates the vMCP server is initializing
	PhasePending Phase = "Pending"

	// PhaseReady indicates the vMCP server is healthy and serving requests
	PhaseReady Phase = "Ready"

	// PhaseDegraded indicates some backends are unavailable but vMCP is still serving
	PhaseDegraded Phase = "Degraded"

	// PhaseFailed indicates the vMCP server has failed and cannot serve requests
	PhaseFailed Phase = "Failed"
)

// Condition represents a specific aspect of vMCP server status
type Condition struct {
	// Type identifies the condition (e.g., "BackendsDiscovered", "Ready", "AuthConfigured")
	Type string

	// Status is the condition state (True, False, Unknown)
	Status metav1.ConditionStatus

	// Reason is a programmatic identifier for the condition state
	Reason string

	// Message is a human-readable explanation
	Message string

	// LastTransitionTime records when the condition last changed
	LastTransitionTime time.Time
}

// DiscoveredBackend represents a backend server discovered by vMCP runtime.
// This mirrors the operator's DiscoveredBackend type but is platform-agnostic.
type DiscoveredBackend struct {
	// Name is the unique identifier for the backend
	Name string

	// URL is the endpoint where the backend can be reached
	URL string

	// Status indicates backend health (Ready, Degraded, Unavailable, Unknown)
	Status BackendStatus

	// AuthConfigRef references the auth configuration used for this backend
	// In Kubernetes: MCPExternalAuthConfig name
	// In CLI: auth profile name
	AuthConfigRef string

	// AuthType indicates the authentication method (oauth2, header_injection, etc.)
	AuthType string

	// LastHealthCheck records when the backend was last checked
	LastHealthCheck time.Time

	// Message provides additional context about the backend status
	Message string
}

// BackendStatus represents the health status of a backend
type BackendStatus string

const (
	// BackendStatusReady indicates the backend is healthy and available
	BackendStatusReady BackendStatus = "Ready"

	// BackendStatusDegraded indicates the backend is partially available
	BackendStatusDegraded BackendStatus = "Degraded"

	// BackendStatusUnavailable indicates the backend is not reachable
	BackendStatusUnavailable BackendStatus = "Unavailable"

	// BackendStatusUnknown indicates the backend status is not yet determined
	BackendStatusUnknown BackendStatus = "Unknown"
)

// Condition type constants for common vMCP conditions
const (
	// ConditionTypeBackendsDiscovered indicates whether backend discovery completed
	ConditionTypeBackendsDiscovered = "BackendsDiscovered"

	// ConditionTypeReady indicates whether the vMCP server is ready to serve requests
	ConditionTypeReady = "Ready"

	// ConditionTypeAuthConfigured indicates whether authentication is properly configured
	ConditionTypeAuthConfigured = "AuthConfigured"
)

// Reason constants for condition reasons
const (
	// ReasonBackendDiscoverySucceeded indicates successful backend discovery
	ReasonBackendDiscoverySucceeded = "BackendDiscoverySucceeded"

	// ReasonBackendDiscoveryFailed indicates backend discovery failed
	ReasonBackendDiscoveryFailed = "BackendDiscoveryFailed"

	// ReasonServerReady indicates the server is ready and healthy
	ReasonServerReady = "ServerReady"

	// ReasonServerStarting indicates the server is starting up
	ReasonServerStarting = "ServerStarting"

	// ReasonServerDegraded indicates some backends are unhealthy
	ReasonServerDegraded = "ServerDegraded"

	// ReasonServerFailed indicates the server has failed
	ReasonServerFailed = "ServerFailed"
)
