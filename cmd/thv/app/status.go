package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var statusCmd = &cobra.Command{
	Use:               "status [workload-name]",
	Args:              cobra.ExactArgs(1),
	Short:             "Show detailed status of an MCP server",
	Long:              `Display detailed status information for a specific MCP server managed by ToolHive.`,
	ValidArgsFunction: completeMCPServerNames,
	RunE:              statusCmdFunc,
}

var statusFormat string

func init() {
	statusCmd.Flags().StringVar(&statusFormat, "format", FormatText, "Output format (json or text)")
}

func statusCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	workloadName := args[0]

	// Instantiate the status manager.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create status manager: %v", err)
	}

	workload, err := manager.GetWorkload(ctx, workloadName)
	if err != nil {
		return fmt.Errorf("failed to get workload status: %v", err)
	}
	stats, err := manager.GetWorkloadStats(ctx, workloadName)
	if err != nil {
		return fmt.Errorf("failed to get workload stats: %v", err)
	}

	// Output based on format
	switch statusFormat {
	case FormatJSON:
		return printStatusJSONOutput(workload, stats)
	default:
		printStatusTextOutput(workload, stats)
		return nil
	}
}

func printStatusJSONOutput(workload core.Workload, stats rt.WorkloadStats) error {
	uptime := ""
	if !workload.StartedAt.IsZero() {
		uptime = formatUptime(time.Since(workload.StartedAt))
	}

	output := struct {
		Name        string `json:"name"`
		Status      string `json:"status"`
		Health      string `json:"health,omitempty"`
		Package     string `json:"package"`
		URL         string `json:"url"`
		Port        int    `json:"port"`
		Transport   string `json:"transport"`
		ProxyMode   string `json:"proxy_mode,omitempty"`
		Group       string `json:"group,omitempty"`
		Uptime      string `json:"uptime,omitempty"`
		PID         int    `json:"pid,omitempty"`
		CPUUsage    string `json:"cpu_usage"`
		MemoryUsage string `json:"memory_usage"`
		MemoryLimit string `json:"memory_limit"`
	}{
		Name:        workload.Name,
		Status:      string(workload.Status),
		Health:      workload.StatusContext,
		Package:     workload.Package,
		URL:         workload.URL,
		Port:        workload.Port,
		Transport:   string(workload.TransportType),
		ProxyMode:   workload.ProxyMode,
		Group:       workload.Group,
		Uptime:      uptime,
		PID:         workload.ProcessID,
		CPUUsage:    fmt.Sprintf("%.2f%%", stats.CPUPercent),
		MemoryUsage: formatBytes(stats.MemoryUsage),
		MemoryLimit: formatBytes(stats.MemoryLimit),
	}

	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	fmt.Println(string(jsonData))
	return nil
}

func printStatusTextOutput(workload core.Workload, stats rt.WorkloadStats) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	status := string(workload.Status)
	if workload.Status == rt.WorkloadStatusUnauthenticated {
		status = "⚠️  " + status
	}

	// Print workload information in key-value format
	_, _ = fmt.Fprintf(w, "Name:\t%s\n", workload.Name)
	_, _ = fmt.Fprintf(w, "Status:\t%s\n", status)
	if workload.StatusContext != "" {
		_, _ = fmt.Fprintf(w, "Health:\t%s\n", workload.StatusContext)
	}
	_, _ = fmt.Fprintf(w, "Package:\t%s\n", workload.Package)
	_, _ = fmt.Fprintf(w, "URL:\t%s\n", workload.URL)
	_, _ = fmt.Fprintf(w, "Port:\t%d\n", workload.Port)
	_, _ = fmt.Fprintf(w, "Transport:\t%s\n", workload.TransportType)
	if workload.ProxyMode != "" {
		_, _ = fmt.Fprintf(w, "Proxy Mode:\t%s\n", workload.ProxyMode)
	}
	if workload.Group != "" {
		_, _ = fmt.Fprintf(w, "Group:\t%s\n", workload.Group)
	}
	_, _ = fmt.Fprintf(w, "Created:\t%s\n", workload.CreatedAt.Format("2006-01-02 15:04:05"))
	if workload.Remote {
		_, _ = fmt.Fprintf(w, "Remote:\t%v\n", workload.Remote)
	}
	if !workload.StartedAt.IsZero() {
		_, _ = fmt.Fprintf(w, "Uptime: \t%s\n", formatUptime(time.Since(workload.StartedAt)))
	}

	if workload.ProcessID != 0 {
		_, _ = fmt.Fprintf(w, "PID:\t%d\n", workload.ProcessID)
	}

	_, _ = fmt.Fprintf(w, "CPU Usage:\t%.2f%%\n", stats.CPUPercent)
	_, _ = fmt.Fprintf(w, "Memory Usage:\t%s / %s\n", formatBytes(stats.MemoryUsage), formatBytes(stats.MemoryLimit))

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		logger.Errorf("Warning: Failed to flush tabwriter: %v", err)
	}
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
