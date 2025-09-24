package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/environment"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// TODO: This function should be deprecated - CLI flags will be removed in favor of file-based configuration only
// runWithFlagsBasedConfig handles execution using CLI flags (legacy approach)
func runWithFlagsBasedConfig(
	ctx context.Context,
	mcpServerImage string,
	cmdArgs []string,
	validatedHost string,
	rt runtime.Runtime,
	debugMode bool,
	envVarValidator runner.EnvVarValidator,
	imageMetadata *registry.ImageMetadata,
) error {
	envVarsMap, err := environment.ParseEnvironmentVariables(runFlags.runEnv)
	if err != nil {
		return fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Build options using CLI flags
	opts := []runner.RunConfigBuilderOption{
		runner.WithRuntime(rt),
		runner.WithDebug(debugMode),
		runner.WithCmdArgs(cmdArgs),
		runner.WithImage(mcpServerImage),
		runner.WithName(runFlags.runName),
		runner.WithTransportAndPorts(runFlags.runTransport, runFlags.runProxyPort, runFlags.runTargetPort),
		runner.WithHost(validatedHost),
		runner.WithTargetHost(transport.LocalhostIPv4),
		runner.WithProxyMode(types.ProxyMode(runFlags.runProxyMode)),
		runner.WithVolumes(runFlags.runVolumes),
		runner.WithSecrets(runFlags.runSecrets),
		runner.WithAuthzConfigPath(runFlags.runAuthzConfig),
		runner.WithAuditConfigPath(runFlags.runAuditConfig),
		runner.WithAuditEnabled(runFlags.runEnableAudit, runFlags.runAuditConfig),
		runner.WithPermissionProfileNameOrPath(runFlags.runPermissionProfile),
		runner.WithNetworkIsolation(runFlags.runIsolateNetwork),
		runner.WithEnvVars(envVarsMap),
		runner.WithToolsFilter(runFlags.runToolsFilter),
		runner.WithEnvFilesFromDirectory(runFlags.runEnvFileDir),
		runner.WithK8sPodPatch(runFlags.runK8sPodPatch),
		runner.WithOIDCConfig(
			runFlags.oidcIssuer,
			runFlags.oidcAudience,
			runFlags.oidcJwksURL,
			runFlags.oidcIntrospectionURL,
			runFlags.oidcClientID,
			runFlags.oidcClientSecret,
			runFlags.runThvCABundle,
			runFlags.runJWKSAuthTokenFile,
			runFlags.runResourceURL,
			runFlags.runJWKSAllowPrivateIP,
		),
		runner.WithTelemetryConfig(
			runFlags.runOtelEndpoint,
			runFlags.enablePrometheusMetricsPath,
			runFlags.runOtelTracingEnabled,
			runFlags.runOtelMetricsEnabled,
			runFlags.runOtelServiceName,
			runFlags.runOtelTracingSamplingRate,
			runFlags.runOtelHeaders,
			runFlags.runOtelInsecure,
			[]string{}, // CLI flags don't include telemetry environment variables, pass empty slice
		),
	}

	runConfig, err := runner.NewRunConfigBuilder(ctx, imageMetadata, envVarsMap, envVarValidator, opts...)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig: %v", err)
	}

	workloadManager, err := workloads.NewManagerFromRuntime(rt)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}
	return workloadManager.RunWorkload(ctx, runConfig)
}

// runWithFileBasedConfig handles execution when a runconfig.json file is found.
// Uses config from file exactly as-is, ignoring all CLI configuration flags.
// Only uses essential non-configuration inputs: image, command args, and --k8s-pod-patch.
func runWithFileBasedConfig(
	ctx context.Context,
	cmd *cobra.Command,
	mcpServerImage string,
	cmdArgs []string,
	config *runner.RunConfig,
	rt runtime.Runtime,
	debugMode bool,
	envVarValidator runner.EnvVarValidator,
	imageMetadata *registry.ImageMetadata,
) error {
	// Use the file config directly with minimal essential overrides
	config.Image = mcpServerImage
	config.CmdArgs = cmdArgs
	config.Deployer = rt
	config.Debug = debugMode

	// Apply --k8s-pod-patch flag if provided (essential for K8s operation)
	if cmd.Flags().Changed("k8s-pod-patch") && runFlags.runK8sPodPatch != "" {
		config.K8sPodTemplatePatch = runFlags.runK8sPodPatch
	}

	// Validate environment variables using the provided validator
	if envVarValidator != nil {
		validatedEnvVars, err := envVarValidator.Validate(ctx, imageMetadata, config, config.EnvVars)
		if err != nil {
			return fmt.Errorf("failed to validate environment variables: %v", err)
		}
		config.EnvVars = validatedEnvVars
	}

	// Apply image metadata overrides if needed (similar to what the builder does)
	if imageMetadata != nil && config.Name == "" {
		config.Name = imageMetadata.Name
	}

	workloadManager, err := workloads.NewManagerFromRuntime(rt)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}
	return workloadManager.RunWorkload(ctx, config)
}
