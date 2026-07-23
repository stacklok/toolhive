// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

var _ = time.Second

// §2.3 #1: injection success → exactly one Inject line with all fields and
// no token substring anywhere.
func TestAudit_InjectEvent(t *testing.T) {
	t.Parallel()
	h, audit := newCaptureAudit()

	audit.Inject(context.Background(), egressbroker.InjectEvent{
		MCPServer: "github-mcp",
		Pod:       "pod-abc",
		Subject:   "user-123",
		Provider:  "github",
		Host:      "api.github.com",
		Method:    "GET",
		Path:      "/repos/foo",
	})

	lines := h.byMsg("egressbroker credential injected")
	require.Len(t, lines, 1, "exactly one Inject line")
	rec := lines[0]
	assert.Equal(t, "github-mcp", rec.attrs["mcpserver"])
	assert.Equal(t, "pod-abc", rec.attrs["pod"])
	assert.Equal(t, "github", rec.attrs["provider"])
	assert.Equal(t, "api.github.com", rec.attrs["host"])
	assert.Equal(t, "GET", rec.attrs["method"])
	assert.Equal(t, "/repos/foo", rec.attrs["path"])
	// Sub appears hashed only: 16 hex chars, never the raw value.
	assert.Regexp(t, `^[0-9a-f]{16}$`, rec.attrs["sub_hash"])
	assert.NotContains(t, rec.rawLog, "user-123", "raw sub must never reach the log line")
	// No token material ever (the event struct cannot even carry it).
	assert.NotContains(t, rec.rawLog, testToken)
	// Timestamp present (the ts attr carries a parsed time).
	assert.NotEmpty(t, rec.attrs["ts"])
}

// §2.3 #2: each deny reason → one Deny line with the correct reason enum.
func TestAudit_DenyEvents(t *testing.T) {
	t.Parallel()
	reasons := []egressbroker.DenyReason{
		egressbroker.DenyReasonNoPolicy,
		egressbroker.DenyReasonMethodNotAllowed,
		egressbroker.DenyReasonPathNotAllowed,
		egressbroker.DenyReasonCredentialUnavailable,
		egressbroker.DenyReasonBindingMismatch,
		egressbroker.DenyReasonStoreError,
		egressbroker.DenyReasonMalformed,
	}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			h, audit := newCaptureAudit()
			audit.Deny(context.Background(), egressbroker.DenyEvent{
				InjectEvent: egressbroker.InjectEvent{
					MCPServer: "github-mcp",
					Pod:       "pod-abc",
					Subject:   "user-123",
					Provider:  "github",
					Host:      "api.github.com",
					Method:    "GET",
					Path:      "/",
				},
				Reason: reason,
			})
			lines := h.byMsg("egressbroker injection denied")
			require.Len(t, lines, 1)
			assert.Equal(t, string(reason), lines[0].attrs["reason"])
			assert.Equal(t, "github-mcp", lines[0].attrs["mcpserver"])
			assert.NotContains(t, lines[0].rawLog, "user-123")
			assert.NotContains(t, lines[0].rawLog, testToken)
		})
	}
}

// §2.3 #3: leak block → one Leak line, no token substring.
func TestAudit_LeakEvent(t *testing.T) {
	t.Parallel()
	h, audit := newCaptureAudit()

	audit.Leak(context.Background(), egressbroker.LeakEvent{
		MCPServer: "github-mcp",
		Provider:  "github",
		Host:      "api.github.com",
		RequestID: "req-77",
		Where:     egressbroker.LeakLocationBody,
	})

	lines := h.byMsg("egressbroker response suppressed: credential echo detected")
	require.Len(t, lines, 1, "exactly one Leak line")
	rec := lines[0]
	assert.Equal(t, "github-mcp", rec.attrs["mcpserver"])
	assert.Equal(t, "github", rec.attrs["provider"])
	assert.Equal(t, "api.github.com", rec.attrs["host"])
	assert.Equal(t, "req-77", rec.attrs["request_id"])
	assert.Equal(t, "body", rec.attrs["where"])
	// The event has no token fields at all; belt-and-suspenders substring
	// guard against future field additions.
	assert.NotContains(t, rec.rawLog, testToken)
	assert.NotContains(t, rec.rawLog, "user-123")
}

// §2.3 #4: concurrent emissions → no interleave/corruption, no race.
func TestAudit_ConcurrentEmissions(t *testing.T) {
	t.Parallel()
	h, audit := newCaptureAudit()

	const emitters = 32
	const perEmitter = 25
	var wg sync.WaitGroup
	wg.Add(emitters)
	for i := 0; i < emitters; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perEmitter; j++ {
				audit.Inject(context.Background(), egressbroker.InjectEvent{
					MCPServer: "github-mcp",
					Subject:   fmt.Sprintf("user-%d", i),
					Provider:  "github",
					Host:      "api.github.com",
					Method:    "GET",
					Path:      "/",
				})
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent audit emissions")
	}

	lines := h.byMsg("egressbroker credential injected")
	assert.Len(t, lines, emitters*perEmitter, "every emission lands exactly once")
	for _, rec := range lines {
		assert.Regexp(t, `^[0-9a-f]{16}$`, rec.attrs["sub_hash"], "no torn/corrupted records")
	}
}

// TestAudit_SubjectHashIsCorrelationStable proves the hash form is stable
// (same sub → same hash) and distinguishes subs.
func TestAudit_SubjectHashIsCorrelationStable(t *testing.T) {
	t.Parallel()
	a := egressbroker.SubjectHash("user-123")
	b := egressbroker.SubjectHash("user-123")
	c := egressbroker.SubjectHash("user-456")
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Len(t, a, 16)
}

// TestAudit_NilHandlerFailsLoudly guards the constructor-validation rule.
func TestAudit_NilHandlerFailsLoudly(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { egressbroker.NewAuditLoggerWith(nil) })
}

// TestAuditEvent_BuilderFields covers the shared event builder used by the
// injector (pod name optional; sub carried raw only until the log call).
func TestAuditEvent_BuilderFields(t *testing.T) {
	t.Parallel()
	e := egressbroker.AuditEvent(testIdentity, "pod-abc",
		egressbroker.Destination{Host: "api.github.com", Method: "GET", Path: "/x"}, "github")
	assert.Equal(t, "github-mcp", e.MCPServer)
	assert.Equal(t, "pod-abc", e.Pod)
	assert.Equal(t, "user-123", e.Subject)
	assert.Equal(t, "github", e.Provider)
	assert.Equal(t, "api.github.com", e.Host)
}
