package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/networking"
	"github.com/stacklok/vibetool/pkg/transport"
)

var proxyCmd = &cobra.Command{
	Use:   "proxy [flags] SERVER_NAME",
	Short: "Spawn a transparent proxy for an MCP server",
	Long: `Spawn a transparent proxy that will redirect to an MCP server endpoint.
This command creates a standalone proxy without starting a container.`,
	Args: cobra.ExactArgs(1),
	RunE: proxyCmdFunc,
}

var (
	proxyPort       int
	proxyTargetPort int
	proxyTargetHost string
)

func init() {
	proxyCmd.Flags().IntVar(&proxyPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	proxyCmd.Flags().IntVar(&proxyTargetPort, "target-port", 0, "Port for the target MCP server (required)")
	proxyCmd.Flags().StringVar(&proxyTargetHost, "target-host", "localhost", "Host for the target MCP server")

	// Mark target-port as required
	if err := proxyCmd.MarkFlagRequired("target-port"); err != nil {
		fmt.Printf("Warning: Failed to mark flag as required: %v\n", err)
	}
}

func proxyCmdFunc(_ *cobra.Command, args []string) error {
	// Get the server name
	serverName := args[0]

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(proxyPort)
	if err != nil {
		return err
	}
	fmt.Printf("Using host port: %d\n", port)

	// Create the transparent proxy
	fmt.Printf("Setting up transparent proxy to forward from host port %d to %s:%d\n",
		port, proxyTargetHost, proxyTargetPort)

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create the transparent proxy
	proxy := transport.NewTransparentProxy(port, serverName, proxyTargetHost, proxyTargetPort)
	if err := proxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start proxy: %v", err)
	}

	fmt.Printf("Transparent proxy started for server %s on port %d -> %s:%d\n",
		serverName, port, proxyTargetHost, proxyTargetPort)
	fmt.Println("Press Ctrl+C to stop")

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigCh
	fmt.Printf("Received signal %s, stopping proxy...\n", sig)

	// Stop the proxy
	if err := proxy.Stop(ctx); err != nil {
		fmt.Printf("Warning: Failed to stop proxy: %v\n", err)
	}

	fmt.Printf("Proxy for server %s stopped\n", serverName)
	return nil
}
