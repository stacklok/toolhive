// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	aggmocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
)

// fakeStatusProvider is a map-backed health.StatusProvider for tests. A missing
// key reports (zero, false), mirroring a backend untracked by the health monitor.
type fakeStatusProvider map[string]vmcp.BackendHealthStatus

func (f fakeStatusProvider) QueryBackendStatus(id string) (vmcp.BackendHealthStatus, bool) {
	s, ok := f[id]
	return s, ok
}

// coreMocks bundles the gomock collaborators wired into a test Config.
type coreMocks struct {
	agg    *aggmocks.MockAggregator
	reg    *vmcpmocks.MockBackendRegistry
	client *vmcpmocks.MockBackendClient
	router *routermocks.MockRouter
}

// baseConfig returns a Config with the four required collaborators mocked.
func baseConfig(t *testing.T) (*Config, *coreMocks) {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := &coreMocks{
		agg:    aggmocks.NewMockAggregator(ctrl),
		reg:    vmcpmocks.NewMockBackendRegistry(ctrl),
		client: vmcpmocks.NewMockBackendClient(ctrl),
		router: routermocks.NewMockRouter(ctrl),
	}
	cfg := &Config{
		Aggregator:      m.agg,
		Router:          m.router,
		BackendRegistry: m.reg,
		BackendClient:   m.client,
	}
	return cfg, m
}

// testBackendID is the single backend ID used across these tests.
const testBackendID = "be1"

func backendTool(name string) vmcp.Tool {
	return vmcp.Tool{Name: name, BackendID: testBackendID}
}

func backendTarget() *vmcp.BackendTarget {
	return &vmcp.BackendTarget{WorkloadID: testBackendID, BaseURL: "http://" + testBackendID + ":8080"}
}

func TestNew_RequiredCollaborators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"nil aggregator", func(c *Config) { c.Aggregator = nil }, true},
		{"nil router", func(c *Config) { c.Router = nil }, true},
		{"nil backend registry", func(c *Config) { c.BackendRegistry = nil }, true},
		{"nil backend client", func(c *Config) { c.BackendClient = nil }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, _ := baseConfig(t)
			tt.mutate(cfg)

			c, err := New(cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, vmcp.ErrInvalidConfig)
				assert.Nil(t, c)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, c)
			require.NoError(t, c.Close())
		})
	}
}

func TestNew_NilConfig(t *testing.T) {
	t.Parallel()
	c, err := New(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, vmcp.ErrInvalidConfig)
	assert.Nil(t, c)
}

func TestNew_ValidatesWorkflows(t *testing.T) {
	t.Parallel()

	valid := &composer.WorkflowDefinition{
		Name:  "wf",
		Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.tool"}},
	}
	circular := &composer.WorkflowDefinition{
		Name: "wf",
		Steps: []composer.WorkflowStep{
			{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.tool", DependsOn: []string{"s2"}},
			{ID: "s2", Type: composer.StepTypeTool, Tool: "be1.tool", DependsOn: []string{"s1"}},
		},
	}

	tests := []struct {
		name    string
		defs    map[string]*composer.WorkflowDefinition
		wantErr bool
	}{
		{"valid workflow", map[string]*composer.WorkflowDefinition{"wf": valid}, false},
		{"invalid workflow fails fast", map[string]*composer.WorkflowDefinition{"wf": circular}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, _ := baseConfig(t)
			cfg.WorkflowDefs = tt.defs

			c, err := New(cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, c)
				return
			}
			require.NoError(t, err)
			require.NoError(t, c.Close())
		})
	}
}

func TestNew_ElicitationRequiredWhenWorkflowElicits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		steps   []composer.WorkflowStep
		wantErr bool
	}{
		{
			name:    "elicitation step requires a requester",
			steps:   []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeElicitation}},
			wantErr: true,
		},
		{
			name: "forEach inner elicitation step requires a requester",
			steps: []composer.WorkflowStep{{
				ID:        "s1",
				Type:      composer.StepTypeForEach,
				InnerStep: &composer.WorkflowStep{ID: "inner", Type: composer.StepTypeElicitation},
			}},
			wantErr: true,
		},
		{
			name:    "tool-only workflow needs no requester",
			steps:   []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.tool"}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, _ := baseConfig(t)
			cfg.Elicitation = nil // explicit: no requester wired
			cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
				"wf": {Name: "wf", Steps: tt.steps},
			}

			c, err := New(cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, vmcp.ErrInvalidConfig)
				assert.Nil(t, c)
				return
			}
			require.NoError(t, err)
			require.NoError(t, c.Close())
		})
	}
}

func TestListTools_AggregatesOnDemand(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	backends := []vmcp.Backend{{ID: "be1", HealthStatus: vmcp.BackendHealthy}}
	m.reg.EXPECT().List(gomock.Any()).Return(backends)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), backends).Return(&aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("tool_a"), backendTool("tool_b")},
		RoutingTable: &vmcp.RoutingTable{},
	}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	tools, err := c.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "be1", tools[0].BackendID)
}

func TestListTools_AdvertisesAccessibleCompositeTools(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	cfg.WorkflowDefs = map[string]*composer.WorkflowDefinition{
		"wf": {Name: "wf", Steps: []composer.WorkflowStep{{ID: "s1", Type: composer.StepTypeTool, Tool: "be1.tool_a"}}},
	}

	rt := &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"be1.tool_a": backendTarget()}}
	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "be1"}})
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("be1.tool_a")},
		RoutingTable: rt,
	}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	tools, err := c.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	assert.Contains(t, names, "be1.tool_a")
	assert.Contains(t, names, "wf", "accessible composite tool should be advertised")
}

func TestListResourcesAndPrompts(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return(nil).Times(2)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Resources: []vmcp.Resource{{URI: "file://a", BackendID: "be1"}},
		Prompts:   []vmcp.Prompt{{Name: "p1", BackendID: "be1"}},
	}, nil).Times(2)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	resources, err := c.ListResources(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "file://a", resources[0].URI)

	prompts, err := c.ListPrompts(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, prompts, 1)
	assert.Equal(t, "p1", prompts[0].Name)
}

// denyResourceAdmission is a test Admission that drops resources whose URI is in
// deny. It embeds allowAllAdmission so the tool/prompt methods are satisfied, and
// overrides only FilterResources — the sole method ListResourceTemplates exercises
// (the URI template is projected onto a resource URI for admission).
type denyResourceAdmission struct {
	allowAllAdmission
	deny map[string]struct{}
}

func (d denyResourceAdmission) FilterResources(
	_ context.Context, _ *auth.Identity, resources []vmcp.Resource,
) ([]vmcp.Resource, error) {
	out := make([]vmcp.Resource, 0, len(resources))
	for i := range resources {
		if _, blocked := d.deny[resources[i].URI]; !blocked {
			out = append(out, resources[i])
		}
	}
	return out, nil
}

func TestListResourceTemplates(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return(nil).Times(2)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		ResourceTemplates: []vmcp.ResourceTemplate{
			{URITemplate: "file:///logs/{date}.txt", BackendID: "be1"},
			{URITemplate: "secret:///{id}", BackendID: "be1"},
		},
	}, nil).Times(2)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// Default allow-all admission: both templates surface, tagged with their backend.
	templates, err := c.ListResourceTemplates(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, templates, 2)

	// Inject a deny-admission that filters templates by URI template (treated as the
	// resource id): the denied template is withheld, mirroring ListResources.
	c.(*coreVMCP).admission = denyResourceAdmission{deny: map[string]struct{}{"secret:///{id}": {}}}
	templates, err = c.ListResourceTemplates(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, templates, 1)
	assert.Equal(t, "file:///logs/{date}.txt", templates[0].URITemplate)
	assert.Equal(t, "be1", templates[0].BackendID)
}

func TestListResourceTemplates_Empty(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return(nil)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
		&aggregator.AggregatedCapabilities{}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	templates, err := c.ListResourceTemplates(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, templates)
}

func TestListTools_AggregationError(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return(nil)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(nil, errors.New("boom"))

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.ListTools(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aggregation failed")
}

func TestListTools_HealthFiltersBeforeAggregating(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	backends := []vmcp.Backend{
		{ID: "healthy", HealthStatus: vmcp.BackendHealthy},
		{ID: "degraded", HealthStatus: vmcp.BackendDegraded},
		{ID: "unhealthy", HealthStatus: vmcp.BackendHealthy}, // overridden by provider below
		{ID: "unknown", HealthStatus: vmcp.BackendHealthy},   // overridden by provider below
	}
	m.reg.EXPECT().List(gomock.Any()).Return(backends)
	var got []vmcp.Backend
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, b []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			got = b
			return &aggregator.AggregatedCapabilities{}, nil
		})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// The core builds its own monitor from HealthMonitorConfig in production; inject a stub
	// health view directly (internal test) to exercise filtering without a running monitor.
	c.(*coreVMCP).health = fakeStatusProvider{
		"unhealthy": vmcp.BackendUnhealthy,
		"unknown":   vmcp.BackendUnknown,
	}

	_, err = c.ListTools(context.Background(), nil)
	require.NoError(t, err)

	gotIDs := make([]string, 0, len(got))
	for _, b := range got {
		gotIDs = append(gotIDs, b.ID)
	}
	assert.ElementsMatch(t, []string{"healthy", "degraded"}, gotIDs,
		"only healthy/degraded backends should reach the aggregator")
}

func TestFilterHealthyBackends(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "healthy", HealthStatus: vmcp.BackendHealthy},
		{ID: "degraded", HealthStatus: vmcp.BackendDegraded},
		{ID: "empty", HealthStatus: ""},
		{ID: "unhealthy", HealthStatus: vmcp.BackendUnhealthy},
		{ID: "unknown", HealthStatus: vmcp.BackendUnknown},
		{ID: "unauthenticated", HealthStatus: vmcp.BackendUnauthenticated},
	}

	tests := []struct {
		name     string
		provider fakeStatusProvider
		wantIDs  []string
	}{
		{
			name:    "nil provider uses registry status",
			wantIDs: []string{"healthy", "degraded", "empty"},
		},
		{
			name:     "provider overrides registry status",
			provider: fakeStatusProvider{"healthy": vmcp.BackendUnhealthy, "unhealthy": vmcp.BackendHealthy},
			// "healthy" demoted, "unhealthy" promoted; untracked backends fall back to registry.
			wantIDs: []string{"degraded", "empty", "unhealthy"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var provider interface {
				QueryBackendStatus(string) (vmcp.BackendHealthStatus, bool)
			}
			if tt.provider != nil {
				provider = tt.provider
			}
			got := filterHealthyBackends(backends, provider)
			ids := make([]string, 0, len(got))
			for _, b := range got {
				ids = append(ids, b.ID)
			}
			assert.ElementsMatch(t, tt.wantIDs, ids)
		})
	}
}

func TestFilterHealthyBackends_Empty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, filterHealthyBackends(nil, nil))
}

func TestLookup(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	// Seven aggregations: each Lookup aggregates once, except the failing
	// LookupResource, which also consults the resource-template view on the
	// concrete-resource miss.
	m.reg.EXPECT().List(gomock.Any()).Return(nil).Times(7)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:     []vmcp.Tool{backendTool("tool_a")},
		Resources: []vmcp.Resource{{URI: "file://a", BackendID: "be1"}},
		Prompts:   []vmcp.Prompt{{Name: "p1", BackendID: "be1"}},
	}, nil).Times(7)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	tool, err := c.LookupTool(ctx, nil, "tool_a")
	require.NoError(t, err)
	assert.Equal(t, "be1", tool.BackendID)
	_, err = c.LookupTool(ctx, nil, "missing")
	assert.ErrorIs(t, err, vmcp.ErrNotFound)

	res, err := c.LookupResource(ctx, nil, "file://a")
	require.NoError(t, err)
	assert.Equal(t, "be1", res.BackendID)
	_, err = c.LookupResource(ctx, nil, "file://missing")
	assert.ErrorIs(t, err, vmcp.ErrNotFound)

	prompt, err := c.LookupPrompt(ctx, nil, "p1")
	require.NoError(t, err)
	assert.Equal(t, "be1", prompt.BackendID)
	_, err = c.LookupPrompt(ctx, nil, "missing")
	assert.ErrorIs(t, err, vmcp.ErrNotFound)
}

// TestLookupResource_TemplateBackedURI verifies LookupResource accepts the URIs a
// templated read serves — both the template string itself (the form a
// resources/subscribe or completion ref/resource carries) and an expanded URI
// matching an advertised template — while an unmatched URI still misses. This
// keeps the subscribe admission (coreSubscribeHandler) aligned with read.
func TestLookupResource_TemplateBackedURI(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return(nil).Times(6)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Resources: []vmcp.Resource{{URI: "file://a", BackendID: "be1"}},
		ResourceTemplates: []vmcp.ResourceTemplate{
			{
				URITemplate: "file:///logs/{date}.txt",
				Name:        "Daily log",
				Description: "A day's log file",
				MimeType:    "text/plain",
				BackendID:   "be1",
			},
		},
	}, nil).Times(6)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()

	// The exact template string resolves to the template's capability.
	res, err := c.LookupResource(ctx, nil, "file:///logs/{date}.txt")
	require.NoError(t, err)
	assert.Equal(t, "file:///logs/{date}.txt", res.URI)
	assert.Equal(t, "be1", res.BackendID)

	// An expanded URI matching the template resolves to the same capability.
	res, err = c.LookupResource(ctx, nil, "file:///logs/2025-01-01.txt")
	require.NoError(t, err)
	assert.Equal(t, "be1", res.BackendID)

	// A URI matching no concrete resource or template still misses.
	_, err = c.LookupResource(ctx, nil, "db:///users/42")
	assert.ErrorIs(t, err, vmcp.ErrNotFound)
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	cfg, _ := baseConfig(t)

	c, err := New(cfg)
	require.NoError(t, err)

	require.NoError(t, c.Close())
	assert.NotPanics(t, func() {
		require.NoError(t, c.Close())
		require.NoError(t, c.Close())
	})
}

// invalidatorAggregator is a minimal aggregator.Aggregator that also
// implements aggregator.CacheInvalidator, so TestInvalidateCapabilityCache_*
// can assert coreVMCP.InvalidateCapabilityCache's delegation without depending
// on the real cachingAggregator (covered separately in the aggregator package).
type invalidatorAggregator struct {
	aggregator.Aggregator
	invalidateCalls int
}

func (a *invalidatorAggregator) InvalidateAll() { a.invalidateCalls++ }

var _ aggregator.CacheInvalidator = (*invalidatorAggregator)(nil)

// TestInvalidateCapabilityCache_DelegatesWhenSupported verifies (#5748) that
// InvalidateCapabilityCache type-asserts the configured aggregator to
// aggregator.CacheInvalidator and calls InvalidateAll when it is implemented.
func TestInvalidateCapabilityCache_DelegatesWhenSupported(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	inv := &invalidatorAggregator{Aggregator: m.agg}
	cfg.Aggregator = inv

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	c.InvalidateCapabilityCache()
	assert.Equal(t, 1, inv.invalidateCalls)
}

// TestInvalidateCapabilityCache_WarnsWhenUnsupported verifies (#5748) that
// InvalidateCapabilityCache is a WARN-logged no-op — not a silent one — when
// the configured aggregator does not implement aggregator.CacheInvalidator
// (baseConfig's plain MockAggregator does not).
//
//nolint:paralleltest // installs a global slog default + non-thread-safe buffer; must not run in parallel
func TestInvalidateCapabilityCache_WarnsWhenUnsupported(t *testing.T) {
	cfg, _ := baseConfig(t)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	assert.NotPanics(t, func() { c.InvalidateCapabilityCache() })
	assert.Contains(t, buf.String(), "does not support it",
		"unsupported aggregator must WARN-log, not silently no-op")
}

// Must run serially: it swaps the global slog default to capture output into a
// non-thread-safe buffer, which is unsafe if other tests log concurrently.
//
//nolint:paralleltest // installs a global slog default + non-thread-safe buffer; must not run in parallel
func TestIdentityNotLogged(t *testing.T) {
	cfg, m := baseConfig(t)

	const secret = "super-secret-token-value"

	// An excluded backend makes filterHealthyBackends emit a DEBUG log on every
	// aggregatedView, guaranteeing the buffer is non-empty so the assertion below
	// is not vacuous (it would otherwise pass if every path short-circuited).
	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{
		{ID: "ok", HealthStatus: vmcp.BackendHealthy},
		{ID: "bad", HealthStatus: vmcp.BackendHealthy},
	}).AnyTimes()
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{},
	}, nil).AnyTimes()

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	// Inject a stub health view (internal test) so filterHealthyBackends excludes "bad" and
	// emits its DEBUG log (see comment above).
	c.(*coreVMCP).health = fakeStatusProvider{"bad": vmcp.BackendUnhealthy}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	id := &auth.Identity{Token: secret, UpstreamTokens: map[string]string{"github": secret}}
	ctx := context.Background()

	// Drive every identity-taking method; return values are irrelevant — we only
	// inspect the captured logs.
	_, _ = c.ListTools(ctx, id)
	_, _ = c.ListResources(ctx, id)
	_, _ = c.ListPrompts(ctx, id)
	_, _ = c.LookupTool(ctx, id, "missing")
	_, _ = c.LookupResource(ctx, id, "missing")
	_, _ = c.LookupPrompt(ctx, id, "missing")
	_, _ = c.CallTool(ctx, id, "missing", nil, nil)
	_, _ = c.ReadResource(ctx, id, "missing")
	_, _ = c.GetPrompt(ctx, id, "missing", nil)

	logs := buf.String()
	require.NotEmpty(t, logs, "expected the driven methods to emit logs, else the assertion is vacuous")
	assert.NotContains(t, logs, secret, "identity token must never be logged")
}

// TestNew_HealthMonitorOwnedByCore verifies the core owns the backend health monitor
// (#5443 reversal): New builds and starts it from HealthMonitorConfig, exposes it via
// BackendHealth, drives initial checks, and Close stops it idempotently while leaving the
// reporter queryable.
func TestNew_HealthMonitorOwnedByCore(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	backends := []vmcp.Backend{{ID: testBackendID, Name: "be1", BaseURL: "http://be1:8080", TransportType: "sse"}}
	// The monitor lists backends from the registry and health-checks them via the client.
	m.reg.EXPECT().List(gomock.Any()).Return(backends).AnyTimes()
	m.client.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).AnyTimes()
	cfg.HealthMonitorConfig = &health.MonitorConfig{
		CheckInterval:      50 * time.Millisecond,
		UnhealthyThreshold: 1,
		Timeout:            time.Second,
	}

	c, err := New(cfg)
	require.NoError(t, err)

	reporter := c.BackendHealth()
	require.NotNil(t, reporter, "the core must expose the health monitor it built and started")

	require.Eventually(t, func() bool {
		status, ok := reporter.QueryBackendStatus(testBackendID)
		return ok && status == vmcp.BackendHealthy
	}, 2*time.Second, 10*time.Millisecond, "the core-started monitor should report the backend healthy")

	// Close stops the monitor and is idempotent; the reporter stays valid (stopped, not nil).
	require.NoError(t, c.Close())
	require.NoError(t, c.Close())
	assert.NotNil(t, c.BackendHealth())
}

// TestNew_HealthMonitorDisabledWhenNil verifies a nil HealthMonitorConfig leaves health
// monitoring disabled: the core builds no monitor and BackendHealth reports none.
func TestNew_HealthMonitorDisabledWhenNil(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)
	m.reg.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()

	c, err := New(cfg) // HealthMonitorConfig is nil
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	assert.Nil(t, c.BackendHealth(), "nil HealthMonitorConfig must leave health monitoring disabled")
}
