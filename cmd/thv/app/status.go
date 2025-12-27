package app

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var statusCmd = &cobra.Command{
	Use:   "status [workload-name]",
	Short: "Show detailed status of an MCP server",
	Long: `Show detailed status information for a specific MCP server managed by ToolHive.

This command provides comprehensive information about a workload including its
current status, health, uptime, configuration, and runtime details.

Examples:
  # Show status of a specific MCP server
  thv status my-server

  # Show status in JSON format
  thv status my-server --format json`,
	Args:              cobra.ExactArgs(1),
	RunE:              statusCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

var statusFormat string

func init() {
	statusCmd.Flags().StringVar(&statusFormat, "format", FormatText, "Output format (json, text)")
}

// StatusOutput represents the detailed status information for a workload
type StatusOutput struct {
	Name          string            `json:"name"`
	Status        string            `json:"status"`
	Health        string            `json:"health"`
	Uptime        string            `json:"uptime"`
	UptimeSeconds int64             `json:"uptime_seconds,omitempty"`
	Group         string            `json:"group"`
	Transport     string            `json:"transport"`
	ProxyMode     string            `json:"proxy_mode,omitempty"`
	URL           string            `json:"url"`
	Port          int               `json:"port"`
	PID           int               `json:"pid,omitempty"`
	Package       string            `json:"package,omitempty"`
	Remote        bool              `json:"remote,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	Labels        map[string]string `json:"labels,omitempty"`
	StatusContext string            `json:"status_context,omitempty"`
}

func statusCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	workloadName := args[0]

	// Create the workload manager
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Get the workload status
	workload, err := manager.GetWorkload(ctx, workloadName)
	if err != nil {
		return fmt.Errorf("failed to get workload status: %w", err)
	}

	// Build the status output
	output := buildStatusOutput(workload)

	// Output based on format
	switch statusFormat {
	case FormatJSON:
		return printStatusJSON(output)
	default:
		printStatusText(output)
		return nil
	}
}

// buildStatusOutput creates a StatusOutput from a core.Workload
func buildStatusOutput(workload core.Workload) StatusOutput {
	output := StatusOutput{
		Name:          workload.Name,
		Status:        string(workload.Status),
		Health:        determineHealth(workload.Status),
		Group:         workload.Group,
		Transport:     workload.TransportType.String(),
		ProxyMode:     workload.ProxyMode,
		URL:           workload.URL,
		Port:          workload.Port,
		Package:       workload.Package,
		Remote:        workload.Remote,
		CreatedAt:     workload.CreatedAt,
		Labels:        workload.Labels,
		StatusContext: workload.StatusContext,
	}

	// Calculate uptime if workload is running and has a valid CreatedAt
	if workload.Status == rt.WorkloadStatusRunning && !workload.CreatedAt.IsZero() {
		uptime := time.Since(workload.CreatedAt)
		output.Uptime = formatUptime(uptime)
		output.UptimeSeconds = int64(uptime.Seconds())
	} else {
		output.Uptime = "-"
	}

	// Set default group if empty
	if output.Group == "" {
		output.Group = "default"
	}

	return output
}

// determineHealth maps WorkloadStatus to a human-readable health string
func determineHealth(status rt.WorkloadStatus) string {
	switch status {
	case rt.WorkloadStatusRunning:
		return "healthy"
	case rt.WorkloadStatusUnhealthy:
		return "unhealthy"
	case rt.WorkloadStatusError:
		return "error"
	case rt.WorkloadStatusStarting:
		return "starting"
	case rt.WorkloadStatusStopping:
		return "stopping"
	case rt.WorkloadStatusStopped:
		return "stopped"
	case rt.WorkloadStatusRemoving:
		return "removing"
	case rt.WorkloadStatusUnauthenticated:
		return "unauthenticated"
	case rt.WorkloadStatusUnknown:
		return "unknown"
	}
	// This is unreachable, but required for exhaustive checks
	return "unknown"
}

// formatUptime formats a duration into a human-readable string
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}

// printStatusJSON prints the status in JSON format
func printStatusJSON(output StatusOutput) error {
	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	fmt.Println(string(jsonData))
	return nil
}

// printStatusText prints the status in human-readable text format
func printStatusText(output StatusOutput) {
	fmt.Printf("Name:       %s\n", output.Name)
	fmt.Printf("Status:     %s\n", output.Status)
	fmt.Printf("Health:     %s\n", output.Health)
	fmt.Printf("Uptime:     %s\n", output.Uptime)
	fmt.Printf("Group:      %s\n", output.Group)
	fmt.Printf("Transport:  %s\n", output.Transport)
	if output.ProxyMode != "" && output.ProxyMode != output.Transport {
		fmt.Printf("Proxy Mode: %s\n", output.ProxyMode)
	}
	fmt.Printf("URL:        %s\n", output.URL)
	fmt.Printf("Port:       %d\n", output.Port)
	if output.Package != "" {
		fmt.Printf("Package:    %s\n", output.Package)
	}
	if output.Remote {
		fmt.Printf("Remote:     yes\n")
	}
	if output.StatusContext != "" {
		fmt.Printf("Context:    %s\n", output.StatusContext)
	}
	if len(output.Labels) > 0 {
		fmt.Printf("Labels:\n")
		for k, v := range output.Labels {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
}
