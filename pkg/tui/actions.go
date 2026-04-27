// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	cfg "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// actionDoneMsg is sent when a stop/restart/delete action completes.
type actionDoneMsg struct {
	action string // "stopped", "restarted", "deleted"
	name   string // workload name
	err    error
}

// stopWorkload returns a tea.Cmd that stops the named workload.
func stopWorkload(ctx context.Context, manager workloads.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		fn, err := manager.StopWorkloads(ctx, []string{name})
		if err != nil {
			return actionDoneMsg{action: "stopped", name: name, err: err}
		}
		return actionDoneMsg{action: "stopped", name: name, err: fn()}
	}
}

// deleteWorkload returns a tea.Cmd that removes the named workload.
func deleteWorkload(ctx context.Context, manager workloads.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		fn, err := manager.DeleteWorkloads(ctx, []string{name})
		if err != nil {
			return actionDoneMsg{action: "deleted", name: name, err: err}
		}
		return actionDoneMsg{action: "deleted", name: name, err: fn()}
	}
}

// restartWorkload returns a tea.Cmd that restarts the named workload.
func restartWorkload(ctx context.Context, manager workloads.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		fn, err := manager.RestartWorkloads(ctx, []string{name}, false)
		if err != nil {
			return actionDoneMsg{action: "restarted", name: name, err: err}
		}
		return actionDoneMsg{action: "restarted", name: name, err: fn()}
	}
}

// runFormResultMsg is sent when a "run from registry" command completes.
type runFormResultMsg struct {
	name   string
	server string
	err    error
}

// runFromRegistry returns a tea.Cmd that builds a RunConfig from registry
// metadata and launches the workload via the in-process workloads manager.
// This avoids shelling out to `thv run`, which leaks secrets in
// /proc/<pid>/cmdline and introduces unnecessary subprocess complexity.
func runFromRegistry(
	ctx context.Context,
	manager workloads.Manager,
	item regtypes.ServerMetadata,
	workloadName string,
	secrets, envs map[string]string,
) tea.Cmd {
	return func() tea.Msg {
		serverName := item.GetName()

		runCfg, err := buildRunConfigFromRegistry(ctx, item, workloadName, secrets, envs)
		if err != nil {
			return runFormResultMsg{name: workloadName, server: serverName, err: err}
		}

		// Enforce policy before saving state so violations surface immediately.
		if err := runner.EagerCheckCreateServer(ctx, runCfg); err != nil {
			return runFormResultMsg{name: workloadName, server: serverName,
				err: fmt.Errorf("server creation blocked by policy: %w", err)}
		}

		// Persist the config before starting (both foreground and detached need this).
		if err := runCfg.SaveState(ctx); err != nil {
			return runFormResultMsg{name: workloadName, server: serverName,
				err: fmt.Errorf("save run configuration: %w", err)}
		}

		if err := manager.RunWorkloadDetached(ctx, runCfg); err != nil {
			return runFormResultMsg{name: workloadName, server: serverName, err: err}
		}

		return runFormResultMsg{name: workloadName, server: serverName}
	}
}

// buildRunConfigFromRegistry constructs a runner.RunConfig from the registry
// metadata and the form field values. It mirrors the logic in
// cmd/thv/app/run_flags.go BuildRunnerConfig but only for the subset of
// options relevant to a TUI "run from registry" flow.
func buildRunConfigFromRegistry(
	ctx context.Context,
	item regtypes.ServerMetadata,
	workloadName string,
	secrets, envs map[string]string,
) (*runner.RunConfig, error) {
	serverName := item.GetName()

	// Validate env var / secret key names.
	for k := range secrets {
		if strings.ContainsRune(k, '=') {
			return nil, fmt.Errorf("invalid secret name %q: must not contain '='", k)
		}
	}
	for k := range envs {
		if strings.ContainsRune(k, '=') {
			return nil, fmt.Errorf("invalid env var name %q: must not contain '='", k)
		}
	}

	// Merge secrets and explicit env vars into a single map.  The user
	// entered actual values into the form, so they go into EnvVars directly
	// (not the --secret flow which references the secrets manager).
	mergedEnvVars := make(map[string]string, len(secrets)+len(envs))
	for k, v := range secrets {
		mergedEnvVars[k] = v
	}
	for k, v := range envs {
		mergedEnvVars[k] = v
	}

	// Extract ImageMetadata if available (needed for the builder).
	var imageMetadata *regtypes.ImageMetadata
	if img, ok := item.(*regtypes.ImageMetadata); ok && img != nil {
		imageMetadata = img
	}

	// Resolve the image URL from the registry (no pull yet).
	imageURL, serverMetadata, err := retriever.ResolveMCPServer(
		ctx, serverName, "" /* caCertPath */, retriever.VerifyImageWarn, "" /* groupName */, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP server %s: %w", serverName, err)
	}

	// Resolve the transport: prefer registry metadata, default to streamable-http.
	transportType := item.GetTransport()
	if transportType == "" {
		transportType = "streamable-http"
	}

	// Determine the permission profile name from ImageMetadata.
	var permissionProfile string
	if imageMetadata != nil && imageMetadata.Permissions != nil {
		if name := imageMetadata.Permissions.Name; name != "" && name != "none" {
			permissionProfile = name
		}
	}

	// Load application config for registry source URLs.
	configProvider := cfg.NewProvider()
	appConfig, loadErr := configProvider.LoadOrCreateConfig()
	if loadErr != nil {
		slog.Warn("failed to load application config, registry source URLs will be empty", "error", loadErr)
	}
	regAPIURL, regURL := runner.ResolveRegistrySourceURLs(serverMetadata, appConfig)
	regServerName := runner.ResolveRegistryServerName(serverMetadata)

	opts := []runner.RunConfigBuilderOption{
		runner.WithName(workloadName),
		runner.WithImage(imageURL),
		runner.WithHost(transport.LocalhostIPv4),
		runner.WithTargetHost(transport.LocalhostIPv4),
		runner.WithTransportAndPorts(transportType, 0 /* proxyPort */, 0 /* targetPort */),
		runner.WithPermissionProfileNameOrPath(permissionProfile),
		runner.WithGroup("default"),
		runner.WithRegistrySourceURLs(regAPIURL, regURL),
		runner.WithRegistryServerName(regServerName),
	}

	// Use DetachedEnvVarValidator since the TUI cannot prompt interactively.
	runConfig, err := runner.NewRunConfigBuilder(
		ctx,
		imageMetadata,
		mergedEnvVars,
		&runner.DetachedEnvVarValidator{},
		opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("build run config: %w", err)
	}

	// Pull the image (for container-based servers) after the config is built.
	if err := retriever.EnforcePolicyAndPullImage(
		ctx, runConfig, serverMetadata, imageURL,
		retriever.PullMCPServerImage, 0, false,
	); err != nil {
		return nil, fmt.Errorf("pull image: %w", err)
	}

	return runConfig, nil
}
