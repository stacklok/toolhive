// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"
)

func TestMapActionToMCPMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		action   string
		expected string
	}{
		{name: "call_tool maps to tools/call", action: "call_tool", expected: "tools/call"},
		{name: "read_resource maps to resources/read", action: "read_resource", expected: "resources/read"},
		{name: "get_prompt maps to prompts/get", action: "get_prompt", expected: "prompts/get"},
		{name: "unknown action passes through", action: "list_capabilities", expected: "list_capabilities"},
		{name: "empty string passes through", action: "", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapActionToMCPMethod(tt.action)
			if got != tt.expected {
				t.Errorf("mapActionToMCPMethod(%q) = %q, want %q", tt.action, got, tt.expected)
			}
		})
	}
}

func TestMapTransportTypeToNetworkTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType string
		expected      string
	}{
		{name: "stdio maps to pipe", transportType: "stdio", expected: "pipe"},
		{name: "sse maps to tcp", transportType: "sse", expected: "tcp"},
		{name: "streamable-http maps to tcp", transportType: "streamable-http", expected: "tcp"},
		{name: "unknown defaults to tcp", transportType: "unknown", expected: "tcp"},
		{name: "empty defaults to tcp", transportType: "", expected: "tcp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapTransportTypeToNetworkTransport(tt.transportType)
			if got != tt.expected {
				t.Errorf("mapTransportTypeToNetworkTransport(%q) = %q, want %q", tt.transportType, got, tt.expected)
			}
		})
	}
}
