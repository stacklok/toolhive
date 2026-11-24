// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/container/internal/netproxy"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	proxyConfigDirLabel = "toolhive-config-dir"
	proxyCPUs           = "1"
	proxyMemory         = "256M"
	defaultNetworkName  = "default"
	squidConfigMountDir = "/etc/toolhive-squid"
	squidConfigFileName = "squid.conf"
	envoyConfigMountDir = "/etc/toolhive-envoy"
	envoyConfigFileName = "envoy.json"
	squidDirPrefix      = "toolhive-apple-squid-"
	envoyDirPrefix      = "toolhive-apple-envoy-"
)

const maxAppleNetworkNameLen = 63

func internalNetworkName(workloadName string) string {
	candidate := fmt.Sprintf("toolhive-%s-internal", workloadName)
	if len(candidate) <= maxAppleNetworkNameLen {
		return candidate
	}

	sum := sha256.Sum256([]byte(workloadName))
	suffix := hex.EncodeToString(sum[:4])
	const (
		prefixTag = "toolhive-"
		suffixTag = "-internal"
	)
	maxBaseLen := maxAppleNetworkNameLen - len(prefixTag) - 1 - len(suffix) - len(suffixTag)
	base := workloadName
	if len(base) > maxBaseLen {
		base = base[:maxBaseLen]
	}
	base = strings.TrimRight(base, "-._")
	if base == "" {
		return fmt.Sprintf("%s%s%s", prefixTag, suffix, suffixTag)
	}
	return fmt.Sprintf("%s%s-%s%s", prefixTag, base, suffix, suffixTag)
}

func egressContainerName(workloadName string) string {
	return fmt.Sprintf("%s-egress", workloadName)
}

func ingressContainerName(workloadName string) string {
	return fmt.Sprintf("%s-ingress", workloadName)
}

func (client *Client) listNetworks(ctx context.Context) ([]appleNetworkJSON, error) {
	out, err := client.runner.Run(ctx, containerBinary, "network", "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("failed to list Apple container networks: %w", err)
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var networks []appleNetworkJSON
	if err := json.Unmarshal(trimmed, &networks); err != nil {
		return nil, fmt.Errorf("failed to parse Apple container network list: %w", err)
	}
	return networks, nil
}

func (client *Client) discoverGatewayIPs(ctx context.Context, requiredNetworks ...string) ([]netip.Addr, error) {
	networks, err := client.listNetworks(ctx)
	if err != nil {
		return nil, err
	}

	gatewayIPs := make([]netip.Addr, 0, len(networks))
	seenNetworks := make(map[string]struct{}, len(networks))
	for _, netInfo := range networks {
		raw := stripCIDR(netInfo.Status.IPv4Gateway)
		if raw == "" {
			continue
		}
		addr, parseErr := netip.ParseAddr(raw)
		if parseErr != nil {
			slog.Debug("ignoring invalid Apple network gateway address",
				"network", netInfo.ID, "gateway", netInfo.Status.IPv4Gateway, "error", parseErr)
			continue
		}
		gatewayIPs = append(gatewayIPs, addr)
		seenNetworks[netInfo.ID] = struct{}{}
	}

	if len(gatewayIPs) == 0 {
		return nil, fmt.Errorf("could not discover any Apple container network gateway IPs")
	}

	for _, reqNet := range requiredNetworks {
		if reqNet == "" {
			continue
		}
		if _, ok := seenNetworks[reqNet]; !ok {
			return nil, fmt.Errorf("could not discover gateway IP for required Apple container network %q", reqNet)
		}
	}

	slices.SortFunc(gatewayIPs, netip.Addr.Compare)
	return slices.Compact(gatewayIPs), nil
}

func (client *Client) resolveHostGateway(ctx context.Context) (string, error) {
	networks, err := client.listNetworks(ctx)
	if err != nil {
		return "", err
	}

	fallback := ""
	for _, netInfo := range networks {
		raw := stripCIDR(netInfo.Status.IPv4Gateway)
		if raw == "" {
			continue
		}
		addr, parseErr := netip.ParseAddr(raw)
		if parseErr != nil {
			continue
		}
		if netInfo.ID == defaultNetworkName {
			return addr.String(), nil
		}
		if fallback == "" {
			fallback = addr.String()
		}
	}

	if fallback != "" {
		return fallback, nil
	}

	return "", fmt.Errorf("could not determine host gateway IP from Apple container networks")
}

// createInternalNetwork ensures an isolated per-workload Apple vmnet network
// exists. Note that apple/container's `--internal` flag creates a macOS vmnet
// `hostOnly` network: direct internet egress, the default vmnet gateway, and
// host loopback (127.0.0.1) listeners are unreachable from containers on this
// network, and Squid/Envoy deny proxied traffic to all discovered gateway IPs
// unless AllowDockerGateway is set. However, macOS host services bound to
// wildcard addresses (0.0.0.0) remain directly reachable on this internal
// network's own vmnet gateway IP without traversing the proxy.
func (client *Client) createInternalNetwork(ctx context.Context, networkName string) error {
	out, err := client.runner.Run(ctx, containerBinary, "network", "inspect", "--", networkName)
	if err == nil {
		trimmed := bytes.TrimSpace(out)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("[]")) {
			return nil
		}
	} else if !isNotFoundError(err) {
		return fmt.Errorf("failed to inspect Apple container network %q: %w", networkName, err)
	}

	networkLabels := map[string]string{}
	lb.AddNetworkLabels(networkLabels, networkName)

	args := []string{"network", "create", "--internal"}
	args = appendSortedLabels(args, networkLabels)
	args = append(args, "--", networkName)

	if _, err := client.runner.Run(ctx, containerBinary, args...); err != nil {
		return fmt.Errorf("failed to create internal Apple container network %q: %w", networkName, err)
	}
	return nil
}

func (client *Client) deleteInternalNetwork(ctx context.Context, networkName string) error {
	if _, err := client.runner.Run(ctx, containerBinary, "network", "delete", "--", networkName); err != nil {
		if isNotFoundError(err) {
			return nil
		}
		// apple/container 1.7+ reports missing networks on delete as a generic
		// "failed to delete one or more networks" error; verify via inspect.
		if strings.Contains(strings.ToLower(err.Error()), "failed to delete one or more networks") {
			out, inspectErr := client.runner.Run(ctx, containerBinary, "network", "inspect", "--", networkName)
			if isNotFoundError(inspectErr) {
				return nil
			}
			if inspectErr == nil {
				trimmed := bytes.TrimSpace(out)
				if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[]")) {
					return nil
				}
			}
		}
		return fmt.Errorf("failed to delete internal Apple container network %q: %w", networkName, err)
	}
	return nil
}

func (client *Client) getContainerNetworkIP(
	ctx context.Context,
	containerName, networkName string,
) (string, error) {
	pollTimeout := client.pollTimeout
	if pollTimeout <= 0 {
		pollTimeout = defaultPollTimeout
	}
	pollInterval := client.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}

	deadline := time.Now().Add(pollTimeout)
	for {
		cont, err := client.inspectContainer(ctx, containerName)
		if err == nil {
			if ipAddr := extractNetworkIP(cont.Networks, networkName); ipAddr != "" {
				return ipAddr, nil
			}
			status := appleToDomainStatus(cont.Status)
			if status == runtime.WorkloadStatusStopped || status == runtime.WorkloadStatusError {
				return "", fmt.Errorf(
					"container %q entered %s state (%s) before receiving an IPv4 address on network %q",
					containerName, status, cont.Status, networkName,
				)
			}
		} else if !errors.Is(err, runtime.ErrContainerNotFound) {
			return "", fmt.Errorf("failed to inspect container %q while resolving network IP: %w", containerName, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", fmt.Errorf(
				"timed out waiting for container %q to receive an IPv4 address on network %q",
				containerName, networkName,
			)
		}

		waitDur := min(pollInterval, remaining)
		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func extractNetworkIP(networks []appleContainerNetworkJSON, networkName string) string {
	for _, netInfo := range networks {
		if netInfo.Network != networkName {
			continue
		}
		raw := stripCIDR(netInfo.IPv4Address)
		if raw == "" {
			continue
		}
		if addr, err := netip.ParseAddr(raw); err == nil {
			return addr.String()
		}
	}
	return ""
}

func (client *Client) ensureProxyImage(ctx context.Context, imageName string) error {
	if err := client.imageManager.PullImage(ctx, imageName); err != nil {
		exists, inspectErr := client.imageManager.ImageExists(ctx, imageName)
		if inspectErr != nil || !exists {
			return fmt.Errorf("failed to pull proxy image %q: %w", imageName, err)
		}
		slog.Debug("proxy image exists locally, continuing despite pull failure", "image", imageName)
	}
	return nil
}

func (client *Client) setupEgressProxy(
	ctx context.Context,
	backend string,
	spec netproxy.Spec,
	internalNet string,
) (string, string, error) {
	proxyName := egressContainerName(spec.WorkloadName)

	var (
		imageName string
		configDir string
		mountDir  string
		proxyCmd  []string
		err       error
	)

	if backend == netproxy.BackendEnvoy {
		bootstrap := netproxy.BuildEgressBootstrap(spec)
		data, marshalErr := json.MarshalIndent(bootstrap, "", "  ")
		if marshalErr != nil {
			return "", "", fmt.Errorf("failed to marshal Envoy egress bootstrap: %w", marshalErr)
		}
		configDir, err = writeProxyConfigDir(envoyDirPrefix, envoyConfigFileName, data)
		if err != nil {
			return "", "", err
		}
		imageName = netproxy.GetEnvoyImage()
		mountDir = envoyConfigMountDir
		proxyCmd = []string{"-c", path.Join(envoyConfigMountDir, envoyConfigFileName)}
	} else {
		conf := netproxy.BuildEgressSquidConf(
			spec.Permissions,
			spec.WorkloadName,
			spec.AllowDockerGateway,
			spec.GatewayIPs,
		)
		configDir, err = writeProxyConfigDir(squidDirPrefix, squidConfigFileName, []byte(conf))
		if err != nil {
			return "", "", err
		}
		imageName = netproxy.GetSquidImage()
		mountDir = squidConfigMountDir
		proxyCmd = []string{"-f", path.Join(squidConfigMountDir, squidConfigFileName)}
	}

	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			cleanupProxyConfigDir(configDir)
		}
	}()

	if err := client.ensureProxyImage(ctx, imageName); err != nil {
		return "", "", err
	}

	client.removeContainerAndConfigDir(ctx, proxyName)

	args := buildProxyRunArgs(proxyName, backend, imageName, internalNet, true, configDir, mountDir, 0, proxyCmd)
	if _, err := client.runner.Run(ctx, containerBinary, args...); err != nil {
		return "", "", fmt.Errorf("failed to start egress proxy container %q: %w", proxyName, err)
	}

	egressIP, err := client.getContainerNetworkIP(ctx, proxyName, internalNet)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_, _ = client.runner.Run(cleanupCtx, containerBinary, "delete", "--force", "--", proxyName)
		return "", "", fmt.Errorf("failed to resolve egress proxy IP for %q: %w", proxyName, err)
	}

	cleanupOnErr = false
	return egressIP, configDir, nil
}

func (client *Client) setupIngressProxy(
	ctx context.Context,
	backend string,
	spec netproxy.Spec,
	internalNet string,
) (int, string, error) {
	hostPort, err := networking.FindOrUsePort(0)
	if err != nil {
		return 0, "", fmt.Errorf("failed to allocate ingress host port: %w", err)
	}

	proxyName := ingressContainerName(spec.WorkloadName)

	var (
		imageName string
		configDir string
		mountDir  string
		proxyCmd  []string
	)

	if backend == netproxy.BackendEnvoy {
		bootstrap := netproxy.BuildIngressBootstrap(spec, hostPort)
		data, marshalErr := json.MarshalIndent(bootstrap, "", "  ")
		if marshalErr != nil {
			return 0, "", fmt.Errorf("failed to marshal Envoy ingress bootstrap: %w", marshalErr)
		}
		configDir, err = writeProxyConfigDir(envoyDirPrefix, envoyConfigFileName, data)
		if err != nil {
			return 0, "", err
		}
		imageName = netproxy.GetEnvoyImage()
		mountDir = envoyConfigMountDir
		proxyCmd = []string{"-c", path.Join(envoyConfigMountDir, envoyConfigFileName)}
	} else {
		conf := netproxy.BuildIngressSquidConf(
			spec.WorkloadName,
			spec.UpstreamHost,
			spec.UpstreamPort,
			hostPort,
			spec.Permissions,
		)
		configDir, err = writeProxyConfigDir(squidDirPrefix, squidConfigFileName, []byte(conf))
		if err != nil {
			return 0, "", err
		}
		imageName = netproxy.GetSquidImage()
		mountDir = squidConfigMountDir
		proxyCmd = []string{"-f", path.Join(squidConfigMountDir, squidConfigFileName)}
	}

	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			cleanupProxyConfigDir(configDir)
		}
	}()

	if err := client.ensureProxyImage(ctx, imageName); err != nil {
		return 0, "", err
	}

	client.removeContainerAndConfigDir(ctx, proxyName)

	args := buildProxyRunArgs(proxyName, backend, imageName, internalNet, false, configDir, mountDir, hostPort, proxyCmd)
	if _, err := client.runner.Run(ctx, containerBinary, args...); err != nil {
		return 0, "", fmt.Errorf("failed to start ingress proxy container %q: %w", proxyName, err)
	}

	cleanupOnErr = false
	return hostPort, configDir, nil
}

func buildProxyRunArgs(
	proxyName string,
	backend string,
	imageName string,
	internalNet string,
	attachDefaultNetwork bool,
	configDir string,
	mountDir string,
	publishPort int,
	proxyCmd []string,
) []string {
	proxyLabels := map[string]string{}
	lb.AddStandardLabels(proxyLabels, proxyName, proxyName, "stdio", 80)
	proxyLabels[netproxy.AuxiliaryWorkloadLabel] = netproxy.LabelValueTrue
	proxyLabels[proxyConfigDirLabel] = configDir

	args := []string{
		"run", "-d",
		"--name", proxyName,
		"--cpus", proxyCPUs,
		"--memory", proxyMemory,
	}
	if attachDefaultNetwork {
		args = append(args, "--network", defaultNetworkName)
	}
	args = append(args,
		"--network", internalNet,
		"--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", configDir, mountDir),
	)

	if backend == netproxy.BackendEnvoy {
		args = append(args, "--cap-drop", "ALL")
	} else {
		args = append(args, "--cap-add", "CAP_SETUID", "--cap-add", "CAP_SETGID")
	}

	if publishPort > 0 {
		portStr := strconv.Itoa(publishPort)
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%s:%s/tcp", portStr, portStr))
	}

	args = appendSortedLabels(args, proxyLabels)
	args = append(args, "--", imageName)
	if len(proxyCmd) > 0 {
		args = append(args, proxyCmd...)
	}
	return args
}

func (client *Client) stopProxyContainers(ctx context.Context, workloadName string) {
	proxyNames := []string{
		egressContainerName(workloadName),
		ingressContainerName(workloadName),
	}
	for _, proxyName := range proxyNames {
		if _, err := client.runner.Run(
			ctx, containerBinary, "stop", "--time", strconv.Itoa(stopTimeoutSeconds), "--", proxyName,
		); err != nil && !isNotFoundError(err) {
			slog.Debug("failed to stop proxy container", "name", proxyName, "error", err)
		}
	}
}

func (client *Client) removeProxyContainers(ctx context.Context, workloadName string) {
	proxyNames := []string{
		egressContainerName(workloadName),
		ingressContainerName(workloadName),
	}
	for _, proxyName := range proxyNames {
		client.removeContainerAndConfigDir(ctx, proxyName)
	}
}

func (client *Client) removeContainerAndConfigDir(ctx context.Context, containerName string) {
	configDir := ""
	if cont, err := client.inspectContainer(ctx, containerName); err == nil {
		configDir = cont.Configuration.Labels[proxyConfigDirLabel]
	}

	_, delErr := client.runner.Run(ctx, containerBinary, "delete", "--force", "--", containerName)
	if delErr != nil && !isNotFoundError(delErr) {
		slog.Debug("failed to delete container", "name", containerName, "error", delErr)
	}

	if configDir != "" {
		cleanupProxyConfigDir(configDir)
	}
}

func writeProxyConfigDir(prefix, fileName string, content []byte) (string, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary proxy config directory: %w", err)
	}
	if strings.Contains(dir, ",") {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("temporary proxy config directory %q contains a comma, which is incompatible with --mount", dir)
	}

	// Ensure non-root proxy users inside the Linux VM can traverse the VirtioFS directory.
	//nolint:gosec // G302: directory must be traversable by non-root container UIDs (squid/envoy)
	if err := os.Chmod(dir, 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("failed to set proxy config directory permissions: %w", err)
	}

	filePath := filepath.Join(dir, fileName)
	//nolint:gosec // G306: config must be readable by non-root container UIDs (squid/envoy)
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("failed to write proxy config file %q: %w", fileName, err)
	}

	return dir, nil
}

func cleanupProxyConfigDir(dir string) {
	if !isSafeProxyConfigDir(dir) {
		slog.Debug("skipping cleanup of non-managed proxy config directory", "dir", dir)
		return
	}
	if err := os.RemoveAll(filepath.Clean(dir)); err != nil {
		slog.Debug("failed to remove proxy config directory", "dir", dir, "error", err)
	}
}

func isSafeProxyConfigDir(dir string) bool {
	if strings.TrimSpace(dir) == "" || !filepath.IsAbs(dir) {
		return false
	}

	cleaned := filepath.Clean(dir)
	base := filepath.Base(cleaned)
	if !strings.HasPrefix(base, squidDirPrefix) && !strings.HasPrefix(base, envoyDirPrefix) {
		return false
	}

	parent := filepath.Dir(cleaned)
	rawTempDir := filepath.Clean(os.TempDir())
	if parent == rawTempDir {
		return true
	}

	resolvedTempDir, tempErr := filepath.EvalSymlinks(rawTempDir)
	if tempErr != nil {
		return false
	}
	if parent == filepath.Clean(resolvedTempDir) {
		return true
	}

	resolvedParent, parentErr := filepath.EvalSymlinks(parent)
	if parentErr != nil {
		return false
	}
	return filepath.Clean(resolvedParent) == filepath.Clean(resolvedTempDir)
}
