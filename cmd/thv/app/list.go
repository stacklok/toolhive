// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/workloads"
	"github.com/stacklok/toolhive/pkg/workloads/upgrade"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List running MCP servers",
	Long: `List all MCP servers managed by ToolHive, including their status and configuration.

Examples:
  # List running MCP servers
  thv list

  # List all MCP servers (including stopped)
  thv list --all

  # List servers in JSON format
  thv list --format json

  # List servers in a specific group
  thv list --group production

  # List servers with specific labels
  thv list --label env=dev --label team=backend`,
	RunE: listCmdFunc,
}

var (
	listAll           bool
	listFormat        string
	listLabelFilter   []string
	listGroupFilter   string
	listCheckUpgrades bool
)

func init() {
	AddAllFlag(listCmd, &listAll, true, "Show all workloads (default shows running and auth_retrying)")
	AddFormatFlag(listCmd, &listFormat, FormatJSON, FormatText, "mcpservers")
	listCmd.Flags().StringArrayVarP(&listLabelFilter, "label", "l", []string{}, "Filter workloads by labels (format: key=value)")
	AddGroupFlag(listCmd, &listGroupFilter, false)
	listCmd.Flags().BoolVar(&listCheckUpgrades, "check-upgrades", false,
		"Check each workload for available upgrades against its source registry (performs a registry lookup)")

	listCmd.PreRunE = chainPreRunE(
		validateGroupFlag(),
		ValidateFormat(&listFormat, FormatJSON, FormatText, "mcpservers"),
		validateCheckUpgradesFormat(),
	)
}

// validateCheckUpgradesFormat rejects --check-upgrades with --format mcpservers.
// The mcpservers format emits client configuration and has no upgrade column, so
// the flag combination would perform a registry lookup per workload and then
// discard the result. Fail loudly rather than do hidden, wasted work.
func validateCheckUpgradesFormat() func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		if listCheckUpgrades && listFormat == "mcpservers" {
			return fmt.Errorf("--check-upgrades is not supported with --format mcpservers; use --format text or json")
		}
		return nil
	}
}

func listCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Instantiate the status manager.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create status manager: %w", err)
	}

	workloadList, err := manager.ListWorkloads(ctx, listAll, listLabelFilter...)
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
	}

	// Apply group filtering if specified
	if listGroupFilter != "" {
		workloadList, err = workloads.FilterByGroup(workloadList, listGroupFilter)
		if err != nil {
			return fmt.Errorf("failed to filter workloads by group: %w", err)
		}
	}

	// Optionally compute upgrade status for each workload. This is the only path
	// that performs a registry lookup; the default list stays offline-friendly.
	var upgrades map[string]*upgrade.CheckResult
	if listCheckUpgrades {
		upgrades, err = checkUpgradesForWorkloads(ctx, workloadList)
		if err != nil {
			return err
		}
	}

	// Output based on format
	switch listFormat {
	case FormatJSON:
		return printJSONOutput(workloadList, upgrades)
	case "mcpservers":
		return printMCPServersOutput(workloadList)
	default:
		// For text format, handle empty list with a message
		if len(workloadList) == 0 {
			if listGroupFilter != "" {
				fmt.Printf("No MCP servers found in group '%s'\n", listGroupFilter)
			} else {
				fmt.Println("No MCP servers found")
			}
			return nil
		}
		printTextOutput(workloadList, upgrades)
		return nil
	}
}

// checkUpgradesForWorkloads builds a single Checker, loads each workload's saved
// RunConfig, and returns the upgrade result keyed by workload name. Workloads
// whose config cannot be loaded are omitted from the map. The comparison logic
// lives entirely in pkg/workloads/upgrade; this only collects inputs.
func checkUpgradesForWorkloads(ctx context.Context, workloadList []core.Workload) (map[string]*upgrade.CheckResult, error) {
	checker, err := newUpgradeChecker()
	if err != nil {
		return nil, err
	}

	configs := make([]*runner.RunConfig, 0, len(workloadList))
	for _, wl := range workloadList {
		cfg, err := runner.LoadState(ctx, wl.Name)
		if err != nil {
			slog.Debug("skipping upgrade check for workload with unloadable config", "workload", wl.Name, "error", err)
			continue
		}
		configs = append(configs, cfg)
	}

	results := checker.CheckAll(ctx, configs)
	byName := make(map[string]*upgrade.CheckResult, len(results))
	for _, r := range results {
		byName[r.WorkloadName] = r
	}
	return byName, nil
}

// workloadWithUpgrade augments a workload with its optional upgrade-check
// result for JSON output when --check-upgrades is set.
type workloadWithUpgrade struct {
	core.Workload
	Upgrade *upgrade.CheckResult `json:"upgrade,omitempty"`
}

// printJSONOutput prints workload information in JSON format. When upgrades is
// non-nil, each workload is augmented with its upgrade-check result.
func printJSONOutput(workloadList []core.Workload, upgrades map[string]*upgrade.CheckResult) error {
	// Ensure we have a non-nil slice to avoid null in JSON output
	if workloadList == nil {
		workloadList = []core.Workload{}
	}

	// Sort workloads alphabetically by name for deterministic output
	core.SortWorkloadsByName(workloadList)

	// Without upgrade data, marshal the workloads directly to preserve the
	// existing output shape.
	if upgrades == nil {
		jsonData, err := json.MarshalIndent(workloadList, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(jsonData))
		return nil
	}

	augmented := make([]workloadWithUpgrade, 0, len(workloadList))
	for _, wl := range workloadList {
		augmented = append(augmented, workloadWithUpgrade{Workload: wl, Upgrade: upgrades[wl.Name]})
	}

	jsonData, err := json.MarshalIndent(augmented, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	fmt.Println(string(jsonData))
	return nil
}

// printMCPServersOutput prints MCP servers configuration in JSON format
// This format is compatible with client configuration files
func printMCPServersOutput(workloadList []core.Workload) error {
	// Create a map to hold the MCP servers configuration
	mcpServers := make(map[string]map[string]string)

	for _, c := range workloadList {
		// Add the MCP server to the map
		mcpServers[c.Name] = map[string]string{
			"url":  c.URL,
			"type": c.ProxyMode,
		}
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(map[string]interface{}{
		"mcpServers": mcpServers,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// Print JSON directly to stdout
	fmt.Println(string(jsonData))
	return nil
}

// printTextOutput prints workload information in text format. When upgrades is
// non-nil, an additional UPGRADE column reports each workload's upgrade status.
func printTextOutput(workloadList []core.Workload, upgrades map[string]*upgrade.CheckResult) {
	// Sort workloads alphabetically by name for deterministic output
	core.SortWorkloadsByName(workloadList)

	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	header := "NAME\tPACKAGE\tSTATUS\tURL\tPORT\tGROUP\tCREATED"
	if upgrades != nil {
		header += "\tUPGRADE"
	}
	if _, err := fmt.Fprintln(w, header); err != nil {
		slog.Warn(fmt.Sprintf("Failed to write output header: %v", err))
		return
	}

	// Print workload information
	for _, c := range workloadList {
		status := workloadStatusIndicator(c.Status)
		// For unhealthy workloads, include the status context if available
		if c.Status == rt.WorkloadStatusUnhealthy && c.StatusContext != "" {
			status = status + " (" + c.StatusContext + ")"
		}

		// Print workload information
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s",
			c.Name,
			c.Package,
			status,
			c.URL,
			c.Port,
			c.Group,
			c.CreatedAt,
		); err != nil {
			slog.Debug(fmt.Sprintf("Failed to write workload information: %v", err))
		}
		if upgrades != nil {
			upgradeStatus := "-"
			if r, ok := upgrades[c.Name]; ok {
				upgradeStatus = string(r.Status)
			}
			if _, err := fmt.Fprintf(w, "\t%s", upgradeStatus); err != nil {
				slog.Debug(fmt.Sprintf("Failed to write upgrade status: %v", err))
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			slog.Debug(fmt.Sprintf("Failed to write newline: %v", err))
		}
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		slog.Error(fmt.Sprintf("Failed to flush tabwriter: %v", err))
	}
}
