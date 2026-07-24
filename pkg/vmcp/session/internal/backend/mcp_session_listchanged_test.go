// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptransport "github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// stubListChangedNotifier is a minimal vmcp.BackendListChangedNotifier that
// records every call, safe for concurrent use from a client's receive loop.
type stubListChangedNotifier struct {
	mu    sync.Mutex
	calls []struct {
		backendID string
		kind      vmcp.ListChangedKind
	}
	notify chan struct{}
}

func newStubListChangedNotifier() *stubListChangedNotifier {
	return &stubListChangedNotifier{notify: make(chan struct{}, 8)}
}

func (s *stubListChangedNotifier) NotifyBackendListChanged(backendID string, kind vmcp.ListChangedKind) {
	s.mu.Lock()
	s.calls = append(s.calls, struct {
		backendID string
		kind      vmcp.ListChangedKind
	}{backendID, kind})
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// TestNewHTTPConnector_ContinuousListening verifies that a non-nil listChanged
// makes the streamable-HTTP connector open the standalone continuous-listening
// stream (needed to receive an idle list_changed notification), while a nil
// listChanged reproduces the pre-list_changed construction (no continuous
// listening).
func TestNewHTTPConnector_ContinuousListening(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		listChanged vmcp.BackendListChangedNotifier
		want        bool
	}{
		{"nil listChanged: no continuous listening", nil, false},
		{"bound listChanged: continuous listening enabled", newStubListChangedNotifier(), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fb := &fakeBackend{advertiseTools: true, tools: []mcp.Tool{{Name: "echo"}}}
			url := newFakeBackend(t, fb)
			target := &vmcp.BackendTarget{
				WorkloadID:    "backend-1",
				WorkloadName:  "backend-1",
				BaseURL:       url,
				TransportType: "streamable-http",
			}

			connector := NewHTTPConnector(newTestRegistry(t), tt.listChanged)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			sess, _, err := connector(ctx, target, nil, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = sess.Close() })

			ms, ok := sess.(*mcpSession)
			require.True(t, ok)
			st, ok := ms.client.GetTransport().(*mcptransport.StreamableHTTP)
			require.True(t, ok)
			assert.Equal(t, tt.want, st.ContinuousListening())
		})
	}
}

// Real idle-path delivery (a backend mutating its OWN tool set with no call in
// flight, and vMCP's persistent connector receiving the resulting
// notifications/tools/list_changed) is covered end-to-end at the server layer
// by TestListChanged_IdleMutate_RealBackend, which drives a raw go-sdk backend
// (bypassing the mcpcompat shim, whose MCPServer.AddTool only feeds the ONE
// gosdk.Server built lazily by StreamableHTTPServer and has no supported way to
// mutate it afterwards) — the same reason this package does not duplicate that
// scenario at the connector level.
