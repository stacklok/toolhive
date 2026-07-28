// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

// TestInitOneBackend_RevisionAwareSkip verifies that WithRevisionLookup only
// skips the connector for a backend the lookup confirms as Modern; an
// unprobed backend, a known-Legacy backend, or the absence of a lookup at all
// must reproduce today's unconditional connect.
func TestInitOneBackend_RevisionAwareSkip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		lookup           func(string) (mcpparser.Revision, bool)
		wantConnectorHit bool
	}{
		{
			name:             "no lookup configured: connector invoked",
			wantConnectorHit: true,
		},
		{
			name:             "unprobed backend (cache miss): connector invoked",
			lookup:           func(string) (mcpparser.Revision, bool) { return mcpparser.RevisionLegacy, false },
			wantConnectorHit: true,
		},
		{
			name:             "known Legacy: connector invoked",
			lookup:           func(string) (mcpparser.Revision, bool) { return mcpparser.RevisionLegacy, true },
			wantConnectorHit: true,
		},
		{
			name:             "known Modern: connector not invoked",
			lookup:           func(string) (mcpparser.Revision, bool) { return mcpparser.RevisionModern, true },
			wantConnectorHit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var hit bool
			connector := func(
				_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity, _ string, _ internalbk.ListChangedSink,
			) (internalbk.Session, *vmcp.CapabilityList, error) {
				hit = true
				return &mockConnectedBackend{sessID: "sess-" + target.WorkloadID}, &vmcp.CapabilityList{}, nil
			}

			var opts []MultiSessionFactoryOption
			if tt.lookup != nil {
				opts = append(opts, WithRevisionLookup(tt.lookup))
			}
			factory := newSessionFactoryWithConnector(connector, opts...)

			sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, []*vmcp.Backend{{ID: "backend-a"}}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = sess.Close() })

			assert.Equal(t, tt.wantConnectorHit, hit)
		})
	}
}

// TestInitOneBackend_ColdStartRecheckAvoidsWarning pins the post-error
// re-check on initOneBackend: the backend is unprobed at the pre-connect
// check, the connect is attempted and fails, and only the POST-failure
// re-check resolves Modern. That must log at DEBUG rather than the
// real-failure WARN, and must not attempt a second connect on this call.
//
// This models the cold-start race a real Modern backend loses: its connect
// genuinely does fail (server/discover succeeds, then subscriptions/listen
// cannot be served by a session-less backend — see initOneBackend's doc
// comment), so the error branch here is the ordinary Modern path, not an
// exotic one. The fake connector returns a generic error because this test is
// about the re-check's log-level decision, not about reproducing go-sdk's
// exact failure chain.
//
// The lookup's call-counter is a plain int, not atomic: this session has a
// single backend, so a single per-backend goroutine makes both lookup calls,
// in program order, with no other goroutine reading the counter. The test
// goroutine only reads it after MakeSessionWithID returns, which happens
// after that goroutine completes (via sync.WaitGroup in makeBaseSession) —
// safe under the race detector without extra synchronization.
//
//nolint:paralleltest // setupLogRecorder swaps the global slog default logger; see its doc comment.
func TestInitOneBackend_ColdStartRecheckAvoidsWarning(t *testing.T) {
	buf := setupLogRecorder(t)

	var lookupCalls int
	lookup := func(string) (mcpparser.Revision, bool) {
		lookupCalls++
		if lookupCalls == 1 {
			return mcpparser.RevisionLegacy, false // pre-connect check: unprobed
		}
		return mcpparser.RevisionModern, true // post-failure re-check: resolved Modern
	}

	var connectorCalls int
	connector := func(
		context.Context, *vmcp.BackendTarget, *auth.Identity, string, internalbk.ListChangedSink,
	) (internalbk.Session, *vmcp.CapabilityList, error) {
		connectorCalls++
		return nil, nil, errors.New("dial tcp: connection refused")
	}

	factory := newSessionFactoryWithConnector(connector, WithRevisionLookup(lookup))
	sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, []*vmcp.Backend{{ID: "backend-a"}}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	assert.Equal(t, 1, connectorCalls, "the connect must still be attempted when the backend is unprobed at the pre-check")
	assert.NotContains(t, buf.String(), "Failed to initialise backend",
		"a backend that resolves Modern on the post-failure re-check must not log the real-failure WARN")
	// Positive control: without this, downgrading the real-failure WARN to DEBUG
	// would leave the buffer empty and the assertion above would pass silently.
	assert.Contains(t, buf.String(), "is now known Modern",
		"the re-check must log the expected-skip DEBUG line, proving the buffer captures at all")
}

// setupLogRecorder swaps the global slog default logger for a text handler
// writing to the returned buffer, for the duration of the calling test, and
// restores it on cleanup. Matches the established repo pattern (see
// pkg/vmcp/core/core_vmcp_test.go, pkg/vmcp/client/reclassify_test.go).
//
// Not compatible with t.Parallel(): slog's default logger is process-global,
// so a parallel subtest logging concurrently would race this one. Callers
// must not mark themselves (or their subtests) parallel.
func setupLogRecorder(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	// LevelDebug so tests can assert on the DEBUG lines too, not just WARN.
	// Without it a test asserting only the ABSENCE of a WARN would still pass
	// if that WARN were downgraded to DEBUG — it needs a positive control.
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// allBackendsFailedMsg pins the substring of makeBaseSession's no-session-held
// warning. Kept as the stable prefix rather than the whole line so a wording
// tweak does not silently turn the NotContains assertion below into a vacuous
// pass — the assertion that no warning fired is only meaningful while this
// substring still matches the message the code emits.
const allBackendsFailedMsg = "No backend held a session for this session"

// TestMakeSession_AllBackendsFailedWarning verifies that the "all backends
// failed" warning fires only when a real failure is present — an all-Modern
// backend set is a deliberate, expected zero-connection outcome, not a
// failure, while mixing a Modern-skip with one genuine failure still warns.
//
//nolint:paralleltest // setupLogRecorder swaps the global slog default logger; see its doc comment.
func TestMakeSession_AllBackendsFailedWarning(t *testing.T) {
	tests := []struct {
		name     string
		lookup   func(workloadID string) (mcpparser.Revision, bool)
		backends []*vmcp.Backend
		wantWarn bool
	}{
		{
			name:     "all backends skipped as Modern: no warning",
			lookup:   func(string) (mcpparser.Revision, bool) { return mcpparser.RevisionModern, true },
			backends: []*vmcp.Backend{{ID: "backend-a"}, {ID: "backend-b"}},
			wantWarn: false,
		},
		{
			name: "Modern skip mixed with a genuine failure: still warns",
			lookup: func(workloadID string) (mcpparser.Revision, bool) {
				if workloadID == "backend-modern" {
					return mcpparser.RevisionModern, true
				}
				return mcpparser.RevisionLegacy, false
			},
			backends: []*vmcp.Backend{{ID: "backend-modern"}, {ID: "backend-fail"}},
			wantWarn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := setupLogRecorder(t)
			factory := newSessionFactoryWithConnector(nilBackendConnector(), WithRevisionLookup(tt.lookup))

			sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, tt.backends, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = sess.Close() })

			if tt.wantWarn {
				assert.Contains(t, buf.String(), allBackendsFailedMsg)
			} else {
				assert.NotContains(t, buf.String(), allBackendsFailedMsg)
			}
		})
	}
}
