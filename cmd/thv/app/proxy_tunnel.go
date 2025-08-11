package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	tunnelProvider   string
	providerArgsJSON string
)

var proxyTunnelCmd = &cobra.Command{
	Use:   "tunnel [flags] TARGET SERVER_NAME",
	Short: "Create a tunnel proxy for exposing internal endpoints",
	Long: `Create a tunnel proxy for exposing internal endpoints.

	TARGET may be either:
  • a URL (http://..., https://...) -> used directly as the target URI
  • a workload name                  -> resolved to its URL

Examples:
  thv proxy tunnel http://localhost:8080 my-server --tunnel-provider ngrok
  thv proxy tunnel my-workload        my-server --tunnel-provider ngrok

Flags:
  --tunnel-provider string   The provider to use for the tunnel (e.g., "ngrok") - mandatory
  --provider-args string     JSON object with provider-specific arguments: auth-token (mandatory),
  							 url, pooling, traffic-policy-file
  --dry-run                  If set, only validate the configuration without starting the tunnel

Examples:
  thv proxy tunnel --tunnel-provider ngrok --provider-args '{"auth-token": "your-token",
  "url": "https://example.com", "pooling": true}' http://localhost:8080 my-server
  thv proxy tunnel --tunnel-provider ngrok --provider-args '{"auth-token": "your-token",
  "traffic-policy-file": "/path/to/policy.yml"}' my-workload my-server
`,
	Args: cobra.ExactArgs(2),
	RunE: proxyTunnelCmdFunc,
}

func init() {
	proxyTunnelCmd.Flags().StringVar(&tunnelProvider, "tunnel-provider", "",
		"The provider to use for the tunnel (e.g., 'ngrok') - mandatory")
	proxyTunnelCmd.Flags().StringVar(&providerArgsJSON, "provider-args", "{}", "JSON object with provider-specific arguments")

	// Mark tunnel-provider as required
	if err := proxyTunnelCmd.MarkFlagRequired("tunnel-provider"); err != nil {
		logger.Warnf("Warning: Failed to mark flag as required: %v", err)
	}
}

func proxyTunnelCmdFunc(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	targetArg := args[0] // URL or workload name
	serverName := args[1]

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
	finalTargetURI, err := resolveTarget(ctx, targetArg)
	if err != nil {
		return err
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

func resolveTarget(ctx context.Context, target string) (string, error) {
	// If it's a URL, validate and return it
	if looksLikeURL(target) {
		if err := validateProxyTargetURI(target); err != nil {
			return "", fmt.Errorf("invalid target URI: %w", err)
		}
		return target, nil
	}

	// Otherwise treat as workload name
	workloadManager, err := workloads.NewManager(ctx, logger)
	if err != nil {
		return "", fmt.Errorf("failed to create workload manager: %w", err)
	}
	tunnelWorkload, err := workloadManager.GetWorkload(ctx, target)
	if err != nil {
		return "", fmt.Errorf("failed to get workload %q: %w", target, err)
	}
	if tunnelWorkload.URL == "" {
		return "", fmt.Errorf("workload %q has empty URL", target)
	}
	return tunnelWorkload.URL, nil
}

func looksLikeURL(s string) bool {
	// Fast-path for common schemes
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return true
	}
	// Fallback parse check
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}
