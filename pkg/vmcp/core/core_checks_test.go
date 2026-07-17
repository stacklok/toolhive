// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// checkCore builds a *coreVMCP wired with the given admission and the base mocks,
// bypassing New so tests can inject a specific admission seam directly. The tool
// checks call aggregatedView; resource/prompt checks do not.
func checkCore(m *coreMocks, adm Admission) *coreVMCP {
	return &coreVMCP{
		aggregator:      m.agg,
		backendRegistry: m.reg,
		backendClient:   m.client,
		admission:       adm,
	}
}

// expectAggregationAnyTimes wires reg.List + agg.AggregateCapabilities to return
// agg on every call (the check/call pairing aggregates more than once).
func expectAggregationAnyTimes(m *coreMocks, agg *aggregator.AggregatedCapabilities) {
	m.reg.EXPECT().List(gomock.Any()).
		Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}}).AnyTimes()
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(agg, nil).AnyTimes()
}

// isAuthErr reports whether err is an authorization denial (nil is not).
func isAuthErr(err error) bool { return errors.Is(err, vmcp.ErrAuthorizationFailed) }

func TestCheckToolCall_AllowDenyFailClosed(t *testing.T) {
	t.Parallel()
	_, m := baseConfig(t)

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"echo":   {authorized: true},
		"boom":   {err: errors.New("authorizer exploded")},
		"denied": {authorized: false},
	}}
	c := checkCore(m, newCedarAdmission(authorizer))
	expectAggregationAnyTimes(m, &aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("echo"), backendTool("boom"), backendTool("denied")},
		RoutingTable: &vmcp.RoutingTable{},
	})

	require.NoError(t, c.CheckToolCall(t.Context(), cedarIdentity(), "echo", nil),
		"an allowed tool call must pass the pre-flight check")

	err := c.CheckToolCall(t.Context(), cedarIdentity(), "denied", nil)
	assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed, "a denied tool call must fail the check")

	err = c.CheckToolCall(t.Context(), cedarIdentity(), "boom", nil)
	assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed, "an authorizer error must fail closed as an authz denial")
}

// TestCheckToolCall_AggregationError_NotAuthzDenial pins the infra-error branch:
// when aggregatedView fails (a transport/plumbing fault, e.g. the backend registry
// or aggregator is unavailable), CheckToolCall must return that error UNWRAPPED —
// not classified as ErrAuthorizationFailed. The gate admits on a non-authz error,
// so wrapping a transient aggregation failure as an authz denial would silently
// turn it into a hard HTTP 403 (fail-open infra handling must not become fail-closed
// authz). This is the classification the fake-core gate test cannot exercise.
func TestCheckToolCall_AggregationError_NotAuthzDenial(t *testing.T) {
	t.Parallel()
	_, m := baseConfig(t)

	// Admission is never reached; the aggregation failure short-circuits first.
	c := checkCore(m, newCedarAdmission(&mockAuthorizer{results: map[string]mockResult{}}))

	infraErr := errors.New("aggregator unavailable")
	m.reg.EXPECT().List(gomock.Any()).
		Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}}).AnyTimes()
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(nil, infraErr).AnyTimes()

	err := c.CheckToolCall(t.Context(), cedarIdentity(), "echo", nil)
	require.Error(t, err, "an aggregation failure must surface as an error")
	assert.False(t, isAuthErr(err),
		"a transient aggregation failure must NOT be classified as ErrAuthorizationFailed "+
			"(the gate would otherwise convert an infra fault into a 403)")
}

func TestCheckResourceRead_AllowDenyFailClosed_NoAggregation(t *testing.T) {
	t.Parallel()
	// No aggregation expectations are set: if CheckResourceRead ever aggregated, the
	// gomock controller would fail on the unexpected reg.List/AggregateCapabilities
	// call. This proves the read check has no aggregation dependency.
	_, m := baseConfig(t)

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"res://ok":   {authorized: true},
		"res://boom": {err: errors.New("authorizer exploded")},
	}}
	c := checkCore(m, newCedarAdmission(authorizer))

	require.NoError(t, c.CheckResourceRead(t.Context(), cedarIdentity(), "res://ok"))
	assert.ErrorIs(t, c.CheckResourceRead(t.Context(), cedarIdentity(), "res://denied"),
		vmcp.ErrAuthorizationFailed)
	assert.ErrorIs(t, c.CheckResourceRead(t.Context(), cedarIdentity(), "res://boom"),
		vmcp.ErrAuthorizationFailed, "authorizer error must fail closed")
}

func TestCheckPromptGet_AllowDenyFailClosed_NoAggregation(t *testing.T) {
	t.Parallel()
	_, m := baseConfig(t)

	authorizer := &mockAuthorizer{results: map[string]mockResult{
		"greet": {authorized: true},
		"boom":  {err: errors.New("authorizer exploded")},
	}}
	c := checkCore(m, newCedarAdmission(authorizer))

	require.NoError(t, c.CheckPromptGet(t.Context(), cedarIdentity(), "greet"))
	assert.ErrorIs(t, c.CheckPromptGet(t.Context(), cedarIdentity(), "secret"),
		vmcp.ErrAuthorizationFailed)
	assert.ErrorIs(t, c.CheckPromptGet(t.Context(), cedarIdentity(), "boom"),
		vmcp.ErrAuthorizationFailed, "authorizer error must fail closed")
}

// TestCheckToolCall_AntiDrift is the invariant guard: for the same (policy, identity,
// name, args), CheckToolCall must fail with ErrAuthorizationFailed if and only if
// CallTool does. The shared authorizeToolCall helper makes this structural, but the
// test pins it — including an argument-conditional Cedar policy so the args flow
// through both routes is proven. An allowed call falls through to routing, which
// returns ErrNotFound (empty routing table) — NOT an authz error — so the invariant
// compares only the authorization outcome, not the transport outcome.
func TestCheckToolCall_AntiDrift(t *testing.T) {
	t.Parallel()

	const permitEcho = `permit(principal, action == Action::"call_tool", resource == Tool::"echo");`
	const forbidBlocked = `forbid(principal, action == Action::"call_tool", resource == Tool::"echo") ` +
		`when { context has arg_input && context.arg_input == "blocked" };`

	tests := []struct {
		name     string
		policies []string
		toolName string
		args     map[string]any
		// wantAuthzDenied is the ABSOLUTE expected outcome. Asserting it (not just that
		// both paths agree) is what proves the args/name are actually consulted: an
		// agreement-only check would still pass if BOTH paths dropped args entirely.
		wantAuthzDenied bool
	}{
		{"unconditional permit", []string{permitEcho}, "echo", nil, false},
		{"default deny unpermitted name", []string{permitEcho}, "other", nil, true},
		{"arg-gated allow", []string{permitEcho, forbidBlocked}, "echo", map[string]any{"input": "hello"}, false},
		{"arg-gated deny", []string{permitEcho, forbidBlocked}, "echo", map[string]any{"input": "blocked"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, m := baseConfig(t)
			c := checkCore(m, cedarAdmissionWith(t, tt.policies...))
			expectAggregationAnyTimes(m, &aggregator.AggregatedCapabilities{
				Tools:        []vmcp.Tool{backendTool("echo")},
				RoutingTable: &vmcp.RoutingTable{},
			})

			checkErr := c.CheckToolCall(t.Context(), cedarIdentity(), tt.toolName, tt.args)
			_, callErr := c.CallTool(t.Context(), cedarIdentity(), tt.toolName, tt.args, nil)

			// Absolute outcome: proves args are consulted (arg-gated deny/allow differ
			// only by the "input" value), not merely that the two paths agree.
			assert.Equal(t, tt.wantAuthzDenied, isAuthErr(checkErr),
				"CheckToolCall authz outcome mismatch (err=%v)", checkErr)
			// Agreement: the shared helper keeps the two routes from drifting.
			assert.Equal(t, isAuthErr(checkErr), isAuthErr(callErr),
				"CheckToolCall and CallTool must agree on the authorization outcome (check=%v call=%v)",
				checkErr, callErr)
		})
	}
}

// TestCheckResourceRead_AntiDrift pairs CheckResourceRead with ReadResource: the
// check errors with ErrAuthorizationFailed iff the read does. ReadResource
// aggregates before authorizing (allowed reads fall through to routing →
// ErrNotFound); the check does not aggregate, so aggregation is wired AnyTimes.
func TestCheckResourceRead_AntiDrift(t *testing.T) {
	t.Parallel()

	cases := []struct {
		uri             string
		wantAuthzDenied bool
	}{
		{"res://ok", false},
		{"res://denied", true},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			t.Parallel()
			// A fresh authorizer per subtest: mockAuthorizer records calls into a shared
			// slice, so a single instance shared across parallel subtests would race.
			authorizer := &mockAuthorizer{results: map[string]mockResult{"res://ok": {authorized: true}}}
			_, m := baseConfig(t)
			c := checkCore(m, newCedarAdmission(authorizer))
			expectAggregationAnyTimes(m, &aggregator.AggregatedCapabilities{RoutingTable: &vmcp.RoutingTable{}})

			checkErr := c.CheckResourceRead(t.Context(), cedarIdentity(), tc.uri)
			_, readErr := c.ReadResource(t.Context(), cedarIdentity(), tc.uri)

			// Absolute outcome (proves the URI is consulted) plus agreement (no drift).
			assert.Equal(t, tc.wantAuthzDenied, isAuthErr(checkErr),
				"CheckResourceRead authz outcome mismatch (err=%v)", checkErr)
			assert.Equal(t, isAuthErr(checkErr), isAuthErr(readErr),
				"CheckResourceRead and ReadResource must agree (check=%v read=%v)", checkErr, readErr)
		})
	}
}

// TestCheckPromptGet_AntiDrift pairs CheckPromptGet with GetPrompt (same invariant).
func TestCheckPromptGet_AntiDrift(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		wantAuthzDenied bool
	}{
		{"greet", false},
		{"secret", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Fresh authorizer per subtest (shared mockAuthorizer records calls → would race).
			authorizer := &mockAuthorizer{results: map[string]mockResult{"greet": {authorized: true}}}
			_, m := baseConfig(t)
			c := checkCore(m, newCedarAdmission(authorizer))
			expectAggregationAnyTimes(m, &aggregator.AggregatedCapabilities{RoutingTable: &vmcp.RoutingTable{}})

			checkErr := c.CheckPromptGet(t.Context(), cedarIdentity(), tc.name)
			_, getErr := c.GetPrompt(t.Context(), cedarIdentity(), tc.name, nil)

			// Absolute outcome (proves the name is consulted) plus agreement (no drift).
			assert.Equal(t, tc.wantAuthzDenied, isAuthErr(checkErr),
				"CheckPromptGet authz outcome mismatch (err=%v)", checkErr)
			assert.Equal(t, isAuthErr(checkErr), isAuthErr(getErr),
				"CheckPromptGet and GetPrompt must agree (check=%v get=%v)", checkErr, getErr)
		})
	}
}
