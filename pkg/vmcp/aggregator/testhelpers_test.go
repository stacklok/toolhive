// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"github.com/stacklok/toolhive/pkg/vmcp"
)

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
