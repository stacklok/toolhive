// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
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

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/workloads"
	"github.com/stacklok/toolhive/pkg/workloads/upgrade"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Manage upgrades for MCP server workloads",
	Long: `Inspect and apply upgrades for registry-sourced MCP server workloads.

Upgrade checks compare each workload's current image and configuration against
the metadata reported by its source registry. Checks are an offline metadata
comparison and never pull images.`,
}

var upgradeCheckCmd = &cobra.Command{
	Use:   "check [workload-name]",
	Short: "Check workloads for available upgrades",
	Long: `Check whether registry-sourced workloads have a newer image available.

With no arguments, all workloads are checked (including stopped ones) and a
summary table is printed. When a workload name is given, a detailed report for
that single workload is printed, including any new environment variables the
candidate image declares and any configuration (posture) drift.

Examples:
  # Check all workloads
  thv upgrade check

  # Check a single workload with detailed output
  thv upgrade check my-server

  # Check all workloads in JSON format
  thv upgrade check --format json`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeMCPServerNames,
	RunE:              upgradeCheckCmdFunc,
}

var upgradeCheckFormat string

func init() {
	upgradeCmd.AddCommand(upgradeCheckCmd)

	AddFormatFlag(upgradeCheckCmd, &upgradeCheckFormat, FormatJSON, FormatText)
	upgradeCheckCmd.PreRunE = chainPreRunE(
		ValidateFormat(&upgradeCheckFormat, FormatJSON, FormatText),
	)
}

func upgradeCheckCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	checker, err := newUpgradeChecker()
	if err != nil {
		return err
	}

	// Single-workload mode: detailed report.
	if len(args) == 1 {
		cfg, err := runner.LoadState(ctx, args[0])
		if err != nil {
			return fmt.Errorf("failed to load configuration for workload %q: %w", args[0], err)
		}
		result, err := checker.Check(ctx, cfg)
		if err != nil {
			return fmt.Errorf("failed to check workload %q for upgrade: %w", args[0], err)
		}

		if upgradeCheckFormat == FormatJSON {
			return printUpgradeJSON([]*upgrade.CheckResult{result})
		}
		printUpgradeDetail(result)
		return nil
	}

	// Bulk mode: enumerate all workloads and check each.
	configs, err := loadWorkloadRunConfigs(ctx)
	if err != nil {
		return err
	}
	results := checker.CheckAll(ctx, configs)

	if upgradeCheckFormat == FormatJSON {
		return printUpgradeJSON(results)
	}
	printUpgradeTable(results)
	return nil
}

// newUpgradeChecker builds an upgrade.Checker backed by the default registry
// provider used throughout the CLI.
func newUpgradeChecker() (*upgrade.Checker, error) {
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get registry provider: %w", err)
	}
	checker, err := upgrade.NewChecker(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to create upgrade checker: %w", err)
	}
	return checker, nil
}

// loadWorkloadRunConfigs enumerates all workloads (mirroring the list command's
// path) and loads each workload's saved RunConfig. Configs that fail to load are
// skipped so a single corrupt entry does not abort the whole check.
func loadWorkloadRunConfigs(ctx context.Context) ([]*runner.RunConfig, error) {
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create workload manager: %w", err)
	}

	workloadList, err := manager.ListWorkloads(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads: %w", err)
	}

	// Sort by name for deterministic output, matching the `thv list` ordering.
	core.SortWorkloadsByName(workloadList)

	configs := make([]*runner.RunConfig, 0, len(workloadList))
	for _, wl := range workloadList {
		cfg, err := runner.LoadState(ctx, wl.Name)
		if err != nil {
			slog.Debug("skipping workload with unloadable config", "workload", wl.Name, "error", err)
			continue
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

// printUpgradeJSON prints upgrade check results as indented JSON.
func printUpgradeJSON(results []*upgrade.CheckResult) error {
	if results == nil {
		results = []*upgrade.CheckResult{}
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal upgrade check results: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// printUpgradeTable prints a one-line-per-workload summary of upgrade results.
func printUpgradeTable(results []*upgrade.CheckResult) {
	if len(results) == 0 {
		fmt.Println("No MCP servers found")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tSTATUS\tCURRENT\tCANDIDATE\tNEW-ENV\tPOSTURE"); err != nil {
		slog.Warn(fmt.Sprintf("Failed to write output header: %v", err))
		return
	}

	for _, r := range results {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			r.WorkloadName,
			r.Status,
			dashIfEmpty(r.CurrentImage),
			dashIfEmpty(r.CandidateImage),
			newEnvCount(r),
			postureMarker(r),
		); err != nil {
			slog.Debug(fmt.Sprintf("Failed to write upgrade result: %v", err))
		}
	}

	if err := w.Flush(); err != nil {
		slog.Error(fmt.Sprintf("Failed to flush tabwriter: %v", err))
	}
}

// printUpgradeDetail prints a verbose, single-workload upgrade report.
func printUpgradeDetail(r *upgrade.CheckResult) {
	fmt.Printf("Workload:  %s\n", r.WorkloadName)
	fmt.Printf("Status:    %s\n", r.Status)
	if r.RegistryServer != "" {
		fmt.Printf("Registry:  %s\n", r.RegistryServer)
	}
	if r.CurrentImage != "" {
		fmt.Printf("Current:   %s\n", r.CurrentImage)
	}
	if r.CandidateImage != "" {
		fmt.Printf("Candidate: %s\n", r.CandidateImage)
	}
	if r.Reason != "" {
		fmt.Printf("Reason:    %s\n", r.Reason)
	}

	if r.EnvVarDrift != nil && len(r.EnvVarDrift.Added) > 0 {
		fmt.Println("\nNew environment variables declared by the candidate:")
		for _, ev := range r.EnvVarDrift.Added {
			required := ""
			if ev.Required {
				required = " (required)"
			}
			desc := ""
			if ev.Description != "" {
				desc = ": " + ev.Description
			}
			fmt.Printf("  - %s%s%s\n", ev.Name, required, desc)
		}
	}

	if r.ConfigDrift != nil {
		fmt.Println("\nConfiguration (posture) drift:")
		if c := r.ConfigDrift.Transport; c != nil {
			fmt.Printf("  ⚠ transport: %s -> %s\n", c.From, c.To)
		}
		if c := r.ConfigDrift.NetworkIsolation; c != nil {
			fmt.Printf("  ⚠ network isolation: %t -> %t\n", c.From, c.To)
		}
		if c := r.ConfigDrift.PermissionProfile; c != nil {
			fmt.Printf("  ⚠ permission profile: %s -> %s\n", c.From, c.To)
		}
	}
}

// newEnvCount returns the number of new environment variables the candidate
// declares that the workload does not currently satisfy.
func newEnvCount(r *upgrade.CheckResult) int {
	if r.EnvVarDrift == nil {
		return 0
	}
	return len(r.EnvVarDrift.Added)
}

// postureMarker returns a warning marker when the candidate's posture differs
// from the workload's current configuration, or "-" otherwise.
func postureMarker(r *upgrade.CheckResult) string {
	if r.ConfigDrift != nil {
		return "⚠ drift"
	}
	return "-"
}

// dashIfEmpty returns "-" for an empty string, so columns stay aligned.
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
