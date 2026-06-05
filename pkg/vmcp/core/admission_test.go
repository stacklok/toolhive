// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// mockAuthorizer is a hand-rolled stub for the single-method authorizers.Authorizer
// (no generated mock exists; the issue specifies a hand-rolled stub, as
// pkg/authz/tool_filter_test.go does). It records each call — including what it
// observed in the context — so tests can assert the adapter re-injects identity
// and annotations before delegating.
type mockAuthorizer struct {
	results map[string]mockResult // keyed by resourceID
	calls   []mockCall
}

type mockResult struct {
	authorized bool
	err        error
}

type mockCall struct {
	feature         authorizers.MCPFeature
	operation       authorizers.MCPOperation
	resourceID      string
	identitySubject string // "" when no identity was present in ctx
	identityPresent bool
	annotations     *authorizers.ToolAnnotations
}

func (m *mockAuthorizer) AuthorizeWithJWTClaims(
	ctx context.Context,
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	_ map[string]interface{},
) (bool, error) {
	id, present := auth.IdentityFromContext(ctx)
	call := mockCall{
		feature:         feature,
		operation:       operation,
		resourceID:      resourceID,
		identityPresent: present,
		annotations:     authorizers.ToolAnnotationsFromContext(ctx),
	}
	if present && id != nil {
		call.identitySubject = id.Subject
	}
	m.calls = append(m.calls, call)

	r, ok := m.results[resourceID]
	if !ok {
		return false, nil // default-deny for unlisted resources
	}
	return r.authorized, r.err
}

func boolPtr(b bool) *bool { return &b }

// cedarIdentity returns an identity the Cedar authorizer can resolve to a client
// ID, mirroring pkg/authz/tool_filter_test.go's cedarCtx.
func cedarIdentity() *auth.Identity {
	return &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user123",
		Name:    "Test User",
		Claims:  map[string]interface{}{"sub": "user123", "name": "Test User"},
	}}
}

// cedarAdmissionWith builds a cedarAdmission backed by a real Cedar authorizer
// compiled from the given policies (no pass-through tools).
func cedarAdmissionWith(t *testing.T, policies ...string) *cedarAdmission {
	t.Helper()
	a, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{Policies: policies, EntitiesJSON: "[]"}, "")
	require.NoError(t, err)
	return newCedarAdmission(a, nil)
}

// cedarAuthzConfig builds the generic authz.Config New consumes, wrapping the
// given Cedar policies (mirrors factory/incoming.go's newCedarAuthzMiddleware).
func cedarAuthzConfig(t *testing.T, policies ...string) *authorizers.Config {
	t.Helper()
	cfg, err := authorizers.NewConfig(cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{Policies: policies, EntitiesJSON: "[]"},
	})
	require.NoError(t, err)
	return cfg
}

func toolNames(tools []vmcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func TestCedarAdmission_FilterTools(t *testing.T) {
	t.Parallel()

	errAuth := errors.New("authorization failure")

	tests := []struct {
		name      string
		results   map[string]mockResult
		tools     []vmcp.Tool
		wantNames []string
	}{
		{
			name:      "empty tool list returns empty",
			results:   map[string]mockResult{},
			tools:     []vmcp.Tool{},
			wantNames: []string{},
		},
		{
			name: "filters out denied tools",
			results: map[string]mockResult{
				"allowed":  {authorized: true},
				"denied":   {authorized: false},
				"allowed2": {authorized: true},
			},
			tools:     []vmcp.Tool{{Name: "allowed"}, {Name: "denied"}, {Name: "allowed2"}},
			wantNames: []string{"allowed", "allowed2"},
		},
		{
			name: "skips tools whose authorizer errors (log-and-continue)",
			results: map[string]mockResult{
				"good":  {authorized: true},
				"error": {err: errAuth},
			},
			tools:     []vmcp.Tool{{Name: "good"}, {Name: "error"}},
			wantNames: []string{"good"},
		},
		{
			name: "annotated tools are still filtered",
			results: map[string]mockResult{
				"readonly": {authorized: true},
				"writable": {authorized: false},
			},
			tools: []vmcp.Tool{
				{Name: "readonly", Annotations: &vmcp.ToolAnnotations{ReadOnlyHint: boolPtr(true)}},
				{Name: "writable", Annotations: &vmcp.ToolAnnotations{DestructiveHint: boolPtr(true)}},
			},
			wantNames: []string{"readonly"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			adm := newCedarAdmission(&mockAuthorizer{results: tc.results}, nil)
			got, err := adm.FilterTools(context.Background(), cedarIdentity(), tc.tools)
			require.NoError(t, err)
			assert.Equal(t, tc.wantNames, toolNames(got))
		})
	}
}

func TestCedarAdmission_InvokesToolAuthorizerCorrectly(t *testing.T) {
	t.Parallel()

	mock := &mockAuthorizer{results: map[string]mockResult{"hinted": {authorized: true}, "plain": {authorized: true}}}
	adm := newCedarAdmission(mock, nil)

	tools := []vmcp.Tool{
		{Name: "hinted", Annotations: &vmcp.ToolAnnotations{ReadOnlyHint: boolPtr(true)}},
		{Name: "plain"},
	}
	_, err := adm.FilterTools(context.Background(), cedarIdentity(), tools)
	require.NoError(t, err)

	require.Len(t, mock.calls, 2)
	for _, c := range mock.calls {
		assert.Equal(t, authorizers.MCPFeatureTool, c.feature)
		assert.Equal(t, authorizers.MCPOperationCall, c.operation)
		assert.True(t, c.identityPresent, "adapter must re-inject identity into ctx")
		assert.Equal(t, "user123", c.identitySubject)
	}
	// Annotations are injected only for the tool that carries a hint.
	byName := map[string]mockCall{mock.calls[0].resourceID: mock.calls[0], mock.calls[1].resourceID: mock.calls[1]}
	require.NotNil(t, byName["hinted"].annotations)
	assert.Equal(t, boolPtr(true), byName["hinted"].annotations.ReadOnlyHint)
	assert.Nil(t, byName["plain"].annotations, "no annotation ctx written when the tool has no hints")
}

func TestCedarAdmission_AllowToolCall(t *testing.T) {
	t.Parallel()

	errAuth := errors.New("auth error")

	tests := []struct {
		name    string
		results map[string]mockResult
		tool    string
		wantOK  bool
		wantErr error
	}{
		{"allowed", map[string]mockResult{"t": {authorized: true}}, "t", true, nil},
		{"denied", map[string]mockResult{"t": {authorized: false}}, "t", false, nil},
		{"error propagated", map[string]mockResult{"t": {err: errAuth}}, "t", false, errAuth},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock := &mockAuthorizer{results: tc.results}
			adm := newCedarAdmission(mock, nil)
			ok, err := adm.AllowToolCall(context.Background(), cedarIdentity(), &vmcp.Tool{Name: tc.tool}, nil)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, mock.calls, 1)
			assert.Equal(t, authorizers.MCPFeatureTool, mock.calls[0].feature)
			assert.Equal(t, authorizers.MCPOperationCall, mock.calls[0].operation)
			assert.True(t, mock.calls[0].identityPresent)
		})
	}
}

// TestCedarAdmission_PassThroughExempt covers R1(4): optimizer meta-tools
// (find_tool/call_tool) are never denied by the seam, matching their exemption
// through the HTTP middleware today.
func TestCedarAdmission_PassThroughExempt(t *testing.T) {
	t.Parallel()

	passThrough := map[string]struct{}{"find_tool": {}, "call_tool": {}}
	// Authorizer denies everything (empty results -> default deny).
	mock := &mockAuthorizer{results: map[string]mockResult{}}
	adm := newCedarAdmission(mock, passThrough)

	got, err := adm.FilterTools(context.Background(), cedarIdentity(),
		[]vmcp.Tool{{Name: "find_tool"}, {Name: "call_tool"}, {Name: "regular"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"find_tool", "call_tool"}, toolNames(got),
		"pass-through tools survive; the regular tool is denied")

	ok, err := adm.AllowToolCall(context.Background(), cedarIdentity(), &vmcp.Tool{Name: "find_tool"}, nil)
	require.NoError(t, err)
	assert.True(t, ok, "pass-through tool call is exempt")

	// The authorizer is never consulted for pass-through tools.
	for _, c := range mock.calls {
		assert.Equal(t, "regular", c.resourceID)
	}
}

func TestCedarAdmission_FilterResourcesAndPrompts(t *testing.T) {
	t.Parallel()

	t.Run("resources", func(t *testing.T) {
		t.Parallel()
		mock := &mockAuthorizer{results: map[string]mockResult{
			"res-a": {authorized: true},
			"res-b": {authorized: false},
			"res-c": {err: errors.New("boom")}, // skipped
		}}
		adm := newCedarAdmission(mock, nil)
		got, err := adm.FilterResources(context.Background(), cedarIdentity(),
			[]vmcp.Resource{{URI: "res-a"}, {URI: "res-b"}, {URI: "res-c"}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "res-a", got[0].URI)
		assert.Equal(t, authorizers.MCPFeatureResource, mock.calls[0].feature)
		assert.Equal(t, authorizers.MCPOperationRead, mock.calls[0].operation)
	})

	t.Run("prompts", func(t *testing.T) {
		t.Parallel()
		mock := &mockAuthorizer{results: map[string]mockResult{
			"p-a": {authorized: true},
			"p-b": {authorized: false},
		}}
		adm := newCedarAdmission(mock, nil)
		got, err := adm.FilterPrompts(context.Background(), cedarIdentity(),
			[]vmcp.Prompt{{Name: "p-a"}, {Name: "p-b"}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "p-a", got[0].Name)
		assert.Equal(t, authorizers.MCPFeaturePrompt, mock.calls[0].feature)
		assert.Equal(t, authorizers.MCPOperationGet, mock.calls[0].operation)
	})
}

func TestCedarAdmission_AllowResourceReadAndPromptGet(t *testing.T) {
	t.Parallel()

	mock := &mockAuthorizer{results: map[string]mockResult{
		"res-a": {authorized: true},
		"p-a":   {authorized: false},
	}}
	adm := newCedarAdmission(mock, nil)

	ok, err := adm.AllowResourceRead(context.Background(), cedarIdentity(), &vmcp.Resource{URI: "res-a"})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, authorizers.MCPFeatureResource, mock.calls[0].feature)
	assert.Equal(t, authorizers.MCPOperationRead, mock.calls[0].operation)

	ok, err = adm.AllowPromptGet(context.Background(), cedarIdentity(), &vmcp.Prompt{Name: "p-a"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, authorizers.MCPFeaturePrompt, mock.calls[1].feature)
	assert.Equal(t, authorizers.MCPOperationGet, mock.calls[1].operation)
}

// TestCedarAdmission_WithCedarAuthorizer exercises the seam against a real Cedar
// authorizer, mirroring pkg/authz/tool_filter_test.go's Cedar cases, and covers
// the nil-identity edge: the adapter re-injects identity, so ErrMissingPrincipal
// only surfaces for a genuinely nil identity — and filter/call agree (both deny).
func TestCedarAdmission_WithCedarAuthorizer(t *testing.T) {
	t.Parallel()

	adm := cedarAdmissionWith(t,
		`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		`permit(principal, action == Action::"read_resource", resource == Resource::"res-a");`,
		`permit(principal, action == Action::"get_prompt", resource == Prompt::"p-a");`,
	)
	id := cedarIdentity()
	ctx := context.Background()

	t.Run("tools filter and call agree", func(t *testing.T) {
		t.Parallel()
		got, err := adm.FilterTools(ctx, id, []vmcp.Tool{{Name: "weather"}, {Name: "calculator"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"weather"}, toolNames(got))

		ok, err := adm.AllowToolCall(ctx, id, &vmcp.Tool{Name: "weather"}, nil)
		require.NoError(t, err)
		assert.True(t, ok)
		ok, err = adm.AllowToolCall(ctx, id, &vmcp.Tool{Name: "calculator"}, nil)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("resource and prompt parity", func(t *testing.T) {
		t.Parallel()
		ok, err := adm.AllowResourceRead(ctx, id, &vmcp.Resource{URI: "res-a"})
		require.NoError(t, err)
		assert.True(t, ok)
		ok, err = adm.AllowResourceRead(ctx, id, &vmcp.Resource{URI: "res-b"})
		require.NoError(t, err)
		assert.False(t, ok)

		ok, err = adm.AllowPromptGet(ctx, id, &vmcp.Prompt{Name: "p-a"})
		require.NoError(t, err)
		assert.True(t, ok)
		ok, err = adm.AllowPromptGet(ctx, id, &vmcp.Prompt{Name: "p-b"})
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("nil identity denies consistently", func(t *testing.T) {
		t.Parallel()
		// FilterTools skips the tool (per-tool ErrMissingPrincipal -> log-and-continue).
		got, err := adm.FilterTools(ctx, nil, []vmcp.Tool{{Name: "weather"}})
		require.NoError(t, err)
		assert.Empty(t, got)
		// AllowToolCall surfaces the missing-principal error (deny).
		ok, err := adm.AllowToolCall(ctx, nil, &vmcp.Tool{Name: "weather"}, nil)
		require.Error(t, err)
		assert.False(t, ok)
	})
}

// TestCedarAdmission_AnnotationWhenClause covers R1(2): policies keyed on
// annotations (resource.readOnlyHint) evaluate using core-sourced Tool.Annotations
// identically for both the list filter and the call.
func TestCedarAdmission_AnnotationWhenClause(t *testing.T) {
	t.Parallel()

	adm := cedarAdmissionWith(t,
		`permit(principal, action == Action::"call_tool", resource) when { resource.readOnlyHint == true };`)
	id := cedarIdentity()
	ctx := context.Background()

	readOnly := vmcp.Tool{Name: "ro", Annotations: &vmcp.ToolAnnotations{ReadOnlyHint: boolPtr(true)}}
	writable := vmcp.Tool{Name: "rw", Annotations: &vmcp.ToolAnnotations{ReadOnlyHint: boolPtr(false)}}

	got, err := adm.FilterTools(ctx, id, []vmcp.Tool{readOnly, writable})
	require.NoError(t, err)
	assert.Equal(t, []string{"ro"}, toolNames(got), "readOnlyHint-gated allow keeps only the read-only tool")

	ok, err := adm.AllowToolCall(ctx, id, &readOnly, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = adm.AllowToolCall(ctx, id, &writable, nil)
	require.NoError(t, err)
	assert.False(t, ok, "the call enforces the same readOnlyHint decision as the filter")
}

// TestNewAdmission covers construction and R1(3): a nil Authz yields an allow-all
// seam (every Filter* returns its input unchanged, every Allow* returns true).
func TestNewAdmission(t *testing.T) {
	t.Parallel()

	t.Run("nil config is allow-all", func(t *testing.T) {
		t.Parallel()
		adm, err := newAdmission(nil, "", nil)
		require.NoError(t, err)
		ctx := context.Background()

		tools := []vmcp.Tool{{Name: "a"}, {Name: "b"}}
		gotTools, err := adm.FilterTools(ctx, nil, tools)
		require.NoError(t, err)
		assert.Equal(t, tools, gotTools)

		resources := []vmcp.Resource{{URI: "r"}}
		gotRes, err := adm.FilterResources(ctx, nil, resources)
		require.NoError(t, err)
		assert.Equal(t, resources, gotRes)

		prompts := []vmcp.Prompt{{Name: "p"}}
		gotPrompts, err := adm.FilterPrompts(ctx, nil, prompts)
		require.NoError(t, err)
		assert.Equal(t, prompts, gotPrompts)

		ok, err := adm.AllowToolCall(ctx, nil, &vmcp.Tool{Name: "a"}, nil)
		require.NoError(t, err)
		assert.True(t, ok)
		ok, err = adm.AllowResourceRead(ctx, nil, &vmcp.Resource{URI: "r"})
		require.NoError(t, err)
		assert.True(t, ok)
		ok, err = adm.AllowPromptGet(ctx, nil, &vmcp.Prompt{Name: "p"})
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("cedar config builds an enforcing seam", func(t *testing.T) {
		t.Parallel()
		adm, err := newAdmission(
			cedarAuthzConfig(t, `permit(principal, action == Action::"call_tool", resource == Tool::"weather");`),
			"", nil)
		require.NoError(t, err)

		ok, err := adm.AllowToolCall(context.Background(), cedarIdentity(), &vmcp.Tool{Name: "weather"}, nil)
		require.NoError(t, err)
		assert.True(t, ok)
		ok, err = adm.AllowToolCall(context.Background(), cedarIdentity(), &vmcp.Tool{Name: "calculator"}, nil)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("unsupported authz type errors", func(t *testing.T) {
		t.Parallel()
		bad, err := authorizers.NewConfig(struct {
			Version string `json:"version"`
			Type    string `json:"type"`
		}{Version: "1.0", Type: "definitely-not-registered"})
		require.NoError(t, err)

		_, err = newAdmission(bad, "", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, vmcp.ErrInvalidConfig)
	})
}

// TestAdmission_ListCallLookupEnforceSameDecision covers R1(1) end-to-end through
// the core: a tool denied by Cedar is omitted from ListTools, refused by CallTool,
// and unresolvable via LookupTool — closing the "list says yes / call says no" gap.
func TestAdmission_ListCallLookupEnforceSameDecision(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	cfg.Authz = cedarAuthzConfig(t,
		`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`)

	rt := &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"weather": backendTarget()}}
	m.reg.EXPECT().List(gomock.Any()).
		Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}}).AnyTimes()
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("weather"), backendTool("calculator")},
		RoutingTable: rt,
	}, nil).AnyTimes()

	// Only the permitted tool ever reaches the backend.
	want := &vmcp.ToolCallResult{StructuredContent: map[string]any{"ok": true}}
	m.client.EXPECT().CallTool(gomock.Any(), gomock.Any(), "weather", gomock.Any(), gomock.Any()).Return(want, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	id := cedarIdentity()

	// List: the denied tool is omitted.
	tools, err := c.ListTools(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"weather"}, toolNames(tools))

	// Call: the permitted tool routes; the denied tool is refused (same decision).
	got, err := c.CallTool(ctx, id, "weather", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	_, err = c.CallTool(ctx, id, "calculator", nil, nil)
	assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)

	// Lookup: the denied tool is unresolvable; the permitted one resolves.
	_, err = c.LookupTool(ctx, id, "calculator")
	assert.ErrorIs(t, err, vmcp.ErrNotFound)
	tool, err := c.LookupTool(ctx, id, "weather")
	require.NoError(t, err)
	assert.Equal(t, "weather", tool.Name)
}

// TestAdmission_ResourceAndPromptDenyThroughCore confirms the deny wiring is
// present on the resource and prompt call paths (the seam refuses before routing,
// so no backend client call is made).
func TestAdmission_ResourceAndPromptDenyThroughCore(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	cfg.Authz = cedarAuthzConfig(t,
		`permit(principal, action == Action::"read_resource", resource == Resource::"res-a");`,
		`permit(principal, action == Action::"get_prompt", resource == Prompt::"p-a");`,
	)

	m.reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Resources:    []vmcp.Resource{{URI: "res-a", BackendID: testBackendID}, {URI: "res-b", BackendID: testBackendID}},
		Prompts:      []vmcp.Prompt{{Name: "p-a", BackendID: testBackendID}, {Name: "p-b", BackendID: testBackendID}},
		RoutingTable: &vmcp.RoutingTable{},
	}, nil).AnyTimes()

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	id := cedarIdentity()

	resources, err := c.ListResources(ctx, id)
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "res-a", resources[0].URI)

	_, err = c.ReadResource(ctx, id, "res-b")
	assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)

	prompts, err := c.ListPrompts(ctx, id)
	require.NoError(t, err)
	require.Len(t, prompts, 1)
	assert.Equal(t, "p-a", prompts[0].Name)

	_, err = c.GetPrompt(ctx, id, "p-b", nil)
	assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)
}

// TestAdmission_IdentityNeverLogged covers R1(5): the seam passes *auth.Identity
// through unchanged and never logs identity/token material, even on the per-tool
// skip path. Serial: it swaps the global slog default into a non-thread-safe buffer.
//
//nolint:paralleltest // installs a global slog default + non-thread-safe buffer; must not run in parallel
func TestAdmission_IdentityNeverLogged(t *testing.T) {
	const secret = "super-secret-token-value"

	// An erroring authorizer drives the FilterTools skip-warning so the assertion
	// is not vacuous.
	mock := &mockAuthorizer{results: map[string]mockResult{"boom": {err: errors.New("policy eval failed")}}}
	adm := newCedarAdmission(mock, nil)

	id := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "user", Claims: map[string]interface{}{"secret": secret}},
		Token:          secret,
		UpstreamTokens: map[string]string{"github": secret},
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	_, _ = adm.FilterTools(context.Background(), id, []vmcp.Tool{{Name: "boom"}})

	logs := buf.String()
	require.NotEmpty(t, logs, "expected a skip warning so the assertion is not vacuous")
	assert.NotContains(t, logs, secret, "identity token must never be logged")
}
