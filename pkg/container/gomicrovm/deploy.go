// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/go-microvm"
	"github.com/stacklok/go-microvm/hooks"
	"github.com/stacklok/go-microvm/hypervisor/libkrun"
	microvmimage "github.com/stacklok/go-microvm/image"
	microvmssh "github.com/stacklok/go-microvm/ssh"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// stdioRelayGuestPort is the well-known TCP port inside the guest VM where
	// the stdio relay listens. The guest namespace is fully isolated so a fixed
	// port avoids parameterization.
	stdioRelayGuestPort = 9999

	// stdioRelayPortEnvVar is the environment variable injected into the guest
	// so thv-vm-init knows which port to listen on for the stdio relay.
	stdioRelayPortEnvVar = "THV_STDIO_RELAY_PORT"

	// transportStdio is the transport type string for stdio.
	transportStdio = "stdio"
)

// DeployWorkload creates and starts a microVM workload.
// When isolateNetwork is true the permission profile's network rules are
// enforced via go-microvm's egress policy. When false, all egress is allowed
// regardless of the permission profile.
func (c *Client) DeployWorkload(
	ctx context.Context,
	image, name string,
	command []string,
	envVars, labels map[string]string,
	permissionProfile *permissions.Profile,
	transportType string,
	options *runtime.DeployWorkloadOptions,
	isolateNetwork bool,
) (int, error) {
	containerPort, err := extractContainerPort(options)
	if err != nil {
		return 0, fmt.Errorf("go-microvm runtime: %w", err)
	}

	// For HTTP transports, allocate a host port for the MCP server.
	// For stdio, there is no HTTP port — the relay port is used instead.
	var hostPort int
	if transportType != transportStdio {
		hostPort = networking.FindAvailable()
		if hostPort == 0 {
			return 0, fmt.Errorf("go-microvm runtime: could not find an available host port")
		}
	}

	// For stdio transport, allocate a host port for the TCP stdio relay.
	var stdioRelayHostPort int
	if transportType == transportStdio {
		stdioRelayHostPort = networking.FindAvailable()
		if stdioRelayHostPort == 0 {
			return 0, fmt.Errorf("go-microvm runtime: could not find an available host port for stdio relay")
		}
		envVars[stdioRelayPortEnvVar] = strconv.Itoa(stdioRelayGuestPort)
	}

	vmDir := filepath.Join(c.opts.dataDir, "vms", name)
	if err := os.MkdirAll(vmDir, 0o700); err != nil {
		return 0, fmt.Errorf("go-microvm runtime: creating VM data dir: %w", err)
	}

	opts, err := c.buildMicrovmOptions(name, vmDir, command, envVars, permissionProfile,
		containerPort, hostPort, stdioRelayHostPort, transportType, isolateNetwork)
	if err != nil {
		return 0, err
	}

	vm, err := microvm.Run(ctx, image, opts...)
	if err != nil {
		return 0, fmt.Errorf("go-microvm runtime: starting VM: %w", err)
	}

	var ports []runtime.PortMapping
	if containerPort > 0 {
		ports = []runtime.PortMapping{{
			ContainerPort: containerPort,
			HostPort:      hostPort,
			Protocol:      "tcp",
		}}
	}

	entry := &vmEntry{
		name:               name,
		image:              image,
		labels:             labels,
		state:              runtime.WorkloadStatusRunning,
		vm:                 vm,
		createdAt:          time.Now(),
		dataDir:            vmDir,
		ports:              ports,
		transportType:      transportType,
		stdioRelayHostPort: stdioRelayHostPort,
	}

	c.mu.Lock()
	c.vms[name] = entry
	c.mu.Unlock()

	if err := persistVMState(vmDir, entry); err != nil {
		// Non-fatal — the VM is running, state recovery just won't work for this VM.
		slog.Warn("failed to persist VM state", "error", err)
	}

	// For stdio transport, hostPort is 0 because there is no HTTP server port.
	// The caller uses AttachToWorkload() to connect to the stdio relay instead.
	return hostPort, nil
}

// buildMicrovmOptions constructs the microvm.Option slice for a VM deployment.
// When isolateNetwork is true, the egress policy derived from the permission
// profile is applied to restrict outbound traffic. When false, no egress
// restrictions are set regardless of the permission profile.
func (c *Client) buildMicrovmOptions(
	name, vmDir string,
	command []string,
	envVars map[string]string,
	permissionProfile *permissions.Profile,
	containerPort, hostPort, stdioRelayHostPort int,
	transportType string,
	isolateNetwork bool,
) ([]microvm.Option, error) {
	// Generate ephemeral SSH key pair for the guest SSH server started by
	// boot.Run(). The private key is kept on the host for debugging access.
	_, pubKeyPath, err := microvmssh.GenerateKeyPair(vmDir)
	if err != nil {
		return nil, fmt.Errorf("go-microvm runtime: generating SSH key pair: %w", err)
	}
	pubKey, err := microvmssh.GetPublicKeyContent(pubKeyPath)
	if err != nil {
		return nil, fmt.Errorf("go-microvm runtime: reading public key: %w", err)
	}

	backendOpts := []libkrun.Option{libkrun.WithUserNamespaceUID(1000, 1000)}
	if runnerPath, err := FindRunnerPath(); err == nil {
		backendOpts = append(backendOpts, libkrun.WithRunnerPath(runnerPath))
	}

	opts := []microvm.Option{
		microvm.WithName(name),
		microvm.WithCPUs(c.opts.cpus),
		microvm.WithMemory(c.opts.memory),
		microvm.WithDataDir(vmDir),
		microvm.WithCleanDataDir(),
		microvm.WithBackend(libkrun.NewBackend(backendOpts...)),
		microvm.WithLogLevel(c.opts.logLevel),
		microvm.WithInitOverride("/thv-vm-init"),
	}

	if c.opts.imageCacheDir != "" {
		opts = append(opts, microvm.WithImageCache(microvmimage.NewCache(c.opts.imageCacheDir)))
	}

	portOpts, err := buildPortForwardOptions(containerPort, hostPort, stdioRelayHostPort)
	if err != nil {
		return nil, err
	}
	opts = append(opts, portOpts...)

	if isolateNetwork && permissionProfile != nil && permissionProfile.Network != nil {
		if ep := buildEgressPolicy(permissionProfile.Network); ep != nil {
			opts = append(opts, microvm.WithEgressPolicy(*ep))
		}
	}

	permConfig := mapPermissionProfile(permissionProfile)
	if fsMounts := buildVirtioFSMounts(permConfig); len(fsMounts) > 0 {
		opts = append(opts, microvm.WithVirtioFS(fsMounts...))
	}

	opts = append(opts, microvm.WithRootFSHook(buildRootFSHooks(command, pubKey, envVars)...))

	// Add readiness probe: HTTP for container-port transports, TCP for stdio relay.
	switch {
	case transportType == transportStdio && stdioRelayHostPort > 0:
		opts = append(opts, microvm.WithPostBoot(tcpReadinessProbe(stdioRelayHostPort)))
	case containerPort > 0:
		opts = append(opts, microvm.WithPostBoot(httpReadinessProbe(hostPort)))
	}

	return opts, nil
}

// buildPortForwardOptions constructs the port forward microvm.Options for the
// MCP server port and/or the stdio relay port.
func buildPortForwardOptions(containerPort, hostPort, stdioRelayHostPort int) ([]microvm.Option, error) {
	var opts []microvm.Option

	if containerPort > 0 {
		if hostPort > math.MaxUint16 || containerPort > math.MaxUint16 {
			return nil, fmt.Errorf("go-microvm runtime: port value out of range (host=%d, container=%d)", hostPort, containerPort)
		}
		opts = append(opts, microvm.WithPorts(microvm.PortForward{
			Host:  uint16(hostPort),      //nolint:gosec // bounds checked above
			Guest: uint16(containerPort), //nolint:gosec // bounds checked above
		}))
	}

	if stdioRelayHostPort > 0 {
		if stdioRelayHostPort > math.MaxUint16 {
			return nil, fmt.Errorf("go-microvm runtime: stdio relay port out of range (%d)", stdioRelayHostPort)
		}
		opts = append(opts, microvm.WithPorts(microvm.PortForward{
			Host:  uint16(stdioRelayHostPort), //nolint:gosec // bounds checked above
			Guest: uint16(stdioRelayGuestPort),
		}))
	}

	return opts, nil
}

// buildRootFSHooks constructs the rootfs hooks for init binary, entrypoint,
// SSH keys, and optional environment variables.
func buildRootFSHooks(command []string, pubKey string, envVars map[string]string) []microvm.RootFSHook {
	entrypointHook := InjectEntrypoint()
	if len(command) > 0 {
		entrypointHook = InjectEntrypointOverride(command)
	}
	hks := []microvm.RootFSHook{
		InjectInitBinary(),
		entrypointHook,
		InjectSSHKeys(pubKey),
	}
	if len(envVars) > 0 {
		hks = append(hks, hooks.InjectEnvFile("/etc/environment", envVars))
	}
	return hks
}

// extractContainerPort parses the first exposed port from DeployWorkloadOptions.
// The port key format is "port/protocol" (e.g., "8080/tcp") or just "port".
// Returns 0 if options is nil or no ports are exposed.
func extractContainerPort(options *runtime.DeployWorkloadOptions) (int, error) {
	if options == nil || len(options.ExposedPorts) == 0 {
		return 0, nil
	}

	for key := range options.ExposedPorts {
		portStr := strings.Split(key, "/")[0]
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return 0, fmt.Errorf("invalid port %q: %w", key, err)
		}
		return port, nil
	}
	return 0, nil
}
