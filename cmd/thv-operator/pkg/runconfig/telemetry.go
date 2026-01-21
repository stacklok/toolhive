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

// AddTelemetryConfigOptions adds telemetry configuration options to the builder options
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
