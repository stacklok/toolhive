// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cli provides the business logic for the vMCP serve and validate
// commands. It is designed to be imported by both the standalone vmcp binary
// (cmd/vmcp/app) and the thv vmcp subcommand (cmd/thv/app), keeping all
// server-initialization logic in one importable place.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/audit"
	authserverconfig "github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/app"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// ServeConfig holds all parameters needed to start the vMCP server.
// Populated by the caller from Cobra flag values or equivalent.
// At least one of ConfigPath or GroupRef must be non-empty; ConfigPath takes
// precedence when both are provided.
type ServeConfig struct {
	// ConfigPath is the path to the vMCP YAML configuration file.
	// When set, takes precedence over GroupRef.
	ConfigPath string
	// GroupRef is a ToolHive group name used for zero-config quick mode when
	// ConfigPath is empty. A minimal in-memory config is generated from this value.
	GroupRef string
	// Host is the address the server binds to (e.g. "127.0.0.1").
	Host string
	// Port is the TCP port the server listens on.
	Port int
	// EnableAudit enables audit logging with default configuration when
	// the loaded config does not already define an audit section.
	EnableAudit bool

	// SessionTTL is the inactivity timeout for vMCP sessions.
	// Zero uses the server default (30m). Negative values fail validation.
	SessionTTL time.Duration

	// Optimizer tier selection (Phase 4 — flag-driven).
	// EnableOptimizer enables Tier 1 FTS5 keyword search (find_tool / call_tool).
	EnableOptimizer bool
	// EnableEmbedding enables Tier 2 TEI semantic search; implies EnableOptimizer.
	EnableEmbedding bool
	// EmbeddingModel is the HuggingFace model name for the managed TEI container.
	// Defaults to "BAAI/bge-small-en-v1.5" when empty.
	EmbeddingModel string
	// EmbeddingImage is the TEI container image.
	// Defaults to the CPU TEI image when empty.
	EmbeddingImage string
}

// validateQuickModeHost returns an error when the config represents quick mode
// (GroupRef set, ConfigPath empty) and Host is not a loopback address. Quick
// mode always uses anonymous auth, so binding to a non-loopback interface would
// expose an unauthenticated server on the network. Empty host is treated as the
// default loopback address; "localhost" is accepted as a known loopback name.
func (c ServeConfig) validateQuickModeHost() error {
	if c.ConfigPath != "" || c.GroupRef == "" {
		return nil
	}
	h := c.Host
	if h == "" {
		h = "127.0.0.1"
	}
	if h == "localhost" {
		return nil
	}
	ip := net.ParseIP(h)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("quick mode (--group) only supports loopback bind addresses (e.g. 127.0.0.1); got %q", c.Host)
	}
	return nil
}

// Serve loads configuration, assembles the vMCP server via the app package,
// and starts it. It blocks until the context is cancelled or the server stops.
//
// CLI-specific concerns (loading YAML, injecting CLI flags, managing the TEI
// container) are handled here; domain assembly is delegated to app.BuildCore
// and app.BuildServerConfig.
//
//nolint:gocyclo // Complexity from CLI flag handling and server initialization is acceptable here.
func Serve(ctx context.Context, cfg ServeConfig) error {
	if err := cfg.validateQuickModeHost(); err != nil {
		return err
	}
	if cfg.SessionTTL < 0 {
		return fmt.Errorf("session-ttl must be non-negative, got %s", cfg.SessionTTL)
	}

	// Load and validate configuration — file path takes precedence over group quick mode.
	vmcpCfg, err := func() (*vmcpconfig.Config, error) {
		switch {
		case cfg.ConfigPath != "":
			return loadAndValidateConfig(cfg.ConfigPath)
		case cfg.GroupRef != "":
			return generateQuickModeConfig(cfg.GroupRef)
		default:
			return nil, fmt.Errorf("either --config or --group must be specified")
		}
	}()
	if err != nil {
		return err
	}

	// Apply --enable-audit flag when the config has no audit section.
	if cfg.EnableAudit && vmcpCfg.Audit == nil {
		vmcpCfg.Audit = audit.DefaultConfig()
		vmcpCfg.Audit.Component = "vmcp-server"
		slog.Info("audit logging enabled with default configuration")
	}

	// Load auth server config from sibling file if present.
	// Skip in quick mode (no config file) — there is no sibling directory to search.
	var authServerRC *authserverconfig.RunConfig
	if cfg.ConfigPath != "" {
		authServerRC, err = loadAuthServerConfig(cfg.ConfigPath)
		if err != nil {
			return err
		}
	}

	// Inject optimizer config from CLI flags (flag-driven Tier 1 / Tier 2).
	// injectOptimizerConfig modifies vmcpCfg.Optimizer in place and starts the
	// TEI container when embedding is enabled.
	var embMgr embeddingManager
	if cfg.EnableEmbedding {
		model := cfg.EmbeddingModel
		if model == "" {
			model = DefaultEmbeddingModel
		}
		image := cfg.EmbeddingImage
		if image == "" {
			image = DefaultEmbeddingImage
		}
		m, err := NewEmbeddingServiceManager(container.NewFactory(), EmbeddingServiceManagerConfig{
			Model: model,
			Image: image,
		})
		if err != nil {
			return fmt.Errorf("failed to create embedding service manager: %w", err)
		}
		embMgr = m
	}
	teiCleanup, err := injectOptimizerConfig(ctx, cfg, vmcpCfg, embMgr)
	if err != nil {
		return err
	}
	if teiCleanup != nil {
		defer teiCleanup()
	}

	// Assemble the server via the app.Builder — the single assembly entry point. It
	// builds the shared collaborators once (telemetry from vmcpCfg.Telemetry; the
	// Kubernetes backend registry + watcher for the discovered source), wires
	// composite-tool elicitation internally, and returns one cleanup func. The
	// embedded auth server run config is threaded through so the builder can inject
	// subject-provider names before assembly.
	srv, _, cleanup, err := app.NewBuilder(ctx, vmcpCfg,
		app.WithVersion(versions.Version),
		app.WithHost(cfg.Host, cfg.Port),
		app.WithSessionTTL(cfg.SessionTTL),
		app.WithAuthServerRunConfig(authServerRC),
	).Finish()
	if err != nil {
		return fmt.Errorf("failed to assemble Virtual MCP Server: %w", err)
	}
	defer cleanup()

	slog.Info(fmt.Sprintf("Starting Virtual MCP Server at %s", srv.Address()))
	return srv.Start(ctx)
}

// embeddingManager is the minimal interface over *EmbeddingServiceManager needed
// by the Serve lifecycle. Defined here to allow stub injection in unit tests;
// production code passes a *EmbeddingServiceManager.
type embeddingManager interface {
	Start(ctx context.Context) (string, error)
	Stop(ctx context.Context) error
}

// injectOptimizerConfig ensures vmcpCfg.Optimizer is non-nil when flag-driven
// optimizer tiers are active, and starts the TEI container when EnableEmbedding
// is true. Returns a non-nil cleanup func only when a TEI container was started;
// the caller must defer it. mgr must be non-nil when cfg.EnableEmbedding is true.
func injectOptimizerConfig(
	ctx context.Context, cfg ServeConfig, vmcpCfg *vmcpconfig.Config, mgr embeddingManager,
) (func(), error) {
	if !cfg.EnableOptimizer && !cfg.EnableEmbedding {
		return nil, nil
	}
	if vmcpCfg.Optimizer == nil {
		vmcpCfg.Optimizer = &vmcpconfig.OptimizerConfig{}
	}
	if !cfg.EnableEmbedding {
		return nil, nil
	}
	if mgr == nil {
		return nil, fmt.Errorf("embedding manager must not be nil when EnableEmbedding is true")
	}
	teiURL, err := mgr.Start(ctx)
	if err != nil {
		// Best-effort cleanup: a Start failure can still leave a partial
		// container behind (created but health poll timed out, etc.).
		_ = mgr.Stop(context.Background())
		return nil, fmt.Errorf("failed to start TEI embedding service: %w", err)
	}
	vmcpCfg.Optimizer.EmbeddingService = teiURL
	return func() { _ = mgr.Stop(context.Background()) }, nil
}

// loadAndValidateConfig loads and validates the vMCP configuration file.
func loadAndValidateConfig(configPath string) (*vmcpconfig.Config, error) {
	slog.Info(fmt.Sprintf("Loading configuration from: %s", configPath))

	loader := vmcpconfig.NewYAMLLoader(configPath, &env.OSReader{})
	cfg, err := loader.Load()
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to load configuration: %v", err))
		return nil, fmt.Errorf("configuration loading failed: %w", err)
	}

	validator := vmcpconfig.NewValidator()
	if err := validator.Validate(cfg); err != nil {
		slog.Error(fmt.Sprintf("Configuration validation failed: %v", err))
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	slog.Info("configuration loaded and validated successfully")
	slog.Info(fmt.Sprintf("  Name: %s", cfg.Name))
	slog.Info(fmt.Sprintf("  Group: %s", cfg.Group))
	slog.Info(fmt.Sprintf("  Conflict Resolution: %s", cfg.Aggregation.ConflictResolution))
	if len(cfg.CompositeTools) > 0 {
		slog.Info(fmt.Sprintf("  Composite Tools: %d defined", len(cfg.CompositeTools)))
	}

	return cfg, nil
}

// generateQuickModeConfig constructs a minimal in-memory config for zero-config
// quick mode (thv vmcp serve --group <name>). It sets groupRef from groupRef,
// incomingAuth to anonymous, and outgoingAuth.source to "inline" so no
// Kubernetes API access is required. The generated config is validated before
// being returned; returns an error if groupRef is empty or validation fails.
func generateQuickModeConfig(groupRef string) (*vmcpconfig.Config, error) {
	if groupRef == "" {
		return nil, fmt.Errorf("--group must not be empty")
	}
	cfg := &vmcpconfig.Config{
		Name:  groupRef,
		Group: groupRef,
		IncomingAuth: &vmcpconfig.IncomingAuthConfig{
			Type: vmcpconfig.IncomingAuthTypeAnonymous,
		},
		OutgoingAuth: &vmcpconfig.OutgoingAuthConfig{
			Source: "inline",
		},
		Aggregation: &vmcpconfig.AggregationConfig{
			ConflictResolution: vmcp.ConflictStrategyPrefix,
			ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
				PrefixFormat: "{workload}_",
			},
		},
	}
	if err := vmcpconfig.NewValidator().Validate(cfg); err != nil {
		return nil, fmt.Errorf("quick-mode config validation failed: %w", err)
	}
	return cfg, nil
}

// loadAuthServerConfig loads the auth server RunConfig from a sibling file
// alongside the main config. The operator serializes authserver.RunConfig as a
// separate ConfigMap key (authserver-config.yaml).
// Returns nil with no error if the file does not exist.
func loadAuthServerConfig(configPath string) (*authserverconfig.RunConfig, error) {
	authServerPath := filepath.Join(filepath.Dir(configPath), "authserver-config.yaml")
	//nolint:gosec // path is user-supplied and intentionally read from the local filesystem
	authServerData, readErr := os.ReadFile(authServerPath)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read auth server config %s: %w", authServerPath, readErr)
	}
	var rc authserverconfig.RunConfig
	if unmarshalErr := yaml.Unmarshal(authServerData, &rc); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to parse auth server config %s: %w", authServerPath, unmarshalErr)
	}
	slog.Info("auth server configuration loaded", "path", authServerPath)
	return &rc, nil
}
