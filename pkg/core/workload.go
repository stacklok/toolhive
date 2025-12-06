// Package core provides the core domain model for the ToolHive system.
package core

import (
	"sort"
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
	// TransportType is the type of transport used for this workload.
	TransportType types.TransportType `json:"transport_type"`
	// ProxyMode is the proxy mode that clients should use to connect.
	// For stdio transports, this will be the proxy mode (sse or streamable-http).
	// For direct transports (sse/streamable-http), this will be the same as TransportType.
	ProxyMode string `json:"proxy_mode,omitempty"`
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
	// Remote indicates whether this is a remote workload (true) or a container workload (false).
	Remote bool `json:"remote,omitempty"`
}

// SortWorkloadsByName sorts a slice of Workload by the Name field in ascending alphabetical order.
func SortWorkloadsByName(workloads []Workload) {
	sort.Slice(workloads, func(i, j int) bool {
		return workloads[i].Name < workloads[j].Name
	})
}
