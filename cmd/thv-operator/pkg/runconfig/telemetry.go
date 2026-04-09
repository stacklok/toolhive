// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package runconfig provides functions to build RunConfigBuilder options for telemetry configuration.
package runconfig

import (
	"context"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/spectoconfig"
	"github.com/stacklok/toolhive/pkg/runner"
)

// AddTelemetryConfigOptions adds telemetry configuration options to the builder options.
// Deprecated: Use AddMCPTelemetryConfigRefOptions for MCPTelemetryConfig-based configuration.
func AddTelemetryConfigOptions(
	ctx context.Context,
	options *[]runner.RunConfigBuilderOption,
	telemetryConfig *mcpv1alpha1.TelemetryConfig,
	mcpServerName string,
) {
	if telemetryConfig == nil || options == nil {
		return
	}

	config := spectoconfig.ConvertTelemetryConfig(ctx, telemetryConfig, mcpServerName)

	// Add telemetry config to options
	*options = append(*options, runner.WithTelemetryConfig(config))
}

// AddMCPTelemetryConfigRefOptions converts an MCPTelemetryConfig spec with per-server overrides
// into a runner option. This is the preferred path for MCPServer.Spec.TelemetryConfigRef.
// caBundleFilePath is the computed mount path for the CA bundle (empty if none configured).
func AddMCPTelemetryConfigRefOptions(
	options *[]runner.RunConfigBuilderOption,
	telemetrySpec *mcpv1alpha1.MCPTelemetryConfigSpec,
	serviceNameOverride string,
	defaultServiceName string,
	caBundleFilePath string,
) {
	if telemetrySpec == nil || options == nil {
		return
	}

	config := spectoconfig.NormalizeMCPTelemetryConfig(telemetrySpec, serviceNameOverride, defaultServiceName)
	if config == nil {
		return
	}

	if caBundleFilePath != "" {
		config.CACertPath = caBundleFilePath
	}

	*options = append(*options, runner.WithTelemetryConfig(config))
}
