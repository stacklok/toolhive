package app

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var proxyStdioCmd = &cobra.Command{
	Use:   "stdio WORKLOAD-NAME SERVER_NAME",
	Short: "Create a stdio-based proxy for an MCP server",
	Long: `Create a stdio-based proxy that connects stdin/stdout to a target MCP server.

Example:
  thv proxy stdio my-workload my-server-proxy
`,
	Args: cobra.ExactArgs(2),
	RunE: proxyStdioCmdFunc,
}

func proxyStdioCmdFunc(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	workloadName := args[0]
	serverName := args[1]

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}
	stdioWorkload, err := workloadManager.GetWorkload(ctx, workloadName)
	if err != nil {
		return fmt.Errorf("failed to get workload %q: %w", workloadName, err)
	}
	logger.Infof("Starting stdio proxy for server=%q -> %s", serverName, workloadName)

	bridge, err := transport.NewStdioBridge(stdioWorkload.URL)
	if err != nil {
		return fmt.Errorf("failed to create stdio bridge: %w", err)
	}
	bridge.InitReady = make(chan struct{})
	bridge.Start(ctx)

	// Consume until interrupt
	close(bridge.InitReady)
	<-ctx.Done()
	logger.Info("Shutting down bridge")
	bridge.Shutdown()
	return nil
}
