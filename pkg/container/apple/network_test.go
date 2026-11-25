// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple_test

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/apple"
	"github.com/stacklok/toolhive/pkg/container/apple/mocks"
)

func TestDiscoverGatewayIPsAndResolveHostGateway(t *testing.T) {
	t.Parallel()

	t.Run("discovers sorted unique gateways and prefers default network", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		payload := `[
			{"id":"custom","status":{"ipv4Gateway":"192.168.65.1/24"}},
			{"id":"default","status":{"ipv4Gateway":"192.168.64.1"}},
			{"id":"dup","status":{"ipv4Gateway":"192.168.64.1/24"}},
			{"id":"broken","status":{"ipv4Gateway":"not-an-ip"}}
		]`
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "list", "--format", "json").
			Return([]byte(payload), nil).Times(2)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))

		gateways, err := apple.DiscoverGatewayIPs(context.Background(), client)
		require.NoError(t, err)
		assert.Equal(t, []netip.Addr{
			netip.MustParseAddr("192.168.64.1"),
			netip.MustParseAddr("192.168.65.1"),
		}, gateways)

		hostGW, err := client.HostGateway(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "192.168.64.1", hostGW)
	})

	t.Run("falls back to non-default network when default missing", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "list", "--format", "json").
			Return([]byte(`[{"id":"custom","status":{"ipv4Gateway":"10.0.0.1/24"}}]`), nil)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		hostGW, err := apple.ResolveHostGateway(context.Background(), client)
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.1", hostGW)
	})

	t.Run("returns error when no gateway is present", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "list", "--format", "json").
			Return([]byte(`[]`), nil)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		_, err := apple.ResolveHostGateway(context.Background(), client)
		require.Error(t, err)
	})
}

func TestInternalNetworkLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("createInternalNetwork skips creation if network already exists", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "inspect", "--", "toolhive-app-internal").
			Return([]byte(`[{"id":"toolhive-app-internal"}]`), nil)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		require.NoError(t, apple.CreateInternalNetwork(context.Background(), client, "toolhive-app-internal"))
	})

	t.Run("createInternalNetwork creates internal network with labels when absent", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "inspect", "--", "toolhive-app-internal").
			Return([]byte(`[]`), nil)
		runner.EXPECT().Run(
			gomock.Any(),
			apple.ContainerBinary,
			"network", "create", "--internal",
			"--label", "toolhive=true",
			"--label", "toolhive-name=toolhive-app-internal",
			"--", "toolhive-app-internal",
		).Return(nil, nil)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		require.NoError(t, apple.CreateInternalNetwork(context.Background(), client, "toolhive-app-internal"))
	})

	t.Run("deleteInternalNetwork ignores not found", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "delete", "--", "toolhive-app-internal").
			Return(nil, &apple.CommandError{Command: apple.ContainerBinary, Stderr: "Error: network not found: toolhive-app-internal", Err: errors.New("exit 1")})

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		require.NoError(t, apple.DeleteInternalNetwork(context.Background(), client, "toolhive-app-internal"))
	})

	t.Run("deleteInternalNetwork ignores missing network reported generically", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "delete", "--", "toolhive-app-internal").
				Return(nil, &apple.CommandError{
					Command: apple.ContainerBinary,
					Stderr:  `Error: failed to delete one or more networks: ["toolhive-app-internal"]`,
					Err:     errors.New("exit 1"),
				}),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "inspect", "--", "toolhive-app-internal").
				Return(nil, &apple.CommandError{
					Command: apple.ContainerBinary,
					Stderr:  "Error: network not found: toolhive-app-internal",
					Err:     errors.New("exit 1"),
				}),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		require.NoError(t, apple.DeleteInternalNetwork(context.Background(), client, "toolhive-app-internal"))
	})
}

func TestGetContainerNetworkIP(t *testing.T) {
	t.Parallel()

	t.Run("polls until IPv4 address appears", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "mcp-egress").
				Return([]byte(`[{"status":"booting","configuration":{"id":"mcp-egress"},"networks":[]}]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "mcp-egress").
				Return([]byte(`[{"status":"running","configuration":{"id":"mcp-egress"},"networks":[{"network":"toolhive-mcp-internal","ipv4Address":"192.168.70.4/24"}]}]`), nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		apple.SetPollConfigForTest(client, 5*time.Millisecond, 500*time.Millisecond)

		ipAddr, err := apple.GetContainerNetworkIP(context.Background(), client, "mcp-egress", "toolhive-mcp-internal")
		require.NoError(t, err)
		assert.Equal(t, "192.168.70.4", ipAddr)
	})

	t.Run("times out when IP never assigned", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "mcp-egress").
			Return([]byte(`[{"status":"running","configuration":{"id":"mcp-egress"},"networks":[]}]`), nil).
			AnyTimes()

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		apple.SetPollConfigForTest(client, 5*time.Millisecond, 25*time.Millisecond)

		_, err := apple.GetContainerNetworkIP(context.Background(), client, "mcp-egress", "toolhive-mcp-internal")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
	})

	t.Run("fails fast when container enters stopped or error state", func(t *testing.T) {
		t.Parallel()

		for _, rawStatus := range []string{"stopped", "error"} {
			t.Run(rawStatus, func(t *testing.T) {
				t.Parallel()
				ctrl := gomock.NewController(t)
				runner := mocks.NewMockCommandRunner(ctrl)
				runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "mcp-egress").
					Return([]byte(`[{"status":"`+rawStatus+`","configuration":{"id":"mcp-egress"},"networks":[]}]`), nil).
					Times(1)

				client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
				apple.SetPollConfigForTest(client, 5*time.Millisecond, 5*time.Second)

				startTime := time.Now()
				_, err := apple.GetContainerNetworkIP(context.Background(), client, "mcp-egress", "toolhive-mcp-internal")
				require.Error(t, err)
				assert.Contains(t, err.Error(), rawStatus)
				assert.Less(t, time.Since(startTime), time.Second)
			})
		}
	})
}

func TestProxyConfigDirLifecycleAndSafety(t *testing.T) {
	t.Parallel()

	dir, err := apple.WriteProxyConfigDir(apple.SquidDirPrefix, apple.SquidConfigFileName, []byte("http_port 3128\n"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	assert.True(t, apple.IsSafeProxyConfigDir(dir))
	content, err := os.ReadFile(filepath.Join(dir, apple.SquidConfigFileName))
	require.NoError(t, err)
	assert.Equal(t, "http_port 3128\n", string(content))

	apple.CleanupProxyConfigDir(dir)
	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr))

	// Unsafe paths must never be accepted for deletion.
	unsafePaths := []string{
		"",
		"relative/toolhive-apple-squid-123",
		"/etc/toolhive-apple-squid-123",
		filepath.Join(os.TempDir(), "unrelated-dir"),
		filepath.Join(os.TempDir(), "toolhive-apple-squid-123", "..", "other"),
	}
	for _, candidate := range unsafePaths {
		assert.False(t, apple.IsSafeProxyConfigDir(candidate), "path %q must not be considered safe", candidate)
	}
}

func TestInternalNetworkName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "toolhive-my-app-internal", apple.InternalNetworkName("my-app"))

	longA := "thv-apple-int-2535-1795527141790529000-isolated-a"
	longB := "thv-apple-int-2535-1795527141790529000-isolated-b"
	nameA := apple.InternalNetworkName(longA)
	nameB := apple.InternalNetworkName(longB)

	assert.LessOrEqual(t, len(nameA), 63)
	assert.LessOrEqual(t, len(nameB), 63)
	assert.Equal(t, nameA, apple.InternalNetworkName(longA))
	assert.NotEqual(t, nameA, nameB)
	assert.True(t, strings.HasPrefix(nameA, "toolhive-"))
	assert.True(t, strings.HasSuffix(nameA, "-internal"))
}
