// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package runtime provides workload deployment setup functionality
// that was previously part of the transport package.
package runtime

import (
	"context"
	"fmt"
	"log/slog"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

var transportEnvMap = map[types.TransportType]string{
	types.TransportTypeSSE:            "sse",
	types.TransportTypeStreamableHTTP: "streamable-http",
	types.TransportTypeStdio:          "stdio",
}

// SetupResult contains the results of setting up a workload
type SetupResult struct {
	ContainerName string
	TargetURI     string
	TargetPort    int
	TargetHost    string
}

// Setup prepares and deploys a workload for use with a transport.
// The runtime parameter provides access to container operations.
// The permissionProfile is used to configure container permissions (including network mode).
// The k8sPodTemplatePatch is a JSON string to patch the Kubernetes pod template.
// Returns the container name and target URI for configuring the transport.
func Setup(
	ctx context.Context,
	transportType types.TransportType,
	runtime rt.Deployer,
	containerName string,
	image string,
	cmdArgs []string,
	envVars, labels map[string]string,
	permissionProfile *permissions.Profile,
	k8sPodTemplatePatch string,
	isolateNetwork bool,
	ignoreConfig *ignore.Config,
	host string,
	targetPort int,
	targetHost string,
) (*SetupResult, error) {
	// Add transport-specific environment variables
	env, ok := transportEnvMap[transportType]
	if !ok && transportType != types.TransportTypeStdio {
		return nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}

	// For stdio transport, env is already set above
	if transportType == types.TransportTypeStdio {
		envVars["MCP_TRANSPORT"] = "stdio"
	} else {
		envVars["MCP_TRANSPORT"] = env

		// Use the target port for the container's environment variables
		envVars["MCP_PORT"] = fmt.Sprintf("%d", targetPort)
		envVars["FASTMCP_PORT"] = fmt.Sprintf("%d", targetPort)
		envVars["MCP_HOST"] = targetHost
	}

	// Create workload options
	containerOptions := rt.NewDeployWorkloadOptions()
	containerOptions.K8sPodTemplatePatch = k8sPodTemplatePatch
	containerOptions.IgnoreConfig = ignoreConfig

	if transportType == types.TransportTypeStdio {
		containerOptions.AttachStdio = true
	} else {
		// Expose the target port in the container
		containerPortStr := fmt.Sprintf("%d/tcp", targetPort)
		containerOptions.ExposedPorts[containerPortStr] = struct{}{}

		// Create host port bindings (configurable through the --host flag)
		portBindings := []rt.PortBinding{
			{
				HostIP:   host,
				HostPort: fmt.Sprintf("%d", targetPort),
			},
		}

		// Set the port bindings
		containerOptions.PortBindings[containerPortStr] = portBindings
	}

	// Create the container
	slog.Debug("Deploying workload", "container", containerName, "image", image)
	exposedPort, err := runtime.DeployWorkload(
		ctx,
		image,
		containerName,
		cmdArgs,
		envVars,
		labels,
		permissionProfile,
		transportType.String(),
		containerOptions,
		isolateNetwork,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}
	slog.Debug("Container created", "container", containerName)

	result := &SetupResult{
		ContainerName: containerName,
		TargetHost:    targetHost,
		TargetPort:    targetPort,
	}

	// For stdio transport, there's no target URI
	if transportType == types.TransportTypeStdio {
		result.TargetURI = ""
	} else {
		// Update target host and port if needed (for Kubernetes)
		if (transportType == types.TransportTypeSSE || transportType == types.TransportTypeStreamableHTTP) && rt.IsKubernetesRuntime() {
			// If the SSEHeadlessServiceName is set, use it as the target host
			if containerOptions.SSEHeadlessServiceName != "" {
				result.TargetHost = containerOptions.SSEHeadlessServiceName
			}
		}

		// we don't want to override the targetPort in a Kubernetes deployment. Because
		// by default the Kubernetes container deployer returns `0` for the exposedPort
		// therefore causing the "target port not set" error when it is assigned to the targetPort.
		// Issues:
		// - https://github.com/stacklok/toolhive/issues/902
		// - https://github.com/stacklok/toolhive/issues/924
		if !rt.IsKubernetesRuntime() {
			result.TargetPort = exposedPort
		}

		// Construct target URI
		result.TargetURI = fmt.Sprintf("http://%s:%d", result.TargetHost, result.TargetPort)
	}

	return result, nil
}
