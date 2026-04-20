// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// minimalRunConfig represents just the fields we need from a run configuration
type minimalRunConfig struct {
	Group     string `json:"group,omitempty" yaml:"group,omitempty"`
	ProxyMode string `json:"proxy_mode,omitempty" yaml:"proxy_mode,omitempty"`
}

// loadRunConfigFields attempts to load specific fields from the runconfig
// using the provided store. Returns empty struct if runconfig doesn't exist.
func loadRunConfigFields(ctx context.Context, store state.Store, name string) (*minimalRunConfig, error) {
	reader, err := store.GetReader(ctx, name)
	if err != nil {
		// If the run config doesn't exist, return empty config (not an error).
		// This also handles the race where a workload is deleted between listing
		// and reading its config.
		if httperr.Code(err) == http.StatusNotFound {
			return &minimalRunConfig{}, nil
		}
		return nil, fmt.Errorf("failed to read run config for workload %q: %w", name, err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			slog.Warn("failed to close run config reader", "workload", name, "error", err)
		}
	}()

	var config minimalRunConfig
	if err := json.NewDecoder(reader).Decode(&config); err != nil {
		// EOF from an empty reader (e.g. KubernetesStore) means no config exists
		if err == io.EOF {
			return &minimalRunConfig{}, nil
		}
		return nil, fmt.Errorf("failed to decode run config for workload %q: %w", name, err)
	}
	return &config, nil
}

// WorkloadFromContainerInfo creates a Workload struct from the runtime container info.
// The runConfigStore is used to load run configuration fields (proxy mode, group)
// without hitting the real filesystem, enabling proper dependency injection for tests.
func WorkloadFromContainerInfo(container *runtime.ContainerInfo, runConfigStore state.Store) (core.Workload, error) {
	// Get workload name (base name) from labels for user-facing display
	name := labels.GetContainerBaseName(container.Labels)
	if name == "" {
		// Fallback to full container name if base name is not available
		containerName := labels.GetContainerName(container.Labels)
		if containerName == "" {
			name = container.Name // Final fallback to container name
		} else {
			name = containerName
		}
	}

	// Get port from labels
	port, err := labels.GetPort(container.Labels)
	if err != nil {
		port = 0
	}

	transportTypeLabel := labels.GetTransportType(container.Labels)

	tType, err := types.ParseTransportType(transportTypeLabel)
	if err != nil {
		// If we can't parse the transport type, default to SSE.
		tType = types.TransportTypeSSE
	}

	ctx := context.Background()
	runConfig, err := loadRunConfigFields(ctx, runConfigStore, name)
	if err != nil {
		return core.Workload{}, err
	}

	// Generate URL for the MCP server
	url := ""
	if port > 0 {
		url = transport.GenerateMCPServerURL(tType.String(), runConfig.ProxyMode, transport.LocalhostIPv4, port, name, "")
	}

	// Filter out standard ToolHive labels to show only user-defined labels
	userLabels := make(map[string]string)
	for key, value := range container.Labels {
		if !labels.IsStandardToolHiveLabel(key) {
			userLabels[key] = value
		}
	}

	// Calculate the effective proxy mode that clients should use
	effectiveProxyMode := GetEffectiveProxyMode(tType, runConfig.ProxyMode)

	// Translate to the domain model.
	return core.Workload{
		Name:          name, // Use the calculated workload name (base name), not container name
		Package:       container.Image,
		URL:           url,
		TransportType: tType,
		ProxyMode:     effectiveProxyMode,
		Status:        container.State,
		StatusContext: container.Status,
		CreatedAt:     container.Created,
		Port:          port,
		Labels:        userLabels,
		Group:         runConfig.Group,
		StartedAt:     container.StartedAt,
	}, nil
}

// GetEffectiveProxyMode determines the effective proxy mode that clients should use.
// For stdio transports, this returns the proxy mode (sse or streamable-http).
// For direct transports (sse/streamable-http), this returns the transport type as the proxy mode.
//
// Prefer types.EffectiveProxyMode for new code operating on typed values.
func GetEffectiveProxyMode(transportType types.TransportType, proxyMode string) string {
	return types.EffectiveProxyMode(transportType, types.ProxyMode(proxyMode)).String()
}
