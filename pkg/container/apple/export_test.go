// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"context"
	"net/netip"
	"time"

	"github.com/stacklok/toolhive/pkg/container/images"
)

// Constants exposed for external package tests in apple_test.
const (
	ContainerBinary     = containerBinary
	SwVersBinary        = swVersBinary
	ProxyConfigDirLabel = proxyConfigDirLabel
	SquidDirPrefix      = squidDirPrefix
	SquidConfigFileName = squidConfigFileName
)

// NewClientWithDependencies constructs a Client with injected test dependencies.
func NewClientWithDependencies(runner CommandRunner, imageManager images.ImageManager) *Client {
	return newClientWithDependencies(runner, imageManager)
}

// SetPollConfigForTest overrides the client's polling interval and timeout for unit tests.
func SetPollConfigForTest(client *Client, interval, timeout time.Duration) {
	client.pollInterval = interval
	client.pollTimeout = timeout
}

// NewImageManagerWithRunner constructs an ImageManager backed by a mock CommandRunner.
func NewImageManagerWithRunner(runner CommandRunner) *ImageManager {
	return newImageManagerWithRunner(runner)
}

// ParseVersion exposes parseVersion for external package tests.
func ParseVersion(output string) (major, minor, patch int, ok bool) {
	return parseVersion(output)
}

// CheckAvailability exposes checkAvailability for external package tests.
func CheckAvailability(ctx context.Context, runner CommandRunner, goos, goarch string) error {
	return checkAvailability(ctx, runner, goos, goarch)
}

// CheckMacOSVersionForIsolation exposes checkMacOSVersionForIsolation for external package tests.
func CheckMacOSVersionForIsolation(ctx context.Context, runner CommandRunner) error {
	return checkMacOSVersionForIsolation(ctx, runner)
}

// DiscoverGatewayIPs exposes Client.discoverGatewayIPs for external package tests.
func DiscoverGatewayIPs(ctx context.Context, client *Client) ([]netip.Addr, error) {
	return client.discoverGatewayIPs(ctx)
}

// ResolveHostGateway exposes Client.resolveHostGateway for external package tests.
func ResolveHostGateway(ctx context.Context, client *Client) (string, error) {
	return client.resolveHostGateway(ctx)
}

// CreateInternalNetwork exposes Client.createInternalNetwork for external package tests.
func CreateInternalNetwork(ctx context.Context, client *Client, networkName string) error {
	return client.createInternalNetwork(ctx, networkName)
}

// DeleteInternalNetwork exposes Client.deleteInternalNetwork for external package tests.
func DeleteInternalNetwork(ctx context.Context, client *Client, networkName string) error {
	return client.deleteInternalNetwork(ctx, networkName)
}

// GetContainerNetworkIP exposes Client.getContainerNetworkIP for external package tests.
func GetContainerNetworkIP(ctx context.Context, client *Client, containerName, networkName string) (string, error) {
	return client.getContainerNetworkIP(ctx, containerName, networkName)
}

// WriteProxyConfigDir exposes writeProxyConfigDir for external package tests.
func WriteProxyConfigDir(prefix, fileName string, content []byte) (string, error) {
	return writeProxyConfigDir(prefix, fileName, content)
}

// CleanupProxyConfigDir exposes cleanupProxyConfigDir for external package tests.
func CleanupProxyConfigDir(dir string) {
	cleanupProxyConfigDir(dir)
}

// IsSafeProxyConfigDir exposes isSafeProxyConfigDir for external package tests.
func IsSafeProxyConfigDir(dir string) bool {
	return isSafeProxyConfigDir(dir)
}

// InternalNetworkName exposes internalNetworkName for external package tests.
func InternalNetworkName(workloadName string) string {
	return internalNetworkName(workloadName)
}
