package aggregator

import (
	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/workloads/k8s"
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

// K8s workload test helpers

func newTestK8SWorkload(name string, opts ...func(*k8s.Workload)) k8s.Workload {
	w := k8s.Workload{
		Name:          name,
		Namespace:     "default",
		Phase:         mcpv1alpha1.MCPServerPhaseRunning,
		URL:           "http://localhost:8080/mcp",
		TransportType: types.TransportTypeStreamableHTTP,
		ToolType:      "mcp",
		Group:         testGroupName,
		GroupRef:      testGroupName,
		Labels:        make(map[string]string),
	}
	for _, opt := range opts {
		opt(&w)
	}
	return w
}

func withK8SPhase(phase mcpv1alpha1.MCPServerPhase) func(*k8s.Workload) {
	return func(w *k8s.Workload) {
		w.Phase = phase
	}
}

func withK8SURL(url string) func(*k8s.Workload) {
	return func(w *k8s.Workload) {
		w.URL = url
	}
}

func withK8STransport(transport types.TransportType) func(*k8s.Workload) {
	return func(w *k8s.Workload) {
		w.TransportType = transport
	}
}

func withK8SToolType(toolType string) func(*k8s.Workload) {
	return func(w *k8s.Workload) {
		w.ToolType = toolType
	}
}

func withK8SLabels(labels map[string]string) func(*k8s.Workload) {
	return func(w *k8s.Workload) {
		w.Labels = labels
	}
}

func withK8SNamespace(namespace string) func(*k8s.Workload) {
	return func(w *k8s.Workload) {
		w.Namespace = namespace
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
