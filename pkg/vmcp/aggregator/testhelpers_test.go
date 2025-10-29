package aggregator

import (
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Test fixture builders to reduce verbosity in tests

func newTestWorkload(name string, opts ...func(*core.Workload)) core.Workload {
	w := core.Workload{
		Name:          name,
		Status:        runtime.WorkloadStatusRunning,
		URL:           "http://localhost:8080/mcp",
		TransportType: types.TransportTypeStreamableHTTP,
		Group:         testGroupName,
	}
	for _, opt := range opts {
		opt(&w)
	}
	return w
}

func withStatus(status runtime.WorkloadStatus) func(*core.Workload) {
	return func(w *core.Workload) {
		w.Status = status
	}
}

func withURL(url string) func(*core.Workload) {
	return func(w *core.Workload) {
		w.URL = url
	}
}

func withTransport(transport types.TransportType) func(*core.Workload) {
	return func(w *core.Workload) {
		w.TransportType = transport
	}
}

func withToolType(toolType string) func(*core.Workload) {
	return func(w *core.Workload) {
		w.ToolType = toolType
	}
}

func withLabels(labels map[string]string) func(*core.Workload) {
	return func(w *core.Workload) {
		w.Labels = labels
	}
}

func newTestBackend(id string, opts ...func(*vmcp.Backend)) vmcp.Backend {
	b := vmcp.Backend{
		ID:            id,
		Name:          id,
		BaseURL:       "http://localhost:8080",
		TransportType: "streamable-http",
		HealthStatus:  vmcp.BackendHealthy,
	}
	for _, opt := range opts {
		opt(&b)
	}
	return b
}

func withBackendURL(url string) func(*vmcp.Backend) {
	return func(b *vmcp.Backend) {
		b.BaseURL = url
	}
}

func withBackendTransport(transport string) func(*vmcp.Backend) {
	return func(b *vmcp.Backend) {
		b.TransportType = transport
	}
}

func withBackendName(name string) func(*vmcp.Backend) {
	return func(b *vmcp.Backend) {
		b.Name = name
	}
}

func newTestCapabilityList(opts ...func(*vmcp.CapabilityList)) *vmcp.CapabilityList {
	caps := &vmcp.CapabilityList{
		Tools:            []vmcp.Tool{},
		Resources:        []vmcp.Resource{},
		Prompts:          []vmcp.Prompt{},
		SupportsLogging:  false,
		SupportsSampling: false,
	}
	for _, opt := range opts {
		opt(caps)
	}
	return caps
}

func withTools(tools ...vmcp.Tool) func(*vmcp.CapabilityList) {
	return func(c *vmcp.CapabilityList) {
		c.Tools = tools
	}
}

func withResources(resources ...vmcp.Resource) func(*vmcp.CapabilityList) {
	return func(c *vmcp.CapabilityList) {
		c.Resources = resources
	}
}

func withPrompts(prompts ...vmcp.Prompt) func(*vmcp.CapabilityList) {
	return func(c *vmcp.CapabilityList) {
		c.Prompts = prompts
	}
}

func withLogging(enabled bool) func(*vmcp.CapabilityList) {
	return func(c *vmcp.CapabilityList) {
		c.SupportsLogging = enabled
	}
}

func withSampling(enabled bool) func(*vmcp.CapabilityList) {
	return func(c *vmcp.CapabilityList) {
		c.SupportsSampling = enabled
	}
}

func newTestTool(name, backendID string) vmcp.Tool {
	return vmcp.Tool{
		Name:        name,
		Description: name + " description",
		InputSchema: map[string]any{"type": "object"},
		BackendID:   backendID,
	}
}

func newTestResource(uri, backendID string) vmcp.Resource {
	return vmcp.Resource{
		URI:       uri,
		Name:      uri,
		BackendID: backendID,
	}
}

func newTestPrompt(name, backendID string) vmcp.Prompt {
	return vmcp.Prompt{
		Name:      name,
		BackendID: backendID,
	}
}
