package app

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	stdioHost         string
	stdioPort         int
	stdioWorkloadName string
)

var proxyStdioCmd = &cobra.Command{
	Use:   "stdio [flags] SERVER_NAME",
	Short: "Create a stdio-based proxy for an MCP server",
	Long: `Create a stdio-based proxy that connects stdin/stdout to a target MCP server.

Example:
  thv proxy stdio --host 127.0.0.1 --port 9000 --workload-name my-server my-server-proxy

Flags:
  --host           Host for the stdio proxy to bind (default: 127.0.0.1)
  --port           Port for the stdio proxy to bind (default: 0)
  --workload-name  Workload name for the proxy (required)
`,
	Args: cobra.ExactArgs(1),
	RunE: proxyStdioCmdFunc,
}

func proxyStdioCmdFunc(cmd *cobra.Command, args []string) error {
	ctx, stopSignal := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignal()

	serverName := args[0]

	// Normalize host/port
	host, err := ValidateAndNormaliseHostFlag(stdioHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", stdioHost)
	}
	port, err := networking.FindOrUsePort(stdioPort)
	if err != nil {
		return err
	}

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
	fmt.Println("Using workload:", stdioWorkload.Name)

	logger.Infof("Starting stdio proxy for server=%q on %s:%d -> %s",
		serverName, host, port, stdioWorkloadName)

	logger.Info("Proxy running (stdio mode). Press Ctrl+C to stop")
	<-ctx.Done()

}
