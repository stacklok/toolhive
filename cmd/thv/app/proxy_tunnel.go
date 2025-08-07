package app

import (
	"encoding/json"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	tunnelSourceTargetURI string
	tunnelWorkloadName    string
	tunnelProvider        string
)

var providerArgsJSON string

var proxyTunnelCmd = &cobra.Command{
	Use:   "tunnel [flags] SERVER_NAME",
	Short: "Create a tunnel proxy for exposing internal endpoints",
	Long: `Create a tunnel proxy for exposing internal endpoints.

Example:
  thv proxy tunnel --target-uri http://localhost:8080 my-server

Flags:
  --target-uri string   The target URI to tunnel to
  --workload-name string The name of the workload to use for the tunnel
  --tunnel-provider string The provider to use for the tunnel (e.g., "ngrok") - mandatory
`,
	Args: cobra.ExactArgs(1),
	RunE: proxyTunnelCmdFunc,
}

func init() {
	proxyTunnelCmd.Flags().StringVar(&tunnelSourceTargetURI, "target-uri", "", "The target URI to tunnel to (required)")
	proxyTunnelCmd.Flags().StringVar(&tunnelWorkloadName, "workload-name", "", "The name of the workload to use for the tunnel")
	proxyTunnelCmd.Flags().StringVar(&tunnelProvider, "tunnel-provider", "",
		"The provider to use for the tunnel (e.g., 'ngrok') - mandatory")
	proxyTunnelCmd.Flags().StringVar(&providerArgsJSON, "provider-args", "{}", "JSON object with provider-specific arguments")

	// Mark target-uri as required
	if err := proxyTunnelCmd.MarkFlagRequired("tunnel-provider"); err != nil {
		logger.Warnf("Warning: Failed to mark flag as required: %v", err)
	}
}

func proxyTunnelCmdFunc(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	serverName := args[0]

	// Ensure exactly one of target-uri or workload-name is set
	if (tunnelSourceTargetURI == "") == (tunnelWorkloadName == "") {
		return fmt.Errorf("you must provide exactly one of --target-uri or --workload-name")
	}

	// Validate provider
	provider, ok := types.SupportedTunnelProviders[tunnelProvider]
	if !ok {
		return fmt.Errorf("invalid tunnel provider %q, supported providers: %v", tunnelProvider, types.GetSupportedProviderNames())
	}

	var rawArgs map[string]any
	if err := json.Unmarshal([]byte(providerArgsJSON), &rawArgs); err != nil {
		return fmt.Errorf("invalid --provider-args: %w", err)
	}

	// validate target uri
	finalTargetURI := ""
	if tunnelSourceTargetURI != "" {
		err := validateProxyTargetURI(tunnelSourceTargetURI)
		if err != nil {
			return fmt.Errorf("invalid target URI: %w", err)
		}
		finalTargetURI = tunnelSourceTargetURI
	}

	// If workload name is provided, resolve the target URI from the workload
	if tunnelWorkloadName != "" {
		workloadManager, err := workloads.NewManager(ctx)
		if err != nil {
			return fmt.Errorf("failed to create workload manager: %w", err)
		}
		tunnelWorkload, err := workloadManager.GetWorkload(ctx, tunnelWorkloadName)
		if err != nil {
			return fmt.Errorf("failed to get workload %q: %w", tunnelWorkloadName, err)
		}
		finalTargetURI = tunnelWorkload.URL
	}

	// parse provider-specific configuration
	if err := provider.ParseConfig(rawArgs); err != nil {
		return fmt.Errorf("invalid provider config: %w", err)
	}

	// Start the tunnel using the selected provider
	if err := provider.StartTunnel(ctx, serverName, finalTargetURI); err != nil {
		return fmt.Errorf("failed to start tunnel: %w", err)
	}

	// Consume until interrupt
	<-ctx.Done()
	logger.Info("Shutting down tunnel")
	return nil
}
