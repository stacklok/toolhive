// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backendtelemetry

import (
	"context"
	"testing"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// fakeRevClient embeds vmcp.BackendClient (nil — its methods are never called
// here) and adds the optional CachedRevision accessor.
type fakeRevClient struct {
	vmcp.BackendClient
	rev mcpparser.Revision
	ok  bool
}

func (f fakeRevClient) CachedRevision(string) (mcpparser.Revision, bool) { return f.rev, f.ok }

// fakeNoRevClient embeds vmcp.BackendClient but does NOT implement revisionReporter.
type fakeNoRevClient struct{ vmcp.BackendClient }

// TestTelemetryBackendClient_CachedRevisionForwarding verifies the decorator
// forwards CachedRevision to a client that reports it, and reports nothing for a
// client that doesn't.
func TestTelemetryBackendClient_CachedRevisionForwarding(t *testing.T) {
	t.Parallel()

	d := telemetryBackendClient{backendClient: fakeRevClient{rev: mcpparser.RevisionModern, ok: true}}
	rev, ok := d.CachedRevision("b")
	if !ok || rev != mcpparser.RevisionModern {
		t.Fatalf("CachedRevision = (%v, %v), want (Modern, true)", rev, ok)
	}
	if got := d.revisionLabel("b"); got != "2026-07-28" {
		t.Errorf("revisionLabel = %q, want 2026-07-28", got)
	}

	dn := telemetryBackendClient{backendClient: fakeNoRevClient{}}
	if _, ok := dn.CachedRevision("b"); ok {
		t.Error("CachedRevision should report false for a client without the accessor")
	}
	if got := dn.revisionLabel("b"); got != "" {
		t.Errorf("revisionLabel = %q, want empty for unprobed/unsupported", got)
	}
}

// TestRecordRevisionReclassification is a smoke test: the counter lazily binds to
// the global meter provider and increments without panicking (the noop provider
// makes the value unobservable here — the WARN in the same reclassify branch is
// asserted in the client package's reclassify test).
func TestRecordRevisionReclassification(t *testing.T) {
	t.Parallel()
	RecordRevisionReclassification(context.Background())
	RecordRevisionReclassification(context.Background())
}

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
