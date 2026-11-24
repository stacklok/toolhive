// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/internal/netproxy"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	defaultPollInterval      = 100 * time.Millisecond
	defaultPollTimeout       = 10 * time.Second
	rollbackTimeout          = 15 * time.Second
	stopTimeoutSeconds       = 30
	attachedCloseGracePeriod = 200 * time.Millisecond
)

// Client implements runtime.Runtime and runtime.HostGatewayResolver using
// Apple's macOS native `container` CLI (apple/container 1.0+).
type Client struct {
	runner           CommandRunner
	imageManager     images.ImageManager
	pollInterval     time.Duration
	pollTimeout      time.Duration
	hostOnlyWarnOnce sync.Once
}

// Compile-time assertions that Client implements the expected runtime interfaces.
var (
	_ runtime.Runtime             = (*Client)(nil)
	_ runtime.HostGatewayResolver = (*Client)(nil)
)

// NewClient verifies that the Apple container runtime is available on this host
// and returns a new Client instance.
func NewClient(ctx context.Context) (*Client, error) {
	runner := execCommandRunner{}
	if err := checkAvailability(ctx, runner, goruntime.GOOS, goruntime.GOARCH); err != nil {
		return nil, err
	}
	return newClientWithDependencies(runner, newImageManagerWithRunner(runner)), nil
}

// RuntimeType returns runtime.TypeApple.
func (*Client) RuntimeType() runtime.Type {
	return runtime.TypeApple
}

// HostGateway returns the vmnet gateway IP that containers can use to reach
// services listening on the macOS host.
func (client *Client) HostGateway(ctx context.Context) (string, error) {
	return client.resolveHostGateway(ctx)
}

// DeployWorkload creates and starts a workload (or creates it ready for
// two-phase stdio attachment when AttachStdio is true).
func (client *Client) DeployWorkload(
	ctx context.Context,
	image, name string,
	command []string,
	envVars, labels map[string]string,
	permissionProfile *permissions.Profile,
	transportType string,
	options *runtime.DeployWorkloadOptions,
	isolateNetwork bool,
) (int, error) {
	deployEnvVars := cloneStringMap(envVars)
	deployLabels := cloneStringMap(labels)

	permConfig, attachStdio, effectiveIsolation, err := resolveDeploySettings(
		permissionProfile, transportType, options, isolateNetwork,
	)
	if err != nil {
		return 0, err
	}

	if err := validateEnvVars(deployEnvVars); err != nil {
		return 0, fmt.Errorf("failed to prepare environment file for container %q: %w", name, err)
	}

	var isolationPreflight *isolationPreflightConfig
	if effectiveIsolation {
		isolationPreflight, err = client.preflightNetworkIsolation(ctx, transportType, options)
		if err != nil {
			return 0, err
		}
	}

	resolvedPortBindings, hostPort, err := resolveDirectPortBindings(effectiveIsolation, options, deployLabels)
	if err != nil {
		return 0, err
	}

	lb.AddNetworkIsolationLabel(deployLabels, effectiveIsolation)

	if err := client.deleteContainerIfPresent(ctx, name); err != nil {
		return 0, fmt.Errorf("failed to clean up existing container %q: %w", name, err)
	}

	rollback := &deployRollback{client: client}
	committed := false
	defer func() {
		if !committed {
			rollback.execute(ctx)
		}
	}()

	var isolation *isolatedNetworkSetup
	if effectiveIsolation {
		isolation, err = client.prepareNetworkIsolation(
			ctx, name, transportType, attachStdio, permissionProfile, options, isolationPreflight, rollback,
		)
		if err != nil {
			return 0, err
		}
		deployEnvVars = netproxy.AddEgressEnvVars(deployEnvVars, isolation.egressIP)
	}

	internalNet := ""
	if isolation != nil {
		internalNet = isolation.internalNet
	}

	if err := client.launchMCPContainer(
		ctx, name, image, command, deployEnvVars, deployLabels,
		attachStdio, permConfig, internalNet, resolvedPortBindings, effectiveIsolation,
	); err != nil {
		return 0, err
	}
	rollback.mcpName = name

	if transportType == "stdio" {
		committed = true
		return 0, nil
	}

	if isolation != nil {
		hostPort, err = client.finalizeIsolatedIngress(ctx, name, isolation, rollback)
		if err != nil {
			return 0, err
		}
	}

	committed = true
	return hostPort, nil
}

// ListWorkloads returns all non-auxiliary ToolHive containers managed by Apple container.
func (client *Client) ListWorkloads(ctx context.Context) ([]runtime.ContainerInfo, error) {
	containers, err := client.listContainers(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]runtime.ContainerInfo, 0, len(containers))
	for _, cont := range containers {
		if !isToolHiveWorkloadContainer(cont) {
			continue
		}
		result = append(result, toContainerInfo(cont))
	}

	return result, nil
}

// StopWorkload gracefully stops a workload and any associated proxy sidecars.
// Stopping a missing or already-stopped workload succeeds idempotently.
func (client *Client) StopWorkload(ctx context.Context, workloadName string) error {
	info, err := client.GetWorkloadInfo(ctx, workloadName)
	if err != nil {
		if errors.Is(err, runtime.ErrWorkloadNotFound) {
			return nil
		}
		return err
	}

	containerName := info.Name
	if containerName == "" {
		containerName = workloadName
	}

	if info.State == runtime.WorkloadStatusRunning || info.State == runtime.WorkloadStatusStarting {
		if _, stopErr := client.runner.Run(
			ctx, containerBinary, "stop", "--time", strconv.Itoa(stopTimeoutSeconds), "--", containerName,
		); stopErr != nil && !isNotFoundError(stopErr) {
			return runtime.NewContainerError(stopErr, containerName, fmt.Sprintf("failed to stop workload: %v", stopErr))
		}
	}

	if lb.HasNetworkIsolation(info.Labels) {
		client.stopProxyContainers(ctx, containerName)
	}

	return nil
}

// RemoveWorkload removes a workload, its proxy containers, their temp config
// directories, and any isolated internal network.
func (client *Client) RemoveWorkload(ctx context.Context, workloadName string) error {
	containerName := workloadName
	var containerLabels map[string]string

	cont, inspectErr := client.inspectWorkloadByName(ctx, workloadName)
	if inspectErr != nil {
		if !errors.Is(inspectErr, runtime.ErrWorkloadNotFound) {
			slog.Warn("failed to inspect container before removal", "name", workloadName, "error", inspectErr)
		}
	} else {
		if resolved := cont.containerID(); resolved != "" {
			containerName = resolved
		}
		containerLabels = cont.Configuration.Labels
	}

	_, delErr := client.runner.Run(ctx, containerBinary, "delete", "--force", "--", containerName)
	if delErr != nil && !isNotFoundError(delErr) {
		return runtime.NewContainerError(delErr, containerName, fmt.Sprintf("failed to remove workload: %v", delErr))
	}

	if containerLabels == nil || lb.HasNetworkIsolation(containerLabels) {
		client.removeProxyContainers(ctx, containerName)
		if err := client.deleteInternalNetwork(ctx, internalNetworkName(containerName)); err != nil {
			slog.Warn("failed to delete internal network for container", "name", containerName, "error", err)
		}
	}

	return nil
}

// GetWorkloadLogs retrieves or streams logs from the primary container of a workload.
func (client *Client) GetWorkloadLogs(ctx context.Context, workloadName string, follow bool, lines int) (string, error) {
	if follow && lines > 0 {
		return "", runtime.NewContainerError(
			fmt.Errorf("cannot use both follow and line limit"),
			workloadName,
			"follow mode streams logs indefinitely, which conflicts with line limiting",
		)
	}

	cont, err := client.inspectWorkloadByName(ctx, workloadName)
	if err != nil {
		if errors.Is(err, runtime.ErrWorkloadNotFound) {
			return "", err
		}
		return "", runtime.NewContainerError(err, workloadName, fmt.Sprintf("failed to inspect workload: %v", err))
	}

	containerName := cont.containerID()
	if containerName == "" {
		containerName = workloadName
	}

	args := []string{"logs"}
	if follow {
		args = append(args, "--follow")
	}
	if lines > 0 {
		args = append(args, "-n", strconv.Itoa(lines))
	}
	args = append(args, "--", containerName)

	if follow {
		if err := client.runner.RunStreaming(ctx, os.Stdout, os.Stderr, containerBinary, args...); err != nil &&
			!errors.Is(err, context.Canceled) {
			return "", runtime.NewContainerError(err, containerName, fmt.Sprintf("failed to follow workload logs: %v", err))
		}
		return "", nil
	}

	out, err := client.runner.Run(ctx, containerBinary, args...)
	if err != nil {
		return "", runtime.NewContainerError(err, containerName, fmt.Sprintf("failed to get workload logs: %v", err))
	}
	return string(out), nil
}

// IsWorkloadRunning reports whether the named workload container is currently running.
func (client *Client) IsWorkloadRunning(ctx context.Context, workloadName string) (bool, error) {
	cont, err := client.inspectWorkloadByName(ctx, workloadName)
	if err != nil {
		if errors.Is(err, runtime.ErrWorkloadNotFound) {
			return false, err
		}
		return false, runtime.NewContainerError(err, workloadName, fmt.Sprintf("failed to inspect workload: %v", err))
	}
	return appleToDomainStatus(cont.Status) == runtime.WorkloadStatusRunning, nil
}

// GetWorkloadInfo returns metadata and status for the named workload container.
func (client *Client) GetWorkloadInfo(ctx context.Context, workloadName string) (runtime.ContainerInfo, error) {
	cont, err := client.inspectWorkloadByName(ctx, workloadName)
	if err != nil {
		if errors.Is(err, runtime.ErrWorkloadNotFound) {
			return runtime.ContainerInfo{}, err
		}
		return runtime.ContainerInfo{}, runtime.NewContainerError(err, workloadName, fmt.Sprintf("failed to inspect workload: %v", err))
	}
	return toContainerInfo(cont), nil
}

// AttachToWorkload starts a previously created stdio container with attached
// stdin and stdout streams (`container start -a -i <name>`).
// Re-attaching to an already-running Apple container is unsupported and returns
// runtime.ErrReattachNotSupported.
func (client *Client) AttachToWorkload(ctx context.Context, workloadName string) (io.WriteCloser, io.ReadCloser, error) {
	cont, err := client.inspectWorkloadByName(ctx, workloadName)
	if err != nil {
		if errors.Is(err, runtime.ErrWorkloadNotFound) {
			return nil, nil, err
		}
		return nil, nil, runtime.NewContainerError(err, workloadName, fmt.Sprintf("failed to inspect workload: %v", err))
	}

	containerName := cont.containerID()
	if containerName == "" {
		containerName = workloadName
	}

	if appleToDomainStatus(cont.Status) == runtime.WorkloadStatusRunning {
		return nil, nil, runtime.NewContainerError(
			runtime.ErrReattachNotSupported,
			containerName,
			"Apple container does not support re-attaching stdio to an already running container",
		)
	}

	prevStartedAt := cont.StartedAt
	proc, err := client.runner.StartAttached(context.WithoutCancel(ctx), containerBinary, "start", "-a", "-i", "--", containerName)
	if err != nil {
		return nil, nil, runtime.NewContainerError(err, containerName, fmt.Sprintf("failed to start attached container: %v", err))
	}

	stderrDone := make(chan struct{})
	go drainStderr(proc.Stderr(), containerName, stderrDone)

	if err := client.waitForContainerRunning(ctx, containerName, prevStartedAt); err != nil {
		_ = proc.Kill()
		_ = proc.Stdin().Close()
		_ = proc.Stdout().Close()
		<-stderrDone
		_ = proc.Wait()
		return nil, nil, runtime.NewContainerError(err, containerName, "container failed to enter running state after attach")
	}

	wrappedStdout := newAttachedReadCloser(proc, stderrDone)

	return proc.Stdin(), wrappedStdout, nil
}

// IsRunning verifies that the Apple container daemon is responsive.
func (client *Client) IsRunning(ctx context.Context) error {
	if _, err := client.runner.Run(ctx, containerBinary, "system", "status"); err != nil {
		return fmt.Errorf("apple container daemon is not running: %w", err)
	}
	return nil
}

func newClientWithDependencies(runner CommandRunner, imageManager images.ImageManager) *Client {
	return &Client{
		runner:       runner,
		imageManager: imageManager,
		pollInterval: defaultPollInterval,
		pollTimeout:  defaultPollTimeout,
	}
}

func (client *Client) listContainers(ctx context.Context) ([]appleContainerJSON, error) {
	out, err := client.runner.Run(ctx, containerBinary, "list", "--all", "--format", "json")
	if err != nil {
		return nil, runtime.NewContainerError(err, "", fmt.Sprintf("failed to list containers: %v", err))
	}

	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []appleContainerJSON{}, nil
	}

	var containers []appleContainerJSON
	if err := json.Unmarshal(trimmed, &containers); err != nil {
		return nil, runtime.NewContainerError(err, "", fmt.Sprintf("failed to parse container list: %v", err))
	}
	return containers, nil
}

func isToolHiveWorkloadContainer(cont appleContainerJSON) bool {
	if cont.Configuration.Labels[lb.LabelToolHive] != lb.LabelToolHiveValue {
		return false
	}
	return cont.Configuration.Labels[netproxy.AuxiliaryWorkloadLabel] != netproxy.LabelValueTrue
}

// inspectWorkloadByName resolves a ToolHive workload container by its
// toolhive-basename label first (preferring a running container when multiple
// share a base name, matching Docker), then falls back to exact container
// name/ID matching. Returns runtime.ErrWorkloadNotFound when no match exists.
func (client *Client) inspectWorkloadByName(ctx context.Context, workloadName string) (appleContainerJSON, error) {
	containers, err := client.listContainers(ctx)
	if err != nil {
		return appleContainerJSON{}, err
	}

	var firstBaseNameMatch *appleContainerJSON
	var exactNameMatch *appleContainerJSON

	for idx := range containers {
		cont := &containers[idx]
		if !isToolHiveWorkloadContainer(*cont) {
			continue
		}

		if cont.Configuration.Labels[lb.LabelBaseName] == workloadName {
			if appleToDomainStatus(cont.Status) == runtime.WorkloadStatusRunning {
				return *cont, nil
			}
			if firstBaseNameMatch == nil {
				firstBaseNameMatch = cont
			}
		}

		if exactNameMatch == nil && cont.containerID() == workloadName {
			exactNameMatch = cont
		}
	}

	if firstBaseNameMatch != nil {
		return *firstBaseNameMatch, nil
	}
	if exactNameMatch != nil {
		return *exactNameMatch, nil
	}

	return appleContainerJSON{}, runtime.NewContainerError(runtime.ErrWorkloadNotFound, workloadName, "no containers found")
}

func (client *Client) inspectContainer(ctx context.Context, containerName string) (appleContainerJSON, error) {
	out, err := client.runner.Run(ctx, containerBinary, "inspect", "--", containerName)
	if err != nil {
		if isNotFoundError(err) {
			return appleContainerJSON{}, runtime.ErrContainerNotFound
		}
		return appleContainerJSON{}, err
	}

	return parseInspectOutput(out)
}

func (client *Client) deleteContainerIfPresent(ctx context.Context, containerName string) error {
	cont, err := client.inspectContainer(ctx, containerName)
	if errors.Is(err, runtime.ErrContainerNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	_, delErr := client.runner.Run(ctx, containerBinary, "delete", "--force", "--", containerName)
	if delErr != nil && !isNotFoundError(delErr) {
		return delErr
	}

	if lb.HasNetworkIsolation(cont.Configuration.Labels) {
		client.removeProxyContainers(ctx, containerName)
		if netErr := client.deleteInternalNetwork(ctx, internalNetworkName(containerName)); netErr != nil {
			slog.Warn("failed to delete internal network during container replacement",
				"name", containerName, "error", netErr)
		}
	}
	return nil
}

func (client *Client) waitForContainerRunning(ctx context.Context, containerName string, prevStartedAt time.Time) error {
	pollTimeout := client.pollTimeout
	if pollTimeout <= 0 {
		pollTimeout = defaultPollTimeout
	}
	pollInterval := client.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}

	seenTransition := false
	deadline := time.Now().Add(pollTimeout)
	for {
		cont, err := client.inspectContainer(ctx, containerName)
		if err == nil {
			ready, stateErr := evaluateStartupState(cont, containerName, prevStartedAt, &seenTransition)
			if stateErr != nil {
				return stateErr
			}
			if ready {
				return nil
			}
		} else if !errors.Is(err, runtime.ErrContainerNotFound) {
			return fmt.Errorf("failed to inspect container %q during startup: %w", containerName, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("timed out waiting for container %q to start", containerName)
		}

		waitDur := min(pollInterval, remaining)
		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func evaluateStartupState(
	cont appleContainerJSON,
	containerName string,
	prevStartedAt time.Time,
	seenTransition *bool,
) (bool, error) {
	switch appleToDomainStatus(cont.Status) {
	case runtime.WorkloadStatusRunning:
		return true, nil
	case runtime.WorkloadStatusStarting:
		*seenTransition = true
		return false, nil
	case runtime.WorkloadStatusError:
		return false, fmt.Errorf("container %q entered error state (%s)", containerName, cont.Status)
	case runtime.WorkloadStatusStopped:
		exited := strings.EqualFold(strings.TrimSpace(cont.Status), "exited")
		restartedAndStopped := !cont.StartedAt.IsZero() && !cont.StartedAt.Equal(prevStartedAt)
		if exited || *seenTransition || restartedAndStopped {
			return false, fmt.Errorf("container %q exited during startup (%s)", containerName, cont.Status)
		}
		return false, nil
	case runtime.WorkloadStatusStopping,
		runtime.WorkloadStatusUnhealthy,
		runtime.WorkloadStatusRemoving,
		runtime.WorkloadStatusUnknown,
		runtime.WorkloadStatusUnauthenticated,
		runtime.WorkloadStatusAuthRetrying,
		runtime.WorkloadStatusPolicyStopped:
		return false, nil
	}
	return false, nil
}

func (client *Client) launchMCPContainer(
	ctx context.Context,
	name, image string,
	command []string,
	envVars, labels map[string]string,
	attachStdio bool,
	permConfig *runtime.PermissionConfig,
	internalNet string,
	portBindings map[string][]runtime.PortBinding,
	isolateNetwork bool,
) error {
	envFilePath, err := writeEnvFile(envVars)
	if err != nil {
		return fmt.Errorf("failed to prepare environment file for container %q: %w", name, err)
	}
	defer removeEnvFile(envFilePath)

	mcpArgs, err := buildMCPContainerArgs(
		name, image, command, envFilePath, labels,
		attachStdio, permConfig, internalNet, portBindings, isolateNetwork,
	)
	if err != nil {
		return fmt.Errorf("failed to build Apple container arguments for %q: %w", name, err)
	}

	_, runErr := client.runner.Run(ctx, containerBinary, mcpArgs...)
	removeEnvFile(envFilePath)
	if runErr != nil {
		return fmt.Errorf("failed to deploy Apple container %q: %w", name, runErr)
	}
	return nil
}

const envFilePrefix = "toolhive-apple-env-"

func buildMCPContainerArgs(
	name string,
	image string,
	command []string,
	envFilePath string,
	labels map[string]string,
	attachStdio bool,
	permConfig *runtime.PermissionConfig,
	internalNet string,
	portBindings map[string][]runtime.PortBinding,
	effectiveIsolation bool,
) ([]string, error) {
	var args []string
	if attachStdio {
		// Two-phase stdio lifecycle: create the container now and start it
		// attached (`container start -a -i`) in AttachToWorkload.
		args = []string{"create", "-i", "--name", name}
	} else {
		args = []string{"run", "-d", "--name", name}
	}

	if effectiveIsolation {
		args = append(args, "--network", internalNet)
	} else if permConfig != nil && permConfig.NetworkMode != "" && !networking.IsBridgeMode(permConfig.NetworkMode) {
		args = append(args, "--network", permConfig.NetworkMode)
	}

	var err error
	args, err = appendSecurityArgs(args, permConfig)
	if err != nil {
		return nil, err
	}
	args, err = appendMountArgs(args, permConfig)
	if err != nil {
		return nil, err
	}

	if !effectiveIsolation && len(portBindings) > 0 {
		args = appendSortedPortBindings(args, portBindings)
	}

	if envFilePath != "" {
		args = append(args, "--env-file", envFilePath)
	}
	args = appendSortedLabels(args, labels)
	args = append(args, "--", image)

	if len(command) > 0 {
		args = append(args, command...)
	}

	return args, nil
}

func writeEnvFile(envVars map[string]string) (string, error) {
	if len(envVars) == 0 {
		return "", nil
	}

	keys := slices.Sorted(maps.Keys(envVars))
	var buf bytes.Buffer
	for _, key := range keys {
		if err := validateEnvFileEntry(key, envVars[key]); err != nil {
			return "", err
		}
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(envVars[key])
		buf.WriteByte('\n')
	}

	tmpFile, err := os.CreateTemp("", envFilePrefix)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary env file: %w", err)
	}
	filePath := tmpFile.Name()

	cleanup := true
	defer func() {
		if cleanup {
			removeEnvFile(filePath)
		}
	}()

	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("failed to set temporary env file permissions: %w", err)
	}
	if _, err := tmpFile.Write(buf.Bytes()); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("failed to write temporary env file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close temporary env file: %w", err)
	}

	cleanup = false
	return filePath, nil
}

func validateEnvVars(envVars map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(envVars)) {
		if err := validateEnvFileEntry(key, envVars[key]); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvFileEntry(key, value string) error {
	if key == "" {
		return errors.New("environment variable name cannot be empty")
	}
	if strings.HasPrefix(key, "#") || strings.TrimSpace(key) != key || strings.ContainsAny(key, "=\r\n\x00") {
		return fmt.Errorf("invalid environment variable name %q for Apple container --env-file", key)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		// Never echo the value in the error because environment variables may contain secrets.
		return fmt.Errorf("environment variable %q contains unsupported newline or NUL characters for Apple container --env-file", key)
	}
	return nil
}

func removeEnvFile(filePath string) {
	if filePath == "" {
		return
	}
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		slog.Debug("failed to remove temporary Apple container env file", "path", filePath, "error", err)
	}
}

func appendSortedLabels(args []string, labels map[string]string) []string {
	if len(labels) == 0 {
		return args
	}
	keys := slices.Sorted(maps.Keys(labels))
	for _, key := range keys {
		args = append(args, "--label", fmt.Sprintf("%s=%s", key, labels[key]))
	}
	return args
}

func appendSortedPortBindings(args []string, portBindings map[string][]runtime.PortBinding) []string {
	keys := slices.Sorted(maps.Keys(portBindings))
	for _, portKey := range keys {
		containerPort, proto := parsePortKey(portKey)
		for _, binding := range portBindings[portKey] {
			var spec string
			if binding.HostIP != "" {
				spec = fmt.Sprintf("%s:%s/%s", net.JoinHostPort(binding.HostIP, binding.HostPort), containerPort, proto)
			} else {
				spec = fmt.Sprintf("%s:%s/%s", binding.HostPort, containerPort, proto)
			}
			args = append(args, "-p", spec)
		}
	}
	return args
}

func parsePortKey(portKey string) (portNum, proto string) {
	parts := strings.SplitN(portKey, "/", 2)
	portNum = parts[0]
	proto = "tcp"
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		proto = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	return portNum, proto
}

func extractFirstPort(options *runtime.DeployWorkloadOptions) (int, error) {
	if options == nil || len(options.ExposedPorts) == 0 {
		return 0, fmt.Errorf("no exposed ports specified in options.ExposedPorts")
	}
	keys := slices.Sorted(maps.Keys(options.ExposedPorts))
	portStr, _ := parsePortKey(keys[0])
	portInt, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("failed to convert port %s to int: %w", portStr, err)
	}
	return portInt, nil
}

func generatePortBindings(
	labels map[string]string,
	portBindings map[string][]runtime.PortBinding,
) (map[string][]runtime.PortBinding, int, error) {
	// Deep-copy map and binding slices so caller input is never mutated.
	cloned := make(map[string][]runtime.PortBinding, len(portBindings))
	for key, bindings := range portBindings {
		cloned[key] = slices.Clone(bindings)
	}

	keys := slices.Sorted(maps.Keys(cloned))
	var hostPort int

	if labels[netproxy.AuxiliaryWorkloadLabel] == netproxy.LabelValueTrue {
		for _, key := range keys {
			bindings := cloned[key]
			if len(bindings) == 0 {
				continue
			}
			parsed, err := strconv.Atoi(bindings[0].HostPort)
			if err != nil {
				return nil, 0, fmt.Errorf("failed to convert host port %s to int: %w", bindings[0].HostPort, err)
			}
			hostPort = parsed
			break
		}
		return cloned, hostPort, nil
	}

	for _, key := range keys {
		bindings := cloned[key]
		for idx := range bindings {
			hostPortStr := bindings[idx].HostPort
			var resolved int
			if hostPortStr == "" || hostPortStr == "0" {
				resolved = networking.FindAvailable()
				if resolved == 0 {
					return nil, 0, fmt.Errorf("could not find an available port")
				}
				bindings[idx].HostPort = strconv.Itoa(resolved)
			} else {
				parsed, err := strconv.Atoi(hostPortStr)
				if err != nil {
					return nil, 0, fmt.Errorf("failed to convert host port %s to int: %w", hostPortStr, err)
				}
				resolved = parsed
			}
			if hostPort == 0 {
				hostPort = resolved
			}
		}
		cloned[key] = bindings
	}

	return cloned, hostPort, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return make(map[string]string)
	}
	return maps.Clone(source)
}

func drainStderr(stderr io.ReadCloser, workloadName string, done chan<- struct{}) {
	defer close(done)
	if stderr == nil {
		return
	}
	defer func() {
		_ = stderr.Close()
	}()

	reader := bufio.NewReader(stderr)
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			slog.Debug("Apple container stderr", "container", workloadName, "output", trimmed)
		}
		if err != nil {
			return
		}
	}
}

type attachedReadCloser struct {
	proc             AttachedProcess
	stdout           io.ReadCloser
	stdin            io.WriteCloser
	stderrDone       <-chan struct{}
	closeGracePeriod time.Duration
	reapOnce         sync.Once
	reapDone         chan struct{}
	reapErr          error
}

func newAttachedReadCloser(proc AttachedProcess, stderrDone <-chan struct{}) *attachedReadCloser {
	return &attachedReadCloser{
		proc:             proc,
		stdout:           proc.Stdout(),
		stdin:            proc.Stdin(),
		stderrDone:       stderrDone,
		closeGracePeriod: attachedCloseGracePeriod,
		reapDone:         make(chan struct{}),
	}
}

func (rc *attachedReadCloser) Read(buf []byte) (int, error) {
	numBytes, err := rc.stdout.Read(buf)
	if errors.Is(err, io.EOF) {
		// All stdout bytes have been consumed; reap the process asynchronously
		// once stderr finishes draining so child processes never become zombies
		// even if the caller does not invoke Close.
		rc.startReap()
	}
	return numBytes, err
}

func (rc *attachedReadCloser) Close() error {
	_ = rc.stdout.Close()
	if rc.stdin != nil {
		_ = rc.stdin.Close()
	}
	rc.startReap()

	select {
	case <-rc.reapDone:
		return rc.reapErr
	default:
	}

	grace := rc.closeGracePeriod
	if grace <= 0 {
		grace = attachedCloseGracePeriod
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case <-rc.reapDone:
		return rc.reapErr
	case <-timer.C:
		_ = rc.proc.Kill()
		<-rc.reapDone
		return rc.reapErr
	}
}

func (rc *attachedReadCloser) startReap() {
	rc.reapOnce.Do(func() {
		go func() {
			defer close(rc.reapDone)
			if rc.stderrDone != nil {
				<-rc.stderrDone
			}
			rc.reapErr = rc.proc.Wait()
		}()
	})
}

type deployRollback struct {
	client           *Client
	mcpName          string
	egressName       string
	egressConfigDir  string
	ingressName      string
	ingressConfigDir string
	internalNet      string
}

func (rb *deployRollback) execute(parentCtx context.Context) {
	if rb.mcpName == "" && rb.egressName == "" && rb.ingressName == "" &&
		rb.internalNet == "" && rb.egressConfigDir == "" && rb.ingressConfigDir == "" {
		return
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), rollbackTimeout)
	defer cancel()

	for _, contName := range []string{rb.ingressName, rb.mcpName, rb.egressName} {
		if contName == "" {
			continue
		}
		_, err := rb.client.runner.Run(cleanupCtx, containerBinary, "delete", "--force", "--", contName)
		if err != nil && !isNotFoundError(err) {
			slog.Debug("rollback failed to delete container", "name", contName, "error", err)
		}
	}

	if rb.ingressConfigDir != "" {
		cleanupProxyConfigDir(rb.ingressConfigDir)
	}
	if rb.egressConfigDir != "" {
		cleanupProxyConfigDir(rb.egressConfigDir)
	}
	if rb.internalNet != "" {
		if err := rb.client.deleteInternalNetwork(cleanupCtx, rb.internalNet); err != nil {
			slog.Debug("rollback failed to delete internal network", "network", rb.internalNet, "error", err)
		}
	}
}

type isolationPreflightConfig struct {
	proxyBackend string
	upstreamPort int
}

type isolatedNetworkSetup struct {
	proxyBackend string
	proxySpec    netproxy.Spec
	internalNet  string
	egressIP     string
}

func (client *Client) preflightNetworkIsolation(
	ctx context.Context,
	transportType string,
	options *runtime.DeployWorkloadOptions,
) (*isolationPreflightConfig, error) {
	if err := checkMacOSVersionForIsolation(ctx, client.runner); err != nil {
		return nil, err
	}

	proxyBackend, err := netproxy.SelectedBackend()
	if err != nil {
		return nil, err
	}

	var upstreamPort int
	if transportType != "stdio" {
		upstreamPort, err = extractFirstPort(options)
		if err != nil {
			return nil, err
		}
	}

	return &isolationPreflightConfig{
		proxyBackend: proxyBackend,
		upstreamPort: upstreamPort,
	}, nil
}

func (client *Client) prepareNetworkIsolation(
	ctx context.Context,
	name, transportType string,
	attachStdio bool,
	permissionProfile *permissions.Profile,
	options *runtime.DeployWorkloadOptions,
	preflight *isolationPreflightConfig,
	rollback *deployRollback,
) (*isolatedNetworkSetup, error) {
	proxyBackend := preflight.proxyBackend
	upstreamPort := preflight.upstreamPort

	internalNet := internalNetworkName(name)
	if err := client.createInternalNetwork(ctx, internalNet); err != nil {
		return nil, err
	}
	rollback.internalNet = internalNet

	client.hostOnlyWarnOnce.Do(func() {
		slog.Warn("Apple container network isolation uses macOS vmnet hostOnly networks: "+
			"direct internet egress and host loopback (127.0.0.1) listeners are blocked, "+
			"but host services bound to 0.0.0.0 remain directly reachable on the internal network gateway IP",
			"workload", name)
	})

	gatewayIPs, err := client.discoverGatewayIPs(ctx, internalNet)
	if err != nil {
		return nil, fmt.Errorf("failed to discover Apple container gateway IPs: %w", err)
	}

	var netPerms *permissions.NetworkPermissions
	if permissionProfile != nil {
		netPerms = permissionProfile.Network
	}

	proxySpec := netproxy.Spec{
		WorkloadName:       name,
		Permissions:        netPerms,
		AllowDockerGateway: options != nil && options.AllowDockerGateway,
		GatewayIPs:         gatewayIPs,
		TransportType:      transportType,
		UpstreamPort:       upstreamPort,
		AttachStdio:        attachStdio,
	}

	egressIP, egressConfigDir, err := client.setupEgressProxy(ctx, proxyBackend, proxySpec, internalNet)
	if err != nil {
		return nil, fmt.Errorf("failed to set up egress proxy: %w", err)
	}
	rollback.egressName = egressContainerName(name)
	rollback.egressConfigDir = egressConfigDir

	return &isolatedNetworkSetup{
		proxyBackend: proxyBackend,
		proxySpec:    proxySpec,
		internalNet:  internalNet,
		egressIP:     egressIP,
	}, nil
}

func (client *Client) finalizeIsolatedIngress(
	ctx context.Context,
	name string,
	isolation *isolatedNetworkSetup,
	rollback *deployRollback,
) (int, error) {
	upstreamIP, err := client.getContainerNetworkIP(ctx, name, isolation.internalNet)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve MCP container IP for ingress: %w", err)
	}
	isolation.proxySpec.UpstreamHost = upstreamIP

	ingressPort, ingressConfigDir, err := client.setupIngressProxy(
		ctx, isolation.proxyBackend, isolation.proxySpec, isolation.internalNet,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to set up ingress proxy: %w", err)
	}
	rollback.ingressName = ingressContainerName(name)
	rollback.ingressConfigDir = ingressConfigDir
	return ingressPort, nil
}

func resolveDirectPortBindings(
	effectiveIsolation bool,
	options *runtime.DeployWorkloadOptions,
	deployLabels map[string]string,
) (map[string][]runtime.PortBinding, int, error) {
	if effectiveIsolation || options == nil || len(options.PortBindings) == 0 {
		return nil, 0, nil
	}
	portBindings, hostPort, err := generatePortBindings(deployLabels, options.PortBindings)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to generate port bindings: %w", err)
	}
	return portBindings, hostPort, nil
}

func resolveDeploySettings(
	permissionProfile *permissions.Profile,
	transportType string,
	options *runtime.DeployWorkloadOptions,
	isolateNetwork bool,
) (*runtime.PermissionConfig, bool, bool, error) {
	var ignoreConfig *ignore.Config
	if options != nil {
		ignoreConfig = options.IgnoreConfig
	}

	permConfig, err := getPermissionConfigFromProfile(permissionProfile, transportType, ignoreConfig)
	if err != nil {
		return nil, false, false, fmt.Errorf("failed to get permission config: %w", err)
	}
	permConfig.ReadOnlyRootfs = options != nil && options.ReadOnlyRootfs

	attachStdio := options == nil || options.AttachStdio
	effectiveIsolation := isolateNetwork && networking.IsBridgeMode(permConfig.NetworkMode)
	if isolateNetwork && !effectiveIsolation {
		if strings.EqualFold(permConfig.NetworkMode, "none") {
			slog.Debug(networking.NetworkIsolationNoneRedundantMsg, "network_mode", permConfig.NetworkMode)
		} else {
			slog.Warn(networking.NetworkIsolationHostDroppedMsg, "network_mode", permConfig.NetworkMode)
		}
	}
	return permConfig, attachStdio, effectiveIsolation, nil
}
