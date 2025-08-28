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
	Use:   "stdio WORKLOAD-NAME",
	Short: "Create a stdio-based proxy for an MCP server",
	Long: `Create a stdio-based proxy that connects stdin/stdout to a target MCP server.

Example:
  thv proxy stdio my-workload
`,
	Args: cobra.ExactArgs(1),
	RunE: proxyStdioCmdFunc,
}

func proxyStdioCmdFunc(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	workloadName := args[0]
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	// just get details of workload without doing status check
	stdioWorkload, err := workloadManager.GetWorkload(ctx, workloadName)
	if err != nil {
		return fmt.Errorf("failed to get workload %q: %w", workloadName, err)
	}

	// check if we have details for the workload or not
	if stdioWorkload.URL == "" || stdioWorkload.TransportType == "" {
		return fmt.Errorf("workload %q does not have connection details (is it running?)", workloadName)
	}
	logger.Infof("Starting stdio proxy for workload=%q", workloadName)

	bridge, err := transport.NewStdioBridge(workloadName, stdioWorkload.URL, stdioWorkload.TransportType)
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
