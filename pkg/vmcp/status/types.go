// Package status privdes status reporting capabilities for Virtual MCP servers.
// It will enable vMCP runtime to report operational state to different platforms
// Kubernetes, local CLI through a common interface

package status

import "time"

type Phase string

const (

	// PhaseReady it will indicate the server is running and healthy
	PhaseReady Phase = "Ready"

	// PhaseDegraded indicates the server is running but some backends are unhealthy
	PhaseDegraded Phase = "Degraded"

	// Phase Failed will indicate if the server has failed and cannot serve requests
	PhaseFailed Phase = "Failed"

	// PhasePending indicates the server is starting up
	PhasePending Phase = "Pending"
)

// BackendHealthReport contains health information for a single backend.
type BackendHealthReport struct {
	Name        string
	Healthy     bool
	Message     string
	LastChecked time.Time
}

// RunTimeStatus it will represent the current operational status of the Virual MCP server
// Data structure that will be reported to Kubernates or logged locally

type RuntimeStatus struct {
	Phase   Phase
	Message string
	Backends []BackendHealthReport

	TotalToolCount    int
	HealthyBackends   int
	UnhealthyBackends int
	LastDiscoveryTime time.Time
}
