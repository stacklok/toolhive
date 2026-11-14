// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	appconfig "github.com/stacklok/toolhive/pkg/config"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/updates"
	"github.com/stacklok/toolhive/pkg/usagemetrics"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

const (
	// usageMetricsComponent is the component vMCP reports in its User-Agent.
	usageMetricsComponent = "vmcp"

	usageMetricsShutdownTimeout = 10 * time.Second
)

// usageMetrics holds the anonymous usage reporting for one vMCP server.
type usageMetrics struct {
	collector *usagemetrics.Collector
	// toolCount is the tool count of the latest aggregation, -1 until the first.
	toolCount atomic.Int64
}

// usageFacts are the vMCP settings reported in the usage profile, resolved
// once at the composition root.
type usageFacts struct {
	authMode string
	authz    bool
	features []string
}

// startUsageMetrics starts anonymous usage reporting unless it is disabled
// (CI, TOOLHIVE_USAGE_METRICS_ENABLED=false, TOOLHIVE_SKIP_UPDATE_CHECK, or
// `thv config usage-metrics disable`, or a config that cannot be loaded). It
// returns nil when disabled. It also
// runs the vMCP update check in the background, after the collector has read
// the anonymous ID, so that ID never depends on which of the two runs first.
func startUsageMetrics(registry vmcp.BackendRegistry, facts usageFacts) *usageMetrics {
	if !usagemetrics.ShouldEnableMetrics(usageMetricsDisabledInConfig()) {
		return nil
	}
	u := &usageMetrics{}
	u.toolCount.Store(-1)
	collector, err := usagemetrics.NewCollector(
		usagemetrics.WithComponent(usageMetricsComponent),
		usagemetrics.WithProfile(u.profileFunc(registry, facts)),
	)
	if err != nil {
		slog.Debug("failed to initialize usage metrics", "error", err)
		return nil
	}
	u.collector = collector
	collector.Start()
	go checkForUpdates()
	return u
}

// middleware returns the HTTP middleware that counts tool calls. It must run
// after the MCP parsing middleware.
func (u *usageMetrics) middleware() func(http.Handler) http.Handler {
	return usagemetrics.NewMiddleware(u.collector).Handler()
}

// observe wraps an aggregator so every aggregation updates the reported tool count.
func (u *usageMetrics) observe(agg aggregator.Aggregator) aggregator.Aggregator {
	return &toolCountObserver{Aggregator: agg, toolCount: &u.toolCount}
}

// shutdown sends the final report.
func (u *usageMetrics) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), usageMetricsShutdownTimeout)
	defer cancel()
	u.collector.Shutdown(ctx)
}

func (u *usageMetrics) profileFunc(registry vmcp.BackendRegistry, facts usageFacts) func() usagemetrics.Profile {
	authz := usagemetrics.ValueOff
	if facts.authz {
		authz = usagemetrics.ValueOn
	}
	runtime := usagemetrics.ValueNone
	if rt.IsKubernetesRuntime() {
		runtime = usagemetrics.RuntimeKubernetes
	}
	return func() usagemetrics.Profile {
		backends := len(registry.List(context.Background()))
		profile := usagemetrics.Profile{
			Transport:    "streamable-http",
			Runtime:      runtime,
			AuthMode:     facts.authMode,
			Authz:        authz,
			BackendCount: &backends,
			Features:     facts.features,
		}
		if n := u.toolCount.Load(); n >= 0 {
			tools := int(n)
			profile.ToolCount = &tools
		}
		return profile
	}
}

// toolCountObserver records the tool count of each aggregation and otherwise
// delegates to the wrapped aggregator.
type toolCountObserver struct {
	aggregator.Aggregator
	toolCount *atomic.Int64
}

// MergeCapabilities records the merged tool count.
func (o *toolCountObserver) MergeCapabilities(
	ctx context.Context, resolved *aggregator.ResolvedCapabilities, registry vmcp.BackendRegistry,
) (*aggregator.AggregatedCapabilities, error) {
	result, err := o.Aggregator.MergeCapabilities(ctx, resolved, registry)
	o.record(result, err)
	return result, err
}

// AggregateCapabilities records the aggregated tool count.
func (o *toolCountObserver) AggregateCapabilities(
	ctx context.Context, backends []vmcp.Backend,
) (*aggregator.AggregatedCapabilities, error) {
	result, err := o.Aggregator.AggregateCapabilities(ctx, backends)
	o.record(result, err)
	return result, err
}

func (o *toolCountObserver) record(result *aggregator.AggregatedCapabilities, err error) {
	if err == nil && result != nil && result.Metadata != nil {
		o.toolCount.Store(int64(result.Metadata.ToolCount))
	}
}

// vmcpFeatures lists the vMCP features a configuration turns on.
func vmcpFeatures(
	optimizer bool, codeMode *config.CodeModeConfig, compositeTools int, outgoing *config.OutgoingAuthConfig,
) []string {
	var features []string
	if optimizer {
		features = append(features, usagemetrics.FeatureOptimizer)
	}
	if codeMode != nil && codeMode.Enabled {
		features = append(features, usagemetrics.FeatureCodeMode)
	}
	if compositeTools > 0 {
		features = append(features, usagemetrics.FeatureCompositeTools)
	}
	if usesOutgoingAuth(outgoing) {
		features = append(features, usagemetrics.FeatureOutgoingAuth)
	}
	return features
}

// usesOutgoingAuth reports whether any backend gets credentials: discovered
// auth, or a default or per-backend strategy other than unauthenticated.
func usesOutgoingAuth(cfg *config.OutgoingAuthConfig) bool {
	if cfg == nil {
		return false
	}
	if cfg.Source == "discovered" {
		return true
	}
	authenticated := func(s *authtypes.BackendAuthStrategy) bool {
		return s != nil && s.Type != "" && s.Type != authtypes.StrategyTypeUnauthenticated
	}
	if authenticated(cfg.Default) {
		return true
	}
	for _, s := range cfg.Backends {
		if authenticated(s) {
			return true
		}
	}
	return false
}

// vmcpAuthMode classifies the incoming authentication.
func vmcpAuthMode(incoming *config.IncomingAuthConfig, embeddedAuth bool) string {
	if embeddedAuth {
		return usagemetrics.AuthModeEmbedded
	}
	if incoming == nil {
		return usagemetrics.ValueNone
	}
	switch incoming.Type {
	case "", "anonymous":
		return usagemetrics.ValueNone
	case usagemetrics.AuthModeOIDC, usagemetrics.AuthModeLocal:
		return incoming.Type
	default:
		return usagemetrics.ValueOther
	}
}

// usageMetricsDisabledInConfig reports whether the user turned usage metrics
// off with `thv config usage-metrics disable`. In Kubernetes the provider
// returns defaults without touching the filesystem. It deliberately avoids
// DefaultProvider.GetConfig, which exits the process when the config cannot be
// loaded: usage reporting is optional and must never stop vMCP from starting.
func usageMetricsDisabledInConfig() bool {
	return disabledInConfig(appconfig.NewProvider().LoadOrCreateConfig)
}

// disabledInConfig reports whether the loaded config disables usage metrics. A
// config that cannot be loaded counts as disabled, because the user's opt-out
// cannot be ruled out.
func disabledInConfig(load func() (*appconfig.Config, error)) bool {
	cfg, err := load()
	if err != nil {
		slog.Debug("unable to load config, not sending usage metrics", "error", err)
		return true
	}
	return cfg.DisableUsageMetrics
}

// checkForUpdates reports this vMCP instance to the update service, the same
// way the thv CLI and the operator do. Failures are non-fatal. In Kubernetes the
// pod usually has a read-only root filesystem and no persistent home, so the
// file-backed update checker cannot work there; the ping is sent directly with a
// per-process instance ID instead, like each proxy's usage metrics instance ID.
func checkForUpdates() {
	if updates.ShouldSkipUpdateChecks() {
		return
	}
	client := updates.NewVersionClientForComponent(usageMetricsComponent, "", false)
	if rt.IsKubernetesRuntime() {
		if _, err := client.GetLatestVersion(uuid.NewString(), versions.GetVersionInfo().Version); err != nil {
			slog.Debug(fmt.Sprintf("could not check for updates: %s", err))
		}
		return
	}
	checker, err := updates.NewUpdateChecker(client)
	if err != nil {
		slog.Debug(fmt.Sprintf("unable to create update client: %s", err))
		return
	}
	if err := checker.CheckLatestVersion(); err != nil {
		slog.Debug(fmt.Sprintf("could not check for updates: %s", err))
	}
}
