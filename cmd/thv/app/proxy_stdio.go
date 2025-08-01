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

var (
	stdioWorkloadName string
)

var proxyStdioCmd = &cobra.Command{
	Use:   "stdio [flags] SERVER_NAME",
	Short: "Create a stdio-based proxy for an MCP server",
	Long: `Create a stdio-based proxy that connects stdin/stdout to a target MCP server.

Example:
  thv proxy stdio --workload-name my-server my-server-proxy

Flags:
  --workload-name  Workload name for the proxy (required)
`,
	Args: cobra.ExactArgs(1),
	RunE: proxyStdioCmdFunc,
}

func proxyStdioCmdFunc(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	serverName := args[0]

	// validate that workload name exists
	if stdioWorkloadName == "" {
		return fmt.Errorf("workload name must be specified with --workload-name")
	}

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}
	stdioWorkload, err := workloadManager.GetWorkload(ctx, stdioWorkloadName)
	if err != nil {
		return fmt.Errorf("failed to get workload %q: %w", stdioWorkloadName, err)
	}

	logger.Infof("Starting stdio proxy for server=%q -> %s", serverName, stdioWorkloadName)

	bridge, err := transport.NewStdioBridge(stdioWorkload.URL)
	if err != nil {
		return fmt.Errorf("failed to create stdio bridge: %w", err)
	}
	bridge.Start(ctx)

	// Consume until interrupt
	<-ctx.Done()
	logger.Info("Shutting down bridge")
	bridge.Shutdown()
	return nil
}
