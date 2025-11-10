// Package k8s provides Kubernetes-specific domain models for workloads.
package k8s

import (
	"time"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Workload represents a Kubernetes workload (MCPServer CRD).
// This is the Kubernetes-specific domain model, separate from core.Workload.
type Workload struct {
	// Name is the name of the MCPServer CRD
	Name string
	// Namespace is the Kubernetes namespace where the MCPServer is deployed
	Namespace string
	// Package specifies the container image used for this workload
	Package string
	// URL is the URL of the workload exposed by the ToolHive proxy
	URL string
	// Port is the port on which the workload is exposed
	Port int
	// ToolType is the type of tool this workload represents
	// For now, it will always be "mcp" - representing an MCP server
	ToolType string
	// TransportType is the type of transport used for this workload
	TransportType types.TransportType
	// ProxyMode is the proxy mode that clients should use to connect
	ProxyMode string
	// Phase is the current phase of the MCPServer CRD
	Phase mcpv1alpha1.MCPServerPhase
	// StatusContext provides additional context about the workload's status
	StatusContext string
	// CreatedAt is the timestamp when the workload was created
	CreatedAt time.Time
	// Labels are user-defined labels (from annotations)
	Labels map[string]string
	// Group is the name of the group this workload belongs to, if any
	Group string
	// ToolsFilter is the filter on tools applied to the workload
	ToolsFilter []string
	// GroupRef is the reference to the MCPGroup (same as Group, but using CRD terminology)
	GroupRef string
}
