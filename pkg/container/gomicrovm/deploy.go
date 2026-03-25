// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
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
	if transportType == "stdio" {
		return 0, fmt.Errorf("go-microvm runtime: stdio transport is not supported — use SSE or streamable-http")
	}

	containerPort, err := extractContainerPort(options)
	if err != nil {
		return 0, fmt.Errorf("go-microvm runtime: %w", err)
	}

	hostPort := networking.FindAvailable()
	if hostPort == 0 {
		return 0, fmt.Errorf("go-microvm runtime: could not find an available host port")
	}

	vmDir := fmt.Sprintf("%s/vms/%s", c.opts.dataDir, name)
	if err := os.MkdirAll(vmDir, 0o700); err != nil {
		return 0, fmt.Errorf("go-microvm runtime: creating VM data dir: %w", err)
	}

	opts, err := c.buildMicrovmOptions(name, vmDir, command, envVars, permissionProfile, containerPort, hostPort, isolateNetwork)
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
		name:          name,
		image:         image,
		labels:        labels,
		state:         runtime.WorkloadStatusRunning,
		vm:            vm,
		createdAt:     time.Now(),
		dataDir:       vmDir,
		ports:         ports,
		transportType: transportType,
	}

	c.mu.Lock()
	c.vms[name] = entry
	c.mu.Unlock()

	if err := persistVMState(vmDir, entry); err != nil {
		// Non-fatal — the VM is running, state recovery just won't work for this VM.
		slog.Warn("failed to persist VM state", "error", err)
	}

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
	containerPort, hostPort int,
	isolateNetwork bool,
) ([]microvm.Option, error) {
	// Generate ephemeral SSH key pair. boot.Run() requires an authorized_keys
	// file even though we don't actively SSH into the guest.
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

	if containerPort > 0 {
		if hostPort > math.MaxUint16 || containerPort > math.MaxUint16 {
			return nil, fmt.Errorf("go-microvm runtime: port value out of range (host=%d, container=%d)", hostPort, containerPort)
		}
		opts = append(opts, microvm.WithPorts(microvm.PortForward{
			Host:  uint16(hostPort),      //nolint:gosec // bounds checked above
			Guest: uint16(containerPort), //nolint:gosec // bounds checked above
		}))
	}

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

	// Add readiness probe when a port is exposed.
	if containerPort > 0 {
		opts = append(opts, microvm.WithPostBoot(httpReadinessProbe(hostPort)))
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
