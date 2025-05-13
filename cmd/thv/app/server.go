package app

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	s "github.com/stacklok/toolhive/pkg/api"
)

var (
	host string
	port int
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the ToolHive API server",
	Long:  `Starts the ToolHive API server and listen for HTTP requests.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Ensure server is shutdown gracefully on Ctrl+C.
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer cancel()
		address := fmt.Sprintf("%s:%d", host, port)
		return s.Serve(ctx, address)
	},
}

func init() {
	serveCmd.Flags().StringVar(&host, "host", "127.0.0.1", "Host address to bind the server to")
	serveCmd.Flags().IntVar(&port, "port", 8080, "Port to bind the server to")
}
