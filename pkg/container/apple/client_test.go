// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/apple"
	"github.com/stacklok/toolhive/pkg/container/apple/mocks"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

type stubWriteCloser struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
}

func (wc *stubWriteCloser) Write(data []byte) (int, error) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return wc.buf.Write(data)
}

func (wc *stubWriteCloser) Close() error {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.closed = true
	return nil
}

func TestClient_RuntimeTypeAndIsRunning(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "system", "status").Return([]byte("running"), nil)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "system", "status").Return(nil, errors.New("stopped"))

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	assert.Equal(t, runtime.TypeApple, client.RuntimeType())
	require.NoError(t, client.IsRunning(context.Background()))
	require.Error(t, client.IsRunning(context.Background()))
}

func extractFlagValue(args []string, flag string) string {
	for idx, arg := range args {
		if arg == flag && idx+1 < len(args) {
			return args[idx+1]
		}
	}
	return ""
}

func TestClient_DeployWorkload_StdioAndImmutability(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	var (
		capturedArgs    []string
		capturedEnvPath string
		capturedEnvData string
		capturedEnvMode os.FileMode
	)
	gomock.InOrder(
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "mcp-stdio").
			Return([]byte(`[]`), nil),
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, args ...string) ([]byte, error) {
				capturedArgs = slices.Clone(args)
				capturedEnvPath = extractFlagValue(args, "--env-file")
				if capturedEnvPath != "" {
					info, statErr := os.Stat(capturedEnvPath)
					if statErr == nil {
						capturedEnvMode = info.Mode().Perm()
					}
					data, readErr := os.ReadFile(capturedEnvPath)
					if readErr == nil {
						capturedEnvData = string(data)
					}
				}
				return []byte("mcp-stdio\n"), nil
			}),
	)

	secretValue := "ghp_secretTokenValue"
	callerEnv := map[string]string{"FOO": "bar", "GITHUB_TOKEN": secretValue}
	callerLabels := map[string]string{"toolhive": "true"}

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	port, err := client.DeployWorkload(
		context.Background(),
		"ghcr.io/example/mcp:latest",
		"mcp-stdio",
		[]string{"--verbose"},
		callerEnv,
		callerLabels,
		&permissions.Profile{},
		"stdio",
		&runtime.DeployWorkloadOptions{AttachStdio: true, ReadOnlyRootfs: true},
		false,
	)

	require.NoError(t, err)
	assert.Equal(t, 0, port)

	// Caller maps must remain unmodified.
	assert.Equal(t, map[string]string{"FOO": "bar", "GITHUB_TOKEN": secretValue}, callerEnv)
	assert.Equal(t, map[string]string{"toolhive": "true"}, callerLabels)

	// Two-phase stdio uses `create -i`, drops caps, enables read-only rootfs, passes env via 0600 --env-file, and places `--` before image.
	assert.Equal(t, []string{"create", "-i", "--name", "mcp-stdio"}, capturedArgs[:4])
	assert.Contains(t, capturedArgs, "--cap-drop")
	assert.Contains(t, capturedArgs, "--read-only")
	assert.NotContains(t, capturedArgs, "-e")
	assert.NotContains(t, strings.Join(capturedArgs, " "), secretValue)
	require.NotEmpty(t, capturedEnvPath)
	assert.Equal(t, os.FileMode(0o600), capturedEnvMode)
	assert.Equal(t, "FOO=bar\nGITHUB_TOKEN="+secretValue+"\n", capturedEnvData)
	_, statErr := os.Stat(capturedEnvPath)
	assert.True(t, os.IsNotExist(statErr), "temporary env file must be deleted right after container create")
	assert.Equal(t, []string{"--", "ghcr.io/example/mcp:latest", "--verbose"}, capturedArgs[len(capturedArgs)-3:])
}

func TestClient_DeployWorkload_EnvFileSecurityAndValidation(t *testing.T) {
	t.Parallel()

	t.Run("deletes env file on failure and omits secrets from error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		secretValue := "sk-super-secret-value-9876"
		var capturedEnvPath string
		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "mcp-fail").
				Return([]byte(`[]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
				DoAndReturn(func(_ context.Context, _ string, args ...string) ([]byte, error) {
					capturedEnvPath = extractFlagValue(args, "--env-file")
					return nil, &apple.CommandError{
						Command: apple.ContainerBinary,
						Args:    args,
						Stderr:  "Error: image pull failed",
						Err:     errors.New("exit status 1"),
					}
				}),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		_, err := client.DeployWorkload(
			context.Background(),
			"ghcr.io/example/mcp:latest",
			"mcp-fail",
			nil,
			map[string]string{"API_KEY": secretValue},
			nil,
			&permissions.Profile{},
			"stdio",
			&runtime.DeployWorkloadOptions{AttachStdio: true},
			false,
		)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secretValue)
		require.NotEmpty(t, capturedEnvPath)
		_, statErr := os.Stat(capturedEnvPath)
		assert.True(t, os.IsNotExist(statErr), "temporary env file must be deleted when deploy fails")
	})

	t.Run("rejects multiline value without leaking secret", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		secretValue := "line1\nsecret-line2"
		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		_, err := client.DeployWorkload(
			context.Background(),
			"ghcr.io/example/mcp:latest",
			"mcp-ml",
			nil,
			map[string]string{"PRIVATE_KEY": secretValue},
			nil,
			&permissions.Profile{},
			"stdio",
			&runtime.DeployWorkloadOptions{AttachStdio: true},
			false,
		)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "PRIVATE_KEY")
		assert.NotContains(t, err.Error(), "secret-line2")
	})

	t.Run("rejects invalid key names", func(t *testing.T) {
		t.Parallel()
		for _, badKey := range []string{"", "#COMMENT", "BAD=KEY", " HAS_SPACE"} {
			ctrl := gomock.NewController(t)
			runner := mocks.NewMockCommandRunner(ctrl)
			client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
			_, err := client.DeployWorkload(
				context.Background(),
				"ghcr.io/example/mcp:latest",
				"mcp-badkey",
				nil,
				map[string]string{badKey: "secret-val"},
				nil,
				&permissions.Profile{},
				"stdio",
				&runtime.DeployWorkloadOptions{AttachStdio: true},
				false,
			)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-val")
		}
	})
}

func TestClient_DeployWorkload_StreamableHTTP(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	var capturedArgs []string
	gomock.InOrder(
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "mcp-http").
			Return([]byte(`[]`), nil),
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, args ...string) ([]byte, error) {
				capturedArgs = slices.Clone(args)
				return []byte("mcp-http\n"), nil
			}),
	)

	callerBindings := map[string][]runtime.PortBinding{
		"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "0"}},
	}

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	port, err := client.DeployWorkload(
		context.Background(),
		"ghcr.io/example/http:latest",
		"mcp-http",
		nil,
		nil,
		map[string]string{"toolhive": "true"},
		&permissions.Profile{},
		"streamable-http",
		&runtime.DeployWorkloadOptions{
			AttachStdio:  false,
			ExposedPorts: map[string]struct{}{"8080/tcp": {}},
			PortBindings: callerBindings,
		},
		false,
	)

	require.NoError(t, err)
	assert.Greater(t, port, 0)
	// Caller's PortBinding slice must not be mutated.
	assert.Equal(t, "0", callerBindings["8080/tcp"][0].HostPort)
	assert.Equal(t, []string{"run", "-d", "--name", "mcp-http"}, capturedArgs[:4])
	assert.Contains(t, capturedArgs, "-p")
}

func TestClient_DeployWorkload_IsolatedNetwork_Squid(t *testing.T) {
	t.Setenv("TOOLHIVE_NETWORK_PROXY", "squid")

	var warnBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&warnBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	var (
		egressArgs  []string
		mcpArgs     []string
		mcpEnvPath  string
		mcpEnvData  string
		ingressArgs []string
		createdDirs []string
		egressRun   bool
		ingressRun  bool
	)

	extractConfigDir := func(args []string) string {
		for idx, arg := range args {
			if arg == "--label" && idx+1 < len(args) && strings.HasPrefix(args[idx+1], apple.ProxyConfigDirLabel+"=") {
				return strings.TrimPrefix(args[idx+1], apple.ProxyConfigDirLabel+"=")
			}
		}
		return ""
	}

	t.Cleanup(func() {
		for _, dir := range createdDirs {
			_ = os.RemoveAll(dir)
		}
	})

	runner.EXPECT().Run(gomock.Any(), apple.SwVersBinary, "-productVersion").Return([]byte("26.1\n"), nil)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "inspect", "--", "toolhive-iso-app-internal").Return([]byte(`[]`), nil)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "create", "--internal", "--label", "toolhive=true", "--label", "toolhive-name=toolhive-iso-app-internal", "--", "toolhive-iso-app-internal").Return(nil, nil)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "list", "--format", "json").
		Return([]byte(`[{"id":"default","status":{"ipv4Gateway":"192.168.64.1/24"}},{"id":"toolhive-iso-app-internal","status":{"ipv4Gateway":"192.168.70.1/24"}}]`), nil)

	// Egress proxy image pull + cleanup check + run + IP inspect
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "image", "pull", "--", gomock.Any()).Return(nil, nil).Times(2)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "iso-app-egress").Return([]byte(`[]`), nil)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "delete", "--force", "--", "iso-app-egress").Return(nil, &apple.CommandError{Stderr: "Error: container not found: iso-app-egress"})
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch {
			case !egressRun && len(args) > 3 && args[0] == "run" && args[3] == "iso-app-egress":
				egressRun = true
				egressArgs = slices.Clone(args)
				if dir := extractConfigDir(args); dir != "" {
					createdDirs = append(createdDirs, dir)
				}
				return nil, nil
			case len(args) == 3 && args[0] == "inspect" && args[1] == "--" && args[2] == "iso-app-egress":
				return []byte(`[{"status":"running","configuration":{"id":"iso-app-egress"},"networks":[{"network":"toolhive-iso-app-internal","ipv4Address":"192.168.70.2/24"}]}]`), nil
			case len(args) == 3 && args[0] == "inspect" && args[1] == "--" && args[2] == "iso-app":
				if len(mcpArgs) == 0 {
					return []byte(`[]`), nil
				}
				return []byte(`[{"status":"running","configuration":{"id":"iso-app"},"networks":[{"network":"toolhive-iso-app-internal","ipv4Address":"192.168.70.3/24"}]}]`), nil
			case len(args) > 3 && args[0] == "run" && args[3] == "iso-app":
				mcpArgs = slices.Clone(args)
				mcpEnvPath = extractFlagValue(args, "--env-file")
				if mcpEnvPath != "" {
					if data, readErr := os.ReadFile(mcpEnvPath); readErr == nil {
						mcpEnvData = string(data)
					}
				}
				return nil, nil
			case len(args) == 3 && args[0] == "inspect" && args[1] == "--" && args[2] == "iso-app-ingress":
				return []byte(`[]`), nil
			case len(args) == 4 && args[0] == "delete" && args[1] == "--force" && args[2] == "--" && args[3] == "iso-app-ingress":
				return nil, &apple.CommandError{Stderr: "Error: container not found: iso-app-ingress"}
			case !ingressRun && len(args) > 3 && args[0] == "run" && args[3] == "iso-app-ingress":
				ingressRun = true
				ingressArgs = slices.Clone(args)
				if dir := extractConfigDir(args); dir != "" {
					createdDirs = append(createdDirs, dir)
				}
				return nil, nil
			default:
				return nil, errors.New("unexpected command: " + strings.Join(args, " "))
			}
		}).AnyTimes()

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	apple.SetPollConfigForTest(client, 5*time.Millisecond, 500*time.Millisecond)

	hostPort, err := client.DeployWorkload(
		context.Background(),
		"ghcr.io/example/mcp:latest",
		"iso-app",
		nil,
		nil,
		map[string]string{"toolhive": "true"},
		&permissions.Profile{
			Network: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"api.example.com"},
				},
			},
		},
		"streamable-http",
		&runtime.DeployWorkloadOptions{
			AttachStdio:  false,
			ExposedPorts: map[string]struct{}{"8080/tcp": {}},
		},
		true,
	)

	require.NoError(t, err)
	assert.Greater(t, hostPort, 0)
	assert.Contains(t, egressArgs, "CAP_SETUID")
	assert.Contains(t, egressArgs, "CAP_SETGID")
	assert.Contains(t, egressArgs, "/etc/toolhive-squid/squid.conf")
	assert.NotContains(t, mcpArgs, "-e")
	require.NotEmpty(t, mcpEnvPath)
	assert.Contains(t, mcpEnvData, "HTTP_PROXY=http://192.168.70.2:3128")
	_, envStatErr := os.Stat(mcpEnvPath)
	assert.True(t, os.IsNotExist(envStatErr), "isolated MCP temp env file must be deleted right after container run")
	assert.Contains(t, egressArgs, "default")
	assert.Contains(t, egressArgs, "toolhive-iso-app-internal")
	assert.Contains(t, mcpArgs, "toolhive-iso-app-internal")
	assert.Contains(t, ingressArgs, "toolhive-iso-app-internal")
	assert.NotContains(t, ingressArgs, "default", "ingress proxy must attach only to the internal network")
	assert.Contains(t, ingressArgs, "-p")
	assert.Contains(t, warnBuf.String(), "vmnet hostOnly")
	assert.Contains(t, warnBuf.String(), "0.0.0.0")
}

func TestClient_DeployWorkload_IsolatedNetwork_EnvoyAndRollback(t *testing.T) {
	t.Setenv("TOOLHIVE_NETWORK_PROXY", "envoy")

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	var (
		egressConfigDir string
		egressArgs      []string
		deletedNames    []string
		deletedNetwork  string
	)

	runner.EXPECT().Run(gomock.Any(), apple.SwVersBinary, "-productVersion").Return([]byte("26.0\n"), nil)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case joined == "network inspect -- toolhive-rb-app-internal":
				return []byte(`[]`), nil
			case strings.HasPrefix(joined, "network create --internal"):
				return nil, nil
			case joined == "network list --format json":
				return []byte(`[{"id":"default","status":{"ipv4Gateway":"192.168.64.1/24"}},{"id":"toolhive-rb-app-internal","status":{"ipv4Gateway":"192.168.71.1/24"}}]`), nil
			case strings.HasPrefix(joined, "image pull -- "):
				return nil, nil
			case joined == "inspect -- rb-app-egress" && len(egressArgs) == 0:
				return []byte(`[]`), nil
			case joined == "delete --force -- rb-app-egress" && len(egressArgs) == 0:
				return nil, &apple.CommandError{Stderr: "Error: container not found: rb-app-egress"}
			case len(args) > 3 && args[0] == "run" && args[3] == "rb-app-egress":
				egressArgs = slices.Clone(args)
				for idx, arg := range args {
					if arg == "--label" && idx+1 < len(args) && strings.HasPrefix(args[idx+1], apple.ProxyConfigDirLabel+"=") {
						egressConfigDir = strings.TrimPrefix(args[idx+1], apple.ProxyConfigDirLabel+"=")
					}
				}
				return nil, nil
			case joined == "inspect -- rb-app-egress":
				return []byte(`[{"status":"running","configuration":{"id":"rb-app-egress"},"networks":[{"network":"toolhive-rb-app-internal","ipv4Address":"192.168.71.2/24"}]}]`), nil
			case joined == "inspect -- rb-app":
				return []byte(`[]`), nil
			case len(args) > 3 && args[0] == "run" && args[3] == "rb-app":
				// Simulate MCP container deployment failure to trigger full rollback.
				return nil, errors.New("image architecture mismatch")
			case len(args) == 4 && args[0] == "delete" && args[1] == "--force" && args[2] == "--":
				deletedNames = append(deletedNames, args[3])
				return nil, nil
			case len(args) == 4 && args[0] == "network" && args[1] == "delete" && args[2] == "--":
				deletedNetwork = args[3]
				return nil, nil
			default:
				return nil, errors.New("unexpected command: " + joined)
			}
		}).AnyTimes()

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	apple.SetPollConfigForTest(client, 5*time.Millisecond, 500*time.Millisecond)

	_, err := client.DeployWorkload(
		context.Background(),
		"ghcr.io/example/mcp:latest",
		"rb-app",
		nil,
		nil,
		nil,
		&permissions.Profile{},
		"streamable-http",
		&runtime.DeployWorkloadOptions{
			AttachStdio:  false,
			ExposedPorts: map[string]struct{}{"8080/tcp": {}},
		},
		true,
	)

	require.Error(t, err)
	assert.Contains(t, egressArgs, "--cap-drop")
	assert.Contains(t, egressArgs, "ALL")
	assert.Contains(t, egressArgs, "/etc/toolhive-envoy/envoy.json")
	assert.Contains(t, deletedNames, "rb-app-egress")
	assert.Equal(t, "toolhive-rb-app-internal", deletedNetwork)

	require.NotEmpty(t, egressConfigDir)
	_, statErr := os.Stat(egressConfigDir)
	assert.True(t, os.IsNotExist(statErr), "rollback must remove egress temp config dir")
}

func TestClient_DeployWorkload_IsolatedRedeployDeletesMCPBeforeMutatingNetwork(t *testing.T) {
	t.Setenv("TOOLHIVE_NETWORK_PROXY", "squid")

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	var commandSequence []string
	runner.EXPECT().Run(gomock.Any(), apple.SwVersBinary, "-productVersion").Return([]byte("26.1\n"), nil)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			commandSequence = append(commandSequence, joined)
			switch joined {
			case "inspect -- redeploy-app":
				return []byte(`[{"status":"running","configuration":{"id":"redeploy-app","labels":{"toolhive-network-isolation":"true"}}}]`), nil
			case "delete --force -- redeploy-app":
				return nil, nil
			case "inspect -- redeploy-app-egress", "inspect -- redeploy-app-ingress":
				return []byte(`[]`), nil
			case "delete --force -- redeploy-app-egress", "delete --force -- redeploy-app-ingress",
				"network delete -- toolhive-redeploy-app-internal":
				return nil, nil
			case "network inspect -- toolhive-redeploy-app-internal":
				return nil, errors.New("stop after verifying order")
			default:
				return nil, errors.New("unexpected command: " + joined)
			}
		}).AnyTimes()

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	_, err := client.DeployWorkload(
		context.Background(),
		"ghcr.io/example/mcp:latest",
		"redeploy-app",
		nil,
		nil,
		nil,
		&permissions.Profile{},
		"stdio",
		&runtime.DeployWorkloadOptions{AttachStdio: true},
		true,
	)

	require.Error(t, err)
	assert.Equal(t, []string{
		"inspect -- redeploy-app",
		"delete --force -- redeploy-app",
		"inspect -- redeploy-app-egress",
		"delete --force -- redeploy-app-egress",
		"inspect -- redeploy-app-ingress",
		"delete --force -- redeploy-app-ingress",
		"network delete -- toolhive-redeploy-app-internal",
		"network inspect -- toolhive-redeploy-app-internal",
	}, commandSequence)
}

func TestClient_DeployWorkload_ToggleIsolationOffCleansUpPriorSidecars(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	var (
		deletedContainers []string
		deletedNetwork    string
		deployedArgs      []string
	)

	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case joined == "inspect -- toggle-app":
				return []byte(`[{"status":"running","configuration":{"id":"toggle-app","labels":{"toolhive":"true","toolhive-network-isolation":"true"}}}]`), nil
			case joined == "inspect -- toggle-app-egress":
				return []byte(`[{"status":"running","configuration":{"id":"toggle-app-egress"}}]`), nil
			case joined == "inspect -- toggle-app-ingress":
				return []byte(`[]`), nil
			case len(args) == 4 && args[0] == "delete" && args[1] == "--force" && args[2] == "--":
				deletedContainers = append(deletedContainers, args[3])
				return nil, nil
			case joined == "network delete -- toolhive-toggle-app-internal":
				deletedNetwork = "toolhive-toggle-app-internal"
				return nil, nil
			case len(args) > 3 && args[0] == "create" && args[3] == "toggle-app":
				deployedArgs = slices.Clone(args)
				return nil, nil
			default:
				return nil, errors.New("unexpected command: " + joined)
			}
		}).AnyTimes()

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	_, err := client.DeployWorkload(
		context.Background(),
		"ghcr.io/example/mcp:latest",
		"toggle-app",
		nil,
		nil,
		nil,
		&permissions.Profile{},
		"stdio",
		&runtime.DeployWorkloadOptions{AttachStdio: true},
		false,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"toggle-app", "toggle-app-egress", "toggle-app-ingress"}, deletedContainers)
	assert.Equal(t, "toolhive-toggle-app-internal", deletedNetwork)
	assert.Contains(t, deployedArgs, "toolhive-network-isolation=false")
}

func TestClient_DeployWorkload_EgressIPPollFailureCleansUpWithNonCanceledContext(t *testing.T) {
	t.Setenv("TOOLHIVE_NETWORK_PROXY", "squid")

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		egressDeleteCtxErr error
		egressDeleteCalled bool
		egressConfigDir    string
	)

	runner.EXPECT().Run(gomock.Any(), apple.SwVersBinary, "-productVersion").Return([]byte("26.1\n"), nil)
	runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
		DoAndReturn(func(callCtx context.Context, _ string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case joined == "inspect -- cancel-app":
				return []byte(`[]`), nil
			case joined == "network inspect -- toolhive-cancel-app-internal":
				return []byte(`[]`), nil
			case strings.HasPrefix(joined, "network create --internal"):
				return nil, nil
			case joined == "network list --format json":
				return []byte(`[{"id":"default","status":{"ipv4Gateway":"192.168.64.1/24"}},{"id":"toolhive-cancel-app-internal","status":{"ipv4Gateway":"192.168.72.1/24"}}]`), nil
			case strings.HasPrefix(joined, "image pull -- "):
				return nil, nil
			case joined == "inspect -- cancel-app-egress" && egressConfigDir == "":
				return []byte(`[]`), nil
			case joined == "delete --force -- cancel-app-egress" && egressConfigDir == "":
				return nil, &apple.CommandError{Stderr: "Error: container not found: cancel-app-egress"}
			case len(args) > 3 && args[0] == "run" && args[3] == "cancel-app-egress":
				for idx, arg := range args {
					if arg == "--label" && idx+1 < len(args) && strings.HasPrefix(args[idx+1], apple.ProxyConfigDirLabel+"=") {
						egressConfigDir = strings.TrimPrefix(args[idx+1], apple.ProxyConfigDirLabel+"=")
					}
				}
				return nil, nil
			case joined == "inspect -- cancel-app-egress":
				// Cancel caller context during IP polling.
				cancel()
				return nil, context.Canceled
			case joined == "delete --force -- cancel-app-egress":
				egressDeleteCalled = true
				egressDeleteCtxErr = callCtx.Err()
				return nil, nil
			case joined == "network delete -- toolhive-cancel-app-internal":
				return nil, nil
			default:
				return nil, errors.New("unexpected command: " + joined)
			}
		}).AnyTimes()

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	apple.SetPollConfigForTest(client, 5*time.Millisecond, 500*time.Millisecond)

	_, err := client.DeployWorkload(
		ctx,
		"ghcr.io/example/mcp:latest",
		"cancel-app",
		nil,
		nil,
		nil,
		&permissions.Profile{},
		"stdio",
		&runtime.DeployWorkloadOptions{AttachStdio: false},
		true,
	)
	require.Error(t, err)
	assert.True(t, egressDeleteCalled, "egress proxy must be deleted on IP poll failure")
	assert.NoError(t, egressDeleteCtxErr, "cleanup delete must not inherit canceled caller context")
	require.NotEmpty(t, egressConfigDir)
	_, statErr := os.Stat(egressConfigDir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestClient_DeployWorkload_MultiplePortBindingsAndIPv6(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	var runArgs []string
	gomock.InOrder(
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "multi-port").
			Return([]byte(`[]`), nil),
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, args ...string) ([]byte, error) {
				runArgs = slices.Clone(args)
				return nil, nil
			}),
	)

	client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
	hostPort, err := client.DeployWorkload(
		context.Background(),
		"ghcr.io/example/mcp:latest",
		"multi-port",
		nil,
		nil,
		nil,
		&permissions.Profile{},
		"streamable-http",
		&runtime.DeployWorkloadOptions{
			AttachStdio: false,
			PortBindings: map[string][]runtime.PortBinding{
				"8080/tcp": {{HostIP: "::1", HostPort: "18080"}},
				"9090/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
			},
		},
		false,
	)
	require.NoError(t, err)
	assert.Equal(t, 18080, hostPort)

	var portSpecs []string
	for idx, arg := range runArgs {
		if arg == "-p" && idx+1 < len(runArgs) {
			portSpecs = append(portSpecs, runArgs[idx+1])
		}
	}
	require.Len(t, portSpecs, 2)
	assert.Equal(t, "[::1]:18080:8080/tcp", portSpecs[0])
	assert.True(t, strings.HasPrefix(portSpecs[1], "127.0.0.1:"))
	assert.True(t, strings.HasSuffix(portSpecs[1], ":9090/tcp"))
	assert.NotContains(t, portSpecs[1], "127.0.0.1::", "secondary empty host port must be allocated")
}

func TestClient_AttachToWorkload(t *testing.T) {
	t.Parallel()

	t.Run("returns ErrWorkloadNotFound when container is missing", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
			Return([]byte(`[]`), nil)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		_, _, err := client.AttachToWorkload(context.Background(), "missing")
		require.Error(t, err)
		assert.ErrorIs(t, err, runtime.ErrWorkloadNotFound)
	})

	t.Run("rejects already-running container with ErrReattachNotSupported", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
			Return([]byte(`[{"status":"running","configuration":{"id":"stdio-running","labels":{"toolhive":"true"}}}]`), nil)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		_, _, err := client.AttachToWorkload(context.Background(), "stdio-running")
		require.Error(t, err)
		assert.ErrorIs(t, err, runtime.ErrReattachNotSupported)
	})

	t.Run("starts created container attached, streams stdout, and reaps on Close", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		proc := mocks.NewMockAttachedProcess(ctrl)

		stdinStub := &stubWriteCloser{}
		stdoutReader := io.NopCloser(strings.NewReader("hello-mcp\n"))
		stderrReader := io.NopCloser(strings.NewReader("debug stderr line\n"))

		waitCalled := make(chan struct{})
		proc.EXPECT().Stdin().Return(stdinStub).AnyTimes()
		proc.EXPECT().Stdout().Return(stdoutReader).AnyTimes()
		proc.EXPECT().Stderr().Return(stderrReader).Times(1)
		proc.EXPECT().Wait().DoAndReturn(func() error {
			close(waitCalled)
			return nil
		}).Times(1)

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"stopped","configuration":{"id":"thv-stdio-app-123","labels":{"toolhive":"true","toolhive-basename":"stdio-app"}}}]`), nil),
			runner.EXPECT().StartAttached(gomock.Any(), apple.ContainerBinary, "start", "-a", "-i", "--", "thv-stdio-app-123").
				Return(proc, nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "thv-stdio-app-123").
				Return([]byte(`[{"status":"running","configuration":{"id":"thv-stdio-app-123"}}]`), nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		apple.SetPollConfigForTest(client, 5*time.Millisecond, 500*time.Millisecond)

		stdin, stdout, err := client.AttachToWorkload(context.Background(), "stdio-app")
		require.NoError(t, err)
		assert.Same(t, stdinStub, stdin)

		data, readErr := io.ReadAll(stdout)
		require.NoError(t, readErr)
		assert.Equal(t, "hello-mcp\n", string(data))

		require.NoError(t, stdout.Close())

		select {
		case <-waitCalled:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for attached process to be reaped")
		}
	})

	t.Run("kills and cleans up process if container enters error state during startup", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		proc := mocks.NewMockAttachedProcess(ctrl)

		stdinStub := &stubWriteCloser{}
		stdoutReader := io.NopCloser(strings.NewReader(""))
		stderrReader := io.NopCloser(strings.NewReader(""))

		proc.EXPECT().Stdin().Return(stdinStub).AnyTimes()
		proc.EXPECT().Stdout().Return(stdoutReader).AnyTimes()
		proc.EXPECT().Stderr().Return(stderrReader).Times(1)
		proc.EXPECT().Kill().Return(nil).Times(1)
		proc.EXPECT().Wait().Return(nil).Times(1)

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"created","configuration":{"id":"stdio-err","labels":{"toolhive":"true"}}}]`), nil),
			runner.EXPECT().StartAttached(gomock.Any(), apple.ContainerBinary, "start", "-a", "-i", "--", "stdio-err").
				Return(proc, nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "stdio-err").
				Return([]byte(`[{"status":"error","configuration":{"id":"stdio-err"}}]`), nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		apple.SetPollConfigForTest(client, 5*time.Millisecond, 500*time.Millisecond)

		_, _, err := client.AttachToWorkload(context.Background(), "stdio-err")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error state")
	})

	t.Run("waits through initial stopped status from create -i until container starts running", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		proc := mocks.NewMockAttachedProcess(ctrl)

		stdinStub := &stubWriteCloser{}
		stdoutReader := io.NopCloser(strings.NewReader("ok"))
		stderrReader := io.NopCloser(strings.NewReader(""))

		proc.EXPECT().Stdin().Return(stdinStub).AnyTimes()
		proc.EXPECT().Stdout().Return(stdoutReader).AnyTimes()
		proc.EXPECT().Stderr().Return(stderrReader).Times(1)
		proc.EXPECT().Wait().Return(nil).Times(1)

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"stopped","configuration":{"id":"stdio-init","labels":{"toolhive":"true"}}}]`), nil),
			runner.EXPECT().StartAttached(gomock.Any(), apple.ContainerBinary, "start", "-a", "-i", "--", "stdio-init").
				Return(proc, nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "stdio-init").
				Return([]byte(`[{"status":"stopped","configuration":{"id":"stdio-init"}}]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "stdio-init").
				Return([]byte(`[{"status":"booting","configuration":{"id":"stdio-init"}}]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "stdio-init").
				Return([]byte(`[{"status":"running","configuration":{"id":"stdio-init"}}]`), nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		apple.SetPollConfigForTest(client, 5*time.Millisecond, 5*time.Second)

		_, stdout, err := client.AttachToWorkload(context.Background(), "stdio-init")
		require.NoError(t, err)
		require.NoError(t, stdout.Close())
	})

	t.Run("fails fast when container exits after starting or after startedDate advances", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name            string
			inspectPayloads []string
		}{
			{
				name: "exited after starting transition",
				inspectPayloads: []string{
					`[{"status":"starting","configuration":{"id":"stdio-exit"}}]`,
					`[{"status":"stopped","configuration":{"id":"stdio-exit"}}]`,
				},
			},
			{
				name: "stopped with updated startedDate",
				inspectPayloads: []string{
					`[{"status":{"state":"stopped","startedDate":"2026-11-24T12:00:00Z"},"configuration":{"id":"stdio-exit"}}]`,
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				ctrl := gomock.NewController(t)
				runner := mocks.NewMockCommandRunner(ctrl)
				proc := mocks.NewMockAttachedProcess(ctrl)

				proc.EXPECT().Stdin().Return(&stubWriteCloser{}).AnyTimes()
				proc.EXPECT().Stdout().Return(io.NopCloser(strings.NewReader(""))).AnyTimes()
				proc.EXPECT().Stderr().Return(io.NopCloser(strings.NewReader(""))).Times(1)
				proc.EXPECT().Kill().Return(nil).Times(1)
				proc.EXPECT().Wait().Return(nil).Times(1)

				orderedCalls := []any{
					runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
						Return([]byte(`[{"status":"stopped","configuration":{"id":"stdio-exit","labels":{"toolhive":"true"}}}]`), nil),
					runner.EXPECT().StartAttached(gomock.Any(), apple.ContainerBinary, "start", "-a", "-i", "--", "stdio-exit").
						Return(proc, nil),
				}
				for _, payload := range tc.inspectPayloads {
					orderedCalls = append(orderedCalls,
						runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "stdio-exit").
							Return([]byte(payload), nil),
					)
				}
				gomock.InOrder(orderedCalls...)

				client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
				apple.SetPollConfigForTest(client, 5*time.Millisecond, 5*time.Second)

				startTime := time.Now()
				_, _, err := client.AttachToWorkload(context.Background(), "stdio-exit")
				require.Error(t, err)
				assert.Contains(t, err.Error(), "exited during startup")
				assert.Less(t, time.Since(startTime), time.Second)
			})
		}
	})

	t.Run("decouples StartAttached context from caller timeout and kills hung process on Close", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		proc := mocks.NewMockAttachedProcess(ctrl)

		stderrReader, stderrWriter := io.Pipe()
		killed := make(chan struct{})
		var startCtx context.Context

		proc.EXPECT().Stdin().Return(&stubWriteCloser{}).AnyTimes()
		proc.EXPECT().Stdout().Return(io.NopCloser(strings.NewReader(""))).AnyTimes()
		proc.EXPECT().Stderr().Return(stderrReader).Times(1)
		proc.EXPECT().Kill().DoAndReturn(func() error {
			_ = stderrWriter.Close()
			close(killed)
			return nil
		}).Times(1)
		proc.EXPECT().Wait().DoAndReturn(func() error {
			<-killed
			return errors.New("signal: killed")
		}).Times(1)

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"created","configuration":{"id":"stdio-hung","labels":{"toolhive":"true"}}}]`), nil),
			runner.EXPECT().StartAttached(gomock.Any(), apple.ContainerBinary, "start", "-a", "-i", "--", "stdio-hung").
				DoAndReturn(func(ctx context.Context, _ string, _ ...string) (apple.AttachedProcess, error) {
					startCtx = ctx
					return proc, nil
				}),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "stdio-hung").
				Return([]byte(`[{"status":"running","configuration":{"id":"stdio-hung"}}]`), nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		apple.SetPollConfigForTest(client, 5*time.Millisecond, 500*time.Millisecond)

		attachCtx, cancel := context.WithCancel(context.Background())
		_, stdout, err := client.AttachToWorkload(attachCtx, "stdio-hung")
		require.NoError(t, err)

		// Canceling the attach handshake context must not cancel StartAttached's context.
		cancel()
		require.NotNil(t, startCtx)
		assert.NoError(t, startCtx.Err())

		startTime := time.Now()
		closeErr := stdout.Close()
		assert.Error(t, closeErr)
		assert.Less(t, time.Since(startTime), 2*time.Second, "Close must kill hung process after grace period instead of deadlocking")
	})
}

func TestClient_ListStopRemoveAndLogs(t *testing.T) {
	t.Parallel()

	t.Run("ListWorkloads excludes non-toolhive and auxiliary containers", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)
		listJSON := `[
			{"status":"running","configuration":{"id":"mcp-1","image":{"reference":"img:1"},"labels":{"toolhive":"true"}}},
			{"status":"running","configuration":{"id":"mcp-1-egress","image":{"reference":"squid:1"},"labels":{"toolhive":"true","toolhive-auxiliary-workload":"true"}}},
			{"status":"running","configuration":{"id":"other","image":{"reference":"nginx:1"},"labels":{"app":"other"}}}
		]`
		runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
			Return([]byte(listJSON), nil)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		workloads, err := client.ListWorkloads(context.Background())
		require.NoError(t, err)
		require.Len(t, workloads, 1)
		assert.Equal(t, "mcp-1", workloads[0].Name)
	})

	t.Run("StopWorkload resolves basename, stops container and proxies, and is idempotent on missing", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"running","configuration":{"id":"thv-iso-w-999","labels":{"toolhive":"true","toolhive-basename":"iso-w","toolhive-network-isolation":"true"}}}]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "stop", "--time", "30", "--", "thv-iso-w-999").Return(nil, nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "stop", "--time", "30", "--", "thv-iso-w-999-egress").Return(nil, nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "stop", "--time", "30", "--", "thv-iso-w-999-ingress").Return(nil, &apple.CommandError{Stderr: "Error: container not found: thv-iso-w-999-ingress"}),
			// Stopping a container in starting state also invokes container stop.
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"booting","configuration":{"id":"thv-boot-1","labels":{"toolhive":"true","toolhive-basename":"boot","toolhive-network-isolation":"false"}}}]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "stop", "--time", "30", "--", "thv-boot-1").Return(nil, nil),
			// Second stop on missing container succeeds idempotently.
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").Return([]byte(`[]`), nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		require.NoError(t, client.StopWorkload(context.Background(), "iso-w"))
		require.NoError(t, client.StopWorkload(context.Background(), "boot"))
		require.NoError(t, client.StopWorkload(context.Background(), "missing"))
	})

	t.Run("RemoveWorkload resolves basename and deletes container, proxies, config dirs, and internal network", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		egressDir, err := apple.WriteProxyConfigDir(apple.SquidDirPrefix, apple.SquidConfigFileName, []byte("test"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(egressDir) })

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"stopped","configuration":{"id":"thv-iso-w-999","labels":{"toolhive":"true","toolhive-basename":"iso-w","toolhive-network-isolation":"true"}}}]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "delete", "--force", "--", "thv-iso-w-999").Return(nil, nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "thv-iso-w-999-egress").
				Return([]byte(`[{"status":"stopped","configuration":{"id":"thv-iso-w-999-egress","labels":{"toolhive-config-dir":"`+filepath.ToSlash(egressDir)+`"}}}]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "delete", "--force", "--", "thv-iso-w-999-egress").Return(nil, nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "inspect", "--", "thv-iso-w-999-ingress").Return([]byte(`[]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "delete", "--force", "--", "thv-iso-w-999-ingress").Return(nil, &apple.CommandError{Stderr: "Error: container not found: thv-iso-w-999-ingress"}),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "network", "delete", "--", "toolhive-thv-iso-w-999-internal").Return(nil, nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))
		require.NoError(t, client.RemoveWorkload(context.Background(), "iso-w"))

		_, statErr := os.Stat(egressDir)
		assert.True(t, os.IsNotExist(statErr), "proxy config dir should be removed")
	})

	t.Run("GetWorkloadLogs supports basename lookup, lines, follow, conflict, and ErrWorkloadNotFound", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"running","configuration":{"id":"thv-log-app-1","labels":{"toolhive":"true","toolhive-basename":"log-app"}}}]`), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "logs", "-n", "50", "--", "thv-log-app-1").
				Return([]byte("line1\nline2\n"), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[{"status":"running","configuration":{"id":"thv-log-app-1","labels":{"toolhive":"true","toolhive-basename":"log-app"}}}]`), nil),
			runner.EXPECT().RunStreaming(gomock.Any(), gomock.Any(), gomock.Any(), apple.ContainerBinary, "logs", "--follow", "--", "thv-log-app-1").
				Return(nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(`[]`), nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))

		_, err := client.GetWorkloadLogs(context.Background(), "log-app", true, 10)
		require.Error(t, err)

		logs, err := client.GetWorkloadLogs(context.Background(), "log-app", false, 50)
		require.NoError(t, err)
		assert.Equal(t, "line1\nline2\n", logs)

		logs, err = client.GetWorkloadLogs(context.Background(), "log-app", true, 0)
		require.NoError(t, err)
		assert.Empty(t, logs)

		_, err = client.GetWorkloadLogs(context.Background(), "missing", false, 0)
		require.Error(t, err)
		assert.ErrorIs(t, err, runtime.ErrWorkloadNotFound)
	})

	t.Run("IsWorkloadRunning and GetWorkloadInfo prefer running basename match, fall back to exact name, and ignore non-toolhive", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := mocks.NewMockCommandRunner(ctrl)

		multiJSON := `[
			{"status":"stopped","configuration":{"id":"thv-app-old","image":{"reference":"alpine:2"},"labels":{"toolhive":"true","toolhive-basename":"app"}}},
			{"status":"running","configuration":{"id":"thv-app-new","image":{"reference":"alpine:3"},"labels":{"toolhive":"true","toolhive-basename":"app"}}},
			{"status":"stopped","configuration":{"id":"exact-only","image":{"reference":"alpine:4"},"labels":{"toolhive":"true"}}},
			{"status":"running","configuration":{"id":"foreign","image":{"reference":"nginx:1"},"labels":{"app":"other"}}},
			{"status":"running","configuration":{"id":"aux-egress","image":{"reference":"squid:1"},"labels":{"toolhive":"true","toolhive-auxiliary-workload":"true"}}}
		]`

		gomock.InOrder(
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(multiJSON), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(multiJSON), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(multiJSON), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(multiJSON), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(multiJSON), nil),
			runner.EXPECT().Run(gomock.Any(), apple.ContainerBinary, "list", "--all", "--format", "json").
				Return([]byte(multiJSON), nil),
		)

		client := apple.NewClientWithDependencies(runner, apple.NewImageManagerWithRunner(runner))

		running, err := client.IsWorkloadRunning(context.Background(), "app")
		require.NoError(t, err)
		assert.True(t, running)

		info, err := client.GetWorkloadInfo(context.Background(), "app")
		require.NoError(t, err)
		assert.Equal(t, "thv-app-new", info.Name)
		assert.Equal(t, runtime.WorkloadStatusRunning, info.State)
		assert.Equal(t, "alpine:3", info.Image)

		// Exact container ID fallback works even without toolhive-basename label.
		info, err = client.GetWorkloadInfo(context.Background(), "exact-only")
		require.NoError(t, err)
		assert.Equal(t, "exact-only", info.Name)
		assert.Equal(t, runtime.WorkloadStatusStopped, info.State)

		// Non-ToolHive containers are never returned.
		_, err = client.GetWorkloadInfo(context.Background(), "foreign")
		require.Error(t, err)
		assert.ErrorIs(t, err, runtime.ErrWorkloadNotFound)

		// Auxiliary containers are never returned as workloads.
		_, err = client.GetWorkloadInfo(context.Background(), "aux-egress")
		require.Error(t, err)
		assert.ErrorIs(t, err, runtime.ErrWorkloadNotFound)

		// Missing workload returns ErrWorkloadNotFound from IsWorkloadRunning as well.
		_, err = client.IsWorkloadRunning(context.Background(), "does-not-exist")
		require.Error(t, err)
		assert.ErrorIs(t, err, runtime.ErrWorkloadNotFound)
	})
}
